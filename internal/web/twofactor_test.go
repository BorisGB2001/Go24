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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/twofactor"
)

// TestTwoFactor: «Двухэтапная авторизация» (GO24DEV-757) — an administrator requires the second
// factor; one never signed in with it may not shut themselves out at once; the gate passes, reminds or refuses; the
// page lists the employees; REST /api/v1/settings/two-factor for administrators.
func TestTwoFactor(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	admin, plain := newUser("Админов"), newUser("Простов")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Audit: audit.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: admin, Name: "Админов", Admin: true}))
	cookie := rec.Result().Cookies()[0]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}

	// required at once of administrators: the administrator never signed in with the factor would be shut out
	if _, loc, _ := do("POST", "/settings/two-factor", url.Values{"mode": {"admins"}, "days": {"0"}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("self lock-out allowed: %s", loc)
	}
	if p, _ := srv.twoFA().Policy(ctx); p.Mode != "" {
		t.Fatalf("policy = %+v", p)
	}
	if _, loc, _ := do("POST", "/settings/two-factor", url.Values{"mode": {"all"}, "days": {"7"}}); !strings.HasPrefix(loc, "/settings/two-factor?msg=") {
		t.Fatalf("save: %s", loc)
	}
	p, err := srv.twoFA().Policy(ctx)
	must(err)
	if p.Mode != "all" || p.Days != 7 || time.Since(p.Since) > time.Minute {
		t.Fatalf("policy = %+v", p)
	}
	// the gate: the factor used is noted; without it — a reminder while the period runs; a guest passes
	if v, _, err := srv.twoFactorGate(ctx, admin, true, false, []string{"pwd", "mfa"}); err != nil || v != twofactor.Pass {
		t.Fatalf("with the factor: %v %v", v, err)
	}
	if v, due, err := srv.twoFactorGate(ctx, plain, false, false, []string{"pwd"}); err != nil || v != twofactor.Remind || due.Before(time.Now().AddDate(0, 0, 6)) {
		t.Fatalf("without the factor: %v %v %v", v, due, err)
	}
	if v, _, _ := srv.twoFactorGate(ctx, plain, false, true, nil); v != twofactor.Pass {
		t.Fatal("a guest is asked for the factor")
	}
	_, err = pool.Exec(ctx, `UPDATE users SET created_at = now() - interval '30 days' WHERE id = $1`, plain)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET value = jsonb_set(value, '{since}', to_jsonb(to_char((now() - interval '30 days') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))) WHERE key = 'two_factor'`)
	must(err)
	if v, _, _ := srv.twoFactorGate(ctx, plain, false, false, nil); v != twofactor.Deny {
		t.Fatalf("the period is over: %v", v)
	}
	// the page: who signed in with the factor, who must
	if st, _, body := do("GET", "/settings/two-factor", nil); st != http.StatusOK || !strings.Contains(body, "Простов") || !strings.Contains(body, "Админов") {
		t.Fatalf("page: %d", st)
	}
	if st, loc, _ := do("GET", "/security/two-factor?next=//evil", nil); st != http.StatusOK && loc == "//evil" {
		t.Fatal("next goes outside")
	}

	// REST: administrators only; now the administrator used the factor and may require it at once
	adminKey, err := keys.Create(ctx, admin, "k", []string{"settings:read", "settings:write"}, time.Hour)
	must(err)
	plainKey, err := keys.Create(ctx, plain, "k", []string{"settings:read", "settings:write"}, time.Hour)
	must(err)
	api := func(k, method, in string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/settings/two-factor", strings.NewReader(in))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, _ := api(plainKey, "GET", ""); st != http.StatusForbidden {
		t.Fatalf("an employee reads the policy: %d", st)
	}
	if st, b := api(adminKey, "PUT", `{"mode":"admins","days":0}`); st != http.StatusOK || !strings.Contains(b, `"mode": "admins"`) ||
		!strings.Contains(b, `"required": false`) {
		t.Fatalf("PUT: %d %s", st, b)
	}
	if st, _ := api(adminKey, "PUT", `{"mode":"nobody"}`); st != http.StatusUnprocessableEntity {
		t.Fatalf("a bad mode: %d", st)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE kind = 'twofa_saved'`).Scan(&n))
	if n != 2 {
		t.Fatalf("audit twofa_saved = %d", n)
	}
}
