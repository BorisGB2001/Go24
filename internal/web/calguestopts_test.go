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
	"go24/internal/db"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestGuestOptionsHTTP: «Ещё» of the meeting's form — the invited may invite others (on the meeting's page), the
// organizer is not told of the answers, the attendee list is hidden; a form without the options keeps them; REST.
func TestGuestOptionsHTTP(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, eve, bob, zed := user("Анна"), user("Ева"), user("Боб"), user("Зед")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool), Keys: apikeys.NewStore(pool), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve, bob} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
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
		return res.StatusCode, string(raw)
	}
	start := time.Now().Truncate(time.Hour).AddDate(0, 0, 3)
	form := url.Values{"title": {"Планёрка"}, "start": {start.Format("2006-01-02T15:04")}, "end": {start.Add(time.Hour).Format("2006-01-02T15:04")},
		"invite": {eve, bob}, "opts_on": {"1"}, "allow_invite": {"on"}, "hide_guests": {"on"}}

	// the form has «Ещё», answers are told by default
	if _, page := do(anna, "GET", "/calendar/new", nil); !strings.Contains(page, `name="allow_invite"`) ||
		!strings.Contains(page, `name="notify_answers" checked`) || !strings.Contains(page, `name="hide_guests"`) {
		t.Fatal("no «Ещё» in the meeting's form")
	}
	if code, _ := do(anna, "POST", "/calendar/new", form); code != http.StatusSeeOther {
		t.Fatalf("saved: %d", code)
	}
	var id string
	var allow, quiet, hide bool
	must(pool.QueryRow(ctx, `SELECT id::text, allow_invite, quiet_answers, hide_guests FROM events WHERE title = 'Планёрка'`).Scan(&id, &allow, &quiet, &hide))
	if !allow || !quiet || !hide {
		t.Fatalf("options saved %v %v %v", allow, quiet, hide)
	}

	// eve sees herself and «и ещё 1», and the form to invite more
	_, page := do(eve, "GET", "/calendar/"+id, nil)
	if strings.Contains(page, "Боб") || !strings.Contains(page, "и ещё 1") || !strings.Contains(page, `action="/calendar/`+id+`/invite"`) {
		t.Fatal("eve's page of the meeting")
	}
	if _, page := do(anna, "GET", "/calendar/"+id, nil); !strings.Contains(page, "Боб") || strings.Contains(page, "и ещё 1") {
		t.Fatal("the organizer's page of the meeting")
	}
	if code, _ := do(eve, "POST", "/calendar/"+id+"/invite", url.Values{"invite": {zed}}); code != http.StatusSeeOther {
		t.Fatalf("eve invited: %d", code)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM event_attendees WHERE event_id::text = $1 AND user_id = $2`, id, zed).Scan(&n))
	if n != 1 {
		t.Fatal("zed not invited")
	}
	if _, page := do(anna, "GET", "/notifications", nil); !strings.Contains(page, "пригласил(а) на вашу встречу: Зед") {
		t.Fatal("the organizer's notification of the invited")
	}
	// the one who declined has no form; the organizer hears of no answer
	do(bob, "POST", "/calendar/"+id+"/respond", url.Values{"status": {"declined"}})
	if _, page := do(bob, "GET", "/calendar/"+id, nil); strings.Contains(page, `/invite"`) {
		t.Fatal("the declined may invite")
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'event_reply'`, anna).Scan(&n))
	if n != 0 {
		t.Fatal("the organizer was told of a quiet answer")
	}
	if code, _ := do(bob, "POST", "/calendar/"+id+"/invite", url.Values{"invite": {anna}}); code == http.StatusSeeOther {
		t.Fatal("the declined invited")
	}

	// a form without «Ещё» keeps the options
	form.Del("opts_on")
	form.Del("allow_invite")
	form["invite"] = []string{eve, bob, zed}
	do(anna, "POST", "/calendar/"+id, form)
	must(pool.QueryRow(ctx, `SELECT allow_invite, hide_guests FROM events WHERE id::text = $1`, id).Scan(&allow, &hide))
	if !allow || !hide {
		t.Fatal("the options lost by a form without them")
	}

	// REST
	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	evek, err := srv.Keys.Create(ctx, eve, "t", apikeys.AllScopes, time.Hour)
	must(err)
	call := func(key, method, path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+key)
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
	got := call(token, "PATCH", "/api/v1/calendar/events/"+id, map[string]any{"quiet_answers": false, "hide_guests": false}, http.StatusOK)
	if got["allow_invite"] != true || got["quiet_answers"] != nil || got["hide_guests"] != nil {
		t.Fatalf("REST options %v", got)
	}
	call(token, "PATCH", "/api/v1/calendar/events/"+id, map[string]any{"hide_guests": true}, http.StatusOK)
	got = call(evek, "POST", "/api/v1/calendar/events/"+id+"/invite", map[string]any{"attendees": []string{anna, eve}}, http.StatusOK)
	if len(got["attendees"].([]any)) != 1 || got["others"].(float64) != 2 {
		t.Fatalf("REST invite %v", got)
	}
	call(token, "PATCH", "/api/v1/calendar/events/"+id, map[string]any{"allow_invite": false}, http.StatusOK)
	call(evek, "POST", "/api/v1/calendar/events/"+id+"/invite", map[string]any{"attendees": []string{anna}}, http.StatusForbidden)
}
