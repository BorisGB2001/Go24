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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/theme"
)

// TestAppearanceHTTP: «Тема оформления» — the employee's mode and background on every page, «Сбросить»; an
// administrator's «Применить для всех» for those with none of their own, «Сбросить для всех»; a wrong background;
// REST GET/PUT/DELETE /api/v1/theme.
func TestAppearanceHTTP(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, Themes: theme.NewStore(pool), Keys: keys,
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}
	look := func(who string) string {
		t.Helper()
		page, _ := do(who, "GET", "/settings", nil, 200)
		head, _, _ := strings.Cut(page, " data-failed")
		_, attrs, ok := strings.Cut(head, `<html lang="`)
		if !ok {
			t.Fatal("no <html> on the page")
		}
		_, attrs, _ = strings.Cut(attrs, `"`)
		return strings.TrimSpace(attrs)
	}

	if got := look(anna); got != "" {
		t.Fatalf("go24's look: %q", got)
	}
	page, _ := do(anna, "GET", "/settings", nil, 200)
	if !strings.Contains(page, "data-theme-dlg") || !strings.Contains(page, `data-bg="aurora"`) || strings.Contains(page, `name="all"`) {
		t.Fatal("the dialog: no backgrounds, or «Применить для всех» for an employee")
	}
	if _, loc := do(anna, "POST", "/appearance", url.Values{"mode": {"dark"}, "bg": {"ocean"}, "back": {"/settings"}}, 303); loc != "/settings" {
		t.Fatalf("back to %q", loc)
	}
	if got := look(anna); got != `data-theme="dark" data-bg="ocean"` {
		t.Fatalf("anna's look: %q", got)
	}
	if _, loc := do(anna, "POST", "/appearance", url.Values{"bg": {"stars"}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatalf("a wrong background: %q", loc)
	}
	do(anna, "POST", "/appearance", url.Values{"mode": {"light"}, "all": {"1"}}, 403)

	// the company's theme: for those with none of their own
	page, _ = do(boss, "GET", "/settings", nil, 200)
	if !strings.Contains(page, `name="all"`) {
		t.Fatal("no «Применить для всех» for an administrator")
	}
	do(boss, "POST", "/appearance", url.Values{"mode": {"light"}, "bg": {"sand"}, "all": {"1"}}, 303)
	if got := look(boss); got != `data-theme="light" data-bg="sand"` {
		t.Fatalf("the company's theme for the admin: %q", got)
	}
	if got := look(anna); got != `data-theme="dark" data-bg="ocean"` {
		t.Fatalf("anna's own theme went: %q", got)
	}
	do(anna, "POST", "/appearance/reset", url.Values{}, 303)
	if got := look(anna); got != `data-theme="light" data-bg="sand"` {
		t.Fatalf("anna after «Сбросить»: %q", got)
	}
	do(boss, "POST", "/appearance/reset?all=1", url.Values{}, 303)
	if got := look(anna); got != "" {
		t.Fatalf("after «Сбросить для всех»: %q", got)
	}

	k, err := keys.Create(ctx, anna, "k", []string{"settings:read", "settings:write"}, time.Hour)
	must(err)
	call := func(method, path, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	if got := call("GET", "/api/v1/theme", "", 200); got["own"] != false || got["bg"] != "" {
		t.Fatalf("REST GET: %v", got)
	}
	if got := call("PUT", "/api/v1/theme", `{"mode":"dark","bg":"mint"}`, 200); got["mode"] != "dark" || got["bg"] != "mint" || got["own"] != true {
		t.Fatalf("REST PUT: %v", got)
	}
	call("PUT", "/api/v1/theme", `{"bg":"custom"}`, 422) // no picture of one's own yet
	call("PUT", "/api/v1/theme?all=1", `{"bg":"mint"}`, 403)
	if got := look(anna); got != `data-theme="dark" data-bg="mint"` {
		t.Fatalf("after REST: %q", got)
	}
	call("DELETE", "/api/v1/theme", "", 204)
	if got := look(anna); got != "" {
		t.Fatalf("after REST DELETE: %q", got)
	}
}
