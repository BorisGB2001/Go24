package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestEventCRMHTTP: «CRM» of a meeting — the form links a deal (one from the card: «Запланировать встречу» comes
// back to it), the meeting's page shows it, the card lists the meeting; a card the person may not see is not linked;
// a form without the cards unlinks them; REST links a lead and shows it.
func TestEventCRMHTTP(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Менеджер", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка станков", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка с сайта", Responsible: crm.Ref{ID: anna}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	card := "/crm/deals/" + deal
	start := time.Now().Truncate(time.Hour).AddDate(0, 0, 2)
	meeting := func(extra url.Values) url.Values {
		f := url.Values{"title": {"Показ станков"}, "start": {start.Format("2006-01-02T15:04")},
			"end": {start.Add(time.Hour).Format("2006-01-02T15:04")}, "invite": {eve}, "crm_on": {"1"}}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}

	// «Запланировать встречу» of the card: the form has the deal and comes back to the card
	_, page, _ := do(anna, "GET", card, nil)
	if !strings.Contains(page, `href="/calendar/new?crm=deal:`+deal+`"`) {
		t.Fatal("no «Запланировать встречу» on the card")
	}
	_, page, _ = do(anna, "GET", "/calendar/new?crm=deal:"+deal, nil)
	for _, want := range []string{`name="crm" value="deal:` + deal + `"`, "Сделка: Поставка станков", `name="crm_back" value="` + card + `"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the new meeting's form has no %q: %s", want, page)
		}
	}
	if code, _, _ := do(eve, "GET", "/calendar/new?crm=deal:"+deal, nil); code == http.StatusOK {
		t.Fatal("a meeting planned from a card the person may not see")
	}
	code, _, loc := do(anna, "POST", "/calendar/new", meeting(url.Values{"crm": {"deal:" + deal}, "crm_back": {card}}))
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, card+"?") {
		t.Fatalf("save: %d → %s", code, loc)
	}
	var ev string
	must(pool.QueryRow(ctx, `SELECT id::text FROM events WHERE title = 'Показ станков'`).Scan(&ev))
	if refs, err := srv.Calendar.CRM(ctx, ev); err != nil || len(refs) != 1 || refs[0].Title != "Поставка станков" {
		t.Fatalf("links %+v %v", refs, err)
	}
	_, page, _ = do(eve, "GET", "/calendar/"+ev, nil) // the invited see the card's title
	if !strings.Contains(page, `href="`+card+`"`) {
		t.Fatal("the meeting's page has no deal")
	}
	_, page, _ = do(anna, "GET", card, nil)
	if !strings.Contains(page, `href="/calendar/`+ev+`"`) || !strings.Contains(page, "Показ станков") {
		t.Fatal("the card has no meeting")
	}

	// Ева may not see the deal: her own meeting does not link it
	if code, _, _ := do(eve, "POST", "/calendar/new", meeting(url.Values{"title": {"Своя"}, "invite": nil, "crm": {"deal:" + deal}})); code == http.StatusSeeOther {
		t.Fatal("a card the person may not see linked")
	}
	do(eve, "POST", "/calendar/new", meeting(url.Values{"title": {"Своя"}, "crm": {"deal:x"}}))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE title = 'Своя'`).Scan(&n))
	if n != 0 {
		t.Fatal("a meeting saved with a bad card")
	}

	// the form without the cards unlinks them; a form without the field (not the organizer's) keeps them
	must(srv.Calendar.SetCRM(ctx, anna, ev, []calendar.CRMRef{{Entity: "deal", ID: deal}, {Entity: "lead", ID: lead}}))
	if code, _, _ := do(anna, "POST", "/calendar/"+ev, meeting(url.Values{"crm": {"lead:" + lead}})); code != http.StatusSeeOther {
		t.Fatalf("edit: %d", code)
	}
	if refs, _ := srv.Calendar.CRM(ctx, ev); len(refs) != 1 || refs[0].Entity != "lead" {
		t.Fatalf("after the edit %+v", refs)
	}
	if err := srv.Calendar.SetCRM(ctx, eve, ev, nil); err != calendar.ErrForbidden {
		t.Fatalf("someone else's meeting unlinked: %v", err)
	}

	// REST: crm of a new meeting, shown by GET
	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	v := call("POST", "/api/v1/calendar/events", map[string]any{"title": "Созвон", "start": start.Add(3 * time.Hour),
		"crm": []map[string]string{{"entity": "lead", "id": lead}}}, http.StatusCreated)
	got := call("GET", "/api/v1/calendar/events/"+v["id"].(string), nil, http.StatusOK)
	if crmList, _ := got["crm"].([]any); len(crmList) != 1 || crmList[0].(map[string]any)["title"] != "Заявка с сайта" {
		t.Fatalf("REST crm: %v", got["crm"])
	}
	call("POST", "/api/v1/calendar/events", map[string]any{"title": "x", "start": start, "crm": []map[string]string{{"entity": "task", "id": lead}}}, http.StatusUnprocessableEntity)

	// the CRM activities of the responsible are in their calendar by the deadline (not the done ones, not others')
	callAt := start.Add(5 * time.Hour)
	act, err := srv.CRM.AddActivity(ctx, anna, crm.Activity{Entity: "deal", EntityID: deal, Kind: "call", Subject: "Позвонить клиенту",
		Deadline: callAt, Responsible: crm.Ref{ID: anna}})
	must(err)
	done, err := srv.CRM.AddActivity(ctx, anna, crm.Activity{Entity: "deal", EntityID: deal, Kind: "todo", Subject: "Сделанное",
		Deadline: callAt, Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.CompleteActivity(ctx, anna, done)
	must(err)
	week := "/calendar?view=week&date=" + callAt.Format("2006-01-02")
	_, page, _ = do(anna, "GET", week, nil)
	if !strings.Contains(page, `data-cal="crm" href="`+card+`"`) || !strings.Contains(page, "Позвонить клиенту") || strings.Contains(page, "Сделанное") {
		t.Fatal("the calendar has no CRM activity")
	}
	if _, page, _ = do(eve, "GET", week, nil); strings.Contains(page, "Позвонить клиенту") {
		t.Fatal("someone else's CRM activity in the calendar")
	}
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/calendar/events?from="+callAt.Format("2006-01-02")+"&to="+callAt.AddDate(0, 0, 1).Format("2006-01-02"), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	must(err)
	var list []any
	must(json.NewDecoder(res.Body).Decode(&list))
	res.Body.Close()
	found := false
	for _, x := range list {
		if x := x.(map[string]any); x["kind"] == "crm" {
			found = x["id"] == act && x["activity_kind"] == "call" && x["crm"].([]any)[0].(map[string]any)["id"] == deal
		}
	}
	if !found {
		t.Fatalf("REST has no CRM activity: %v", list)
	}
}
