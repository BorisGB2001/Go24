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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMModeHTTP: only an administrator sets «Режим работы CRM»; going simple converts the leads in work, hides the
// «Лиды» tab and tells of the mode on the leads' page.
func TestCRMModeHTTP(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Name: "Олег", Phone: "+7 900 555-44-33", Responsible: crm.Ref{ID: anna}})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, path string, form url.Values) (int, string, string) {
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
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Location")
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)
	simple := url.Values{"mode": {"simple"}, "convert": {"1"}}

	if _, body, _ := do(admin, "/crm/deals", nil); !strings.Contains(body, `href="/crm/leads"`) || !strings.Contains(body, `action="/crm/mode"`) {
		t.Fatal("classic: the «Лиды» tab and the mode for the administrator")
	}
	if _, body, _ := do(worker, "/crm/deals", nil); strings.Contains(body, `action="/crm/mode"`) {
		t.Fatal("the mode is the administrators'")
	}
	do(worker, "/crm/mode", simple)
	if on, _ := srv.CRM.Simple(ctx); on {
		t.Fatal("an employee may not set the mode")
	}
	if c, _, loc := do(admin, "/crm/mode", simple); c != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("set simple: %d %s", c, loc)
	}
	if l, _ := srv.CRM.Lead(ctx, lead); l.DealID == "" || l.ContactID == "" {
		t.Fatalf("the lead in work converted: %+v", l)
	}
	if _, body, _ := do(worker, "/crm/deals", nil); strings.Contains(body, `href="/crm/leads"`) {
		t.Fatal("simple: no «Лиды» tab")
	}
	if c, body, _ := do(worker, "/crm/leads", nil); c != http.StatusOK || !strings.Contains(body, "простом режиме") && !strings.Contains(body, "simple mode") {
		t.Fatalf("simple: the leads' page tells of the mode: %d", c)
	}
}
