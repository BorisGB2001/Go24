package web

import (
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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestCallLists: «Обзвон» — the checked leads of the CRM list become a call list of their clients' phones (a lead
// without a phone is skipped); «Позвонить» and the call out to the number in the log give the result, the employee
// sets it by hand too; another employee does not see the list, «Завершить» and «Удалить».
func TestCallLists(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, inner_phone) VALUES ('anna', 'Анна', 'anna@t', '101') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	store := telephony.NewStore(pool, "secret")
	must(store.SetNumber(ctx, anna, "101"))
	must(store.SetEnabled(ctx, anna, true))
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, pages: pages, CRM: crm.NewStore(pool),
		Telephony: &telephony.PBX{Store: store, Key: "astkey", Records: t.TempDir(), ARI: &telephony.ARI{URL: "http://127.0.0.1:1", User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id, name string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Name: name}))
		return rec.Result().Cookies()[0]
	}
	annaC, bobC := cookie(anna, "Анна"), cookie(bob, "Борис")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(c)
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}

	lead := func(title, name, phone string) string {
		id, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: title, Name: name, Phone: phone, Responsible: crm.Ref{ID: anna}})
		must(err)
		return id
	}
	l1, l2, l3 := lead("Заявка с сайта", "Иван Петров", "+7 (900) 111-22-33"), lead("Без телефона", "Пётр", ""), lead("Звонок", "Мария", "89002223344")

	// the list of leads offers «Начать обзвон»; the leads without a phone are skipped
	if _, _, body := do(annaC, "GET", "/crm/leads?view=list", nil); !strings.Contains(body, `value="calllist"`) {
		t.Fatal("no «Начать обзвон» in the list of leads")
	}
	st, loc, _ := do(annaC, "POST", "/crm/bulk/leads", url.Values{"op": {"calllist"}, "id": {l1, l2, l3}})
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/telephony/calllists/") || !strings.Contains(loc, "msg=") {
		t.Fatalf("start a call list: %d %s", st, loc)
	}
	id := strings.TrimPrefix(loc[:strings.Index(loc, "?")], "/telephony/calllists/")
	l, items, err := store.CallList(ctx, id)
	must(err)
	if l.Owner != anna || l.Entity != "lead" || l.Total != 2 || len(items) != 2 || items[0].Title != "Заявка с сайта" ||
		items[0].Name != "Иван Петров" || items[1].Phone != "89002223344" || items[0].Status != telephony.ItemWait {
		t.Fatalf("the call list: %+v %+v", l, items)
	}
	_, _, body := do(annaC, "GET", "/telephony/calllists/"+id, nil)
	if !strings.Contains(body, `href="tel:&#43;79001112233"`) || !strings.Contains(body, "/items/"+items[0].ID+"/dial") {
		t.Fatalf("the page: no «Позвонить» for the first client\n%.2000s", body)
	}
	if st, _, _ := do(bobC, "GET", "/telephony/calllists/"+id, nil); st == http.StatusOK {
		t.Fatal("another employee sees the call list")
	}
	if _, _, body := do(bobC, "GET", "/telephony/calllists", nil); strings.Contains(body, id) {
		t.Fatal("another employee's lists show it")
	}

	// «Позвонить»: the next call out to the number gives the result
	if st, _, body := do(annaC, "POST", "/telephony/calllists/"+id+"/items/"+items[0].ID+"/dial", url.Values{}); st != http.StatusOK || !strings.Contains(body, `"ok": true`) {
		t.Fatalf("dial: %d %s", st, body)
	}
	if st, _, _ := do(bobC, "POST", "/telephony/calllists/"+id+"/items/"+items[0].ID+"/dial", url.Values{}); st == http.StatusOK {
		t.Fatal("another employee dials from the list")
	}
	_, items, err = store.CallList(ctx, id)
	must(err)
	if items[0].Status != telephony.ItemWait || items[0].Dialed == nil {
		t.Fatalf("dialed, the call not logged yet: %+v", items[0])
	}
	now := time.Now()
	_, err = store.SaveCall(ctx, telephony.CDR{UniqueID: "c1", From: "101", To: "89001112233", Dial: "ANSWER", Start: now, Answer: now.Add(5 * time.Second), End: now.Add(65 * time.Second)})
	must(err)
	l, items, err = store.CallList(ctx, id)
	must(err)
	if items[0].Status != telephony.ItemAnswered || items[0].CallID == "" || items[0].Seconds != 60 || l.Done != 1 {
		t.Fatalf("the answered call's result: %+v %+v", l, items[0])
	}
	if telephony.Current(items) != 1 {
		t.Fatalf("the next client: %d", telephony.Current(items))
	}

	// the second: nobody answers; then she calls again and marks it by hand, skips it
	must(store.DialItem(ctx, id, items[1].ID))
	_, err = store.SaveCall(ctx, telephony.CDR{UniqueID: "c2", From: "101", To: "+79002223344", Dial: "NOANSWER", Start: time.Now()})
	must(err)
	_, items, err = store.CallList(ctx, id)
	must(err)
	if items[1].Status != telephony.ItemNoAnswer || telephony.Current(items) != -1 {
		t.Fatalf("the unanswered call's result: %+v", items[1])
	}
	st, loc, _ = do(annaC, "POST", "/telephony/calllists/"+id+"/items/"+items[1].ID, url.Values{"status": {"skipped"}})
	if st != http.StatusSeeOther || loc != "/telephony/calllists/"+id {
		t.Fatalf("skip: %d %s", st, loc)
	}
	if st, _, _ := do(annaC, "POST", "/telephony/calllists/"+id+"/items/"+items[1].ID, url.Values{"status": {"nonsense"}}); st == http.StatusSeeOther {
		_, items, _ = store.CallList(ctx, id)
		if items[1].Status != telephony.ItemSkipped {
			t.Fatalf("a bad result is taken: %+v", items[1])
		}
	}
	_, _, body = do(annaC, "GET", "/telephony/calllists/"+id, nil)
	if !strings.Contains(body, bundle.T("ru", "tel.cl_all_done")) || !strings.Contains(body, bundle.T("ru", "tel.cl_st.skipped")) {
		t.Fatalf("the page after all the calls\n%.3000s", body)
	}

	// REST: the key owner's lists, a list with its clients, a new one and a result
	key, err := keys.Create(ctx, anna, "k", []string{"telephony:read", "telephony:write"}, time.Hour)
	must(err)
	{
		api := func(method, path, body string) (int, map[string]any) {
			req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			must(err)
			defer res.Body.Close()
			var out map[string]any
			json.NewDecoder(res.Body).Decode(&out)
			return res.StatusCode, out
		}
		if st, out := api("GET", "/api/v1/telephony/calllists/"+id, ""); st != http.StatusOK || len(out["items"].([]any)) != 2 {
			t.Fatalf("REST list: %d %v", st, out)
		}
		st, out := api("POST", "/api/v1/telephony/calllists", `{"entity":"lead","ids":["`+l3+`","`+l2+`"],"title":"Повторный"}`)
		if st != http.StatusCreated || out["skipped"].(float64) != 1 || out["calllist"].(map[string]any)["title"] != "Повторный" {
			t.Fatalf("REST new: %d %v", st, out)
		}
		if st, out := api("GET", "/api/v1/telephony/calllists", ""); st != http.StatusOK || len(out["calllists"].([]any)) != 2 {
			t.Fatalf("REST lists: %d %v", st, out)
		}
		if st, out := api("PATCH", "/api/v1/telephony/calllists/"+id+"/items/"+items[1].ID, `{"status":"wait"}`); st != http.StatusOK ||
			out["items"].([]any)[1].(map[string]any)["status"] != "wait" {
			t.Fatalf("REST result: %d %v", st, out)
		}
	}

	// «Завершить обзвон», «Продолжить», «Удалить»
	do(annaC, "POST", "/telephony/calllists/"+id, url.Values{"closed": {"1"}})
	if l, _, _ := store.CallList(ctx, id); l.Closed == nil {
		t.Fatal("not finished")
	}
	do(annaC, "POST", "/telephony/calllists/"+id, url.Values{"closed": {"0"}})
	if l, _, _ := store.CallList(ctx, id); l.Closed != nil {
		t.Fatal("not resumed")
	}
	if st, _, _ := do(bobC, "POST", "/telephony/calllists/"+id, url.Values{"delete": {"1"}}); st == http.StatusSeeOther {
		if _, _, err := store.CallList(ctx, id); err != nil {
			t.Fatal("another employee deleted the list")
		}
	}
	do(annaC, "POST", "/telephony/calllists/"+id, url.Values{"delete": {"1"}})
	if _, _, err := store.CallList(ctx, id); err != telephony.ErrNotFound {
		t.Fatalf("not deleted: %v", err)
	}
}
