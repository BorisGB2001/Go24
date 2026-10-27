package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMDuplicatesHTTP: «Дубликаты» of the CRM — the page shows the group of two contacts with one phone, «Не
// дубликаты» hides it, and after a third one «Объединить» leaves the main card only.
func TestCRMDuplicatesHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	a, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван Петров", Phone: "+7 900 123-45-67", Responsible: crm.Ref{ID: anna}})
	must(err)
	b, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Ваня", Phone: "89001234567", Responsible: crm.Ref{ID: anna}})
	must(err)

	code, body, _ := do("/crm/duplicates", nil)
	if code != 200 || !strings.Contains(body, "Иван Петров") || !strings.Contains(body, "Ваня") || !strings.Contains(body, `value="`+a+`" checked`) {
		t.Fatalf("page: %d %s", code, body)
	}
	if code, _, loc := do("/crm/duplicates", url.Values{"op": {"skip"}, "entity": {"contact"}, "by": {"phone"}, "ids": {a, b}}); code != 303 || !strings.Contains(loc, "msg=") {
		t.Fatalf("skip: %d %s", code, loc)
	}
	if _, body, _ := do("/crm/duplicates?entity=contact&by=phone", nil); strings.Contains(body, "Ваня") {
		t.Fatal("skipped pair still shown")
	}
	c, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "И. Петров", Phone: "8 (900) 123 45 67", Responsible: crm.Ref{ID: anna}})
	must(err)
	var n int
	do("/crm/duplicates", url.Values{"op": {"merge"}, "entity": {"lead"}, "keep": {a}, "ids": {a, c}}) // leads are not merged here
	if must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_contacts`).Scan(&n)); n != 3 {
		t.Fatalf("contacts after a lead merge: %d", n)
	}
	if code, _, loc := do("/crm/duplicates", url.Values{"op": {"merge"}, "entity": {"contact"}, "by": {"phone"}, "keep": {a}, "ids": {a, b, c}}); code != 303 || !strings.Contains(loc, "msg=") {
		t.Fatalf("merge: %d %s", code, loc)
	}
	if must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_contacts`).Scan(&n)); n != 1 {
		t.Fatalf("contacts after merge: %d", n)
	}
	if _, body, _ := do("/crm/duplicates", nil); !strings.Contains(body, srv.tr(httptest.NewRequest("GET", "/", nil), "crm.dups_none")) {
		t.Fatal("no groups left expected")
	}
}
