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
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/portal"
	"go24/internal/tasks"
)

// TestAuditHTTP: «Журнал событий» — the admin mode, sign-out, API keys (made, revoked, wrong ones once a minute),
// webhooks, the general settings and dismissals are written with the address and the browser; only administrators
// read it (the page with filters, CSV, REST); the period of keeping is chosen and old events go.
func TestAuditHTTP(t *testing.T) {
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
	var boss, anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Portal: portal.NewStore(pool, bundle),
		Hooks: hooks.NewStore(pool), Company: company.NewStore(pool), Audit: audit.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	const agent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	send := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else if who != "" {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		req.Header.Set("User-Agent", agent)
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
		return string(raw)
	}
	kinds := func() []string {
		t.Helper()
		list, err := srv.Audit.List(ctx, audit.Filter{})
		must(err)
		var out []string
		for i := len(list) - 1; i >= 0; i-- {
			out = append(out, list[i].Kind)
		}
		return out
	}

	send(boss, "POST", "/admin/mode", url.Values{}, 303)
	rec := httptest.NewRecorder() // the browser keeps the new cookie: the admin mode is on
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true, AdminMode: true}))
	as[boss] = rec.Result().Cookies()[0]
	send(boss, "POST", "/admin/mode", url.Values{}, 303)
	as[boss] = cookie(boss, true)
	send(anna, "POST", "/settings/keys", url.Values{"name": {"n8n"}, "scopes": {"settings:read"}}, 200)
	var keyID string
	must(pool.QueryRow(ctx, `SELECT id FROM api_keys WHERE user_id = $1`, anna).Scan(&keyID))
	send(anna, "POST", "/settings/keys/"+keyID+"/revoke", url.Values{}, 303)
	send("go24_bad_wrong", "GET", "/api/v1/settings/portal", nil, 401)
	send("go24_bad_wrong", "GET", "/api/v1/settings/portal", nil, 401) // the same minute: not written again
	send(boss, "POST", "/settings/webhooks/new", url.Values{"op": {"save"}, "name": {"CRM в n8n"}, "url": {"https://n8n.example.com/w"}, "events": {"deal.created"}, "active": {"on"}}, 303)
	var hookID string
	must(pool.QueryRow(ctx, `SELECT id FROM webhooks`).Scan(&hookID))
	send(boss, "POST", "/settings/webhooks/"+hookID, url.Values{"op": {"delete"}}, 303)
	send(boss, "POST", "/u/"+ivan+"/active", url.Values{"active": {"0"}}, 303)
	send(boss, "POST", "/logout", url.Values{}, 302)
	want := []string{"admin_on", "admin_off", "key_created", "key_revoked", "api_bad_key", "hook_saved", "hook_deleted", "user_fired", "logout"}
	if got := kinds(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("events = %v, want %v", got, want)
	}
	list, err := srv.Audit.List(ctx, audit.Filter{Kind: "user_fired"})
	must(err)
	if len(list) != 1 || list[0].UserID != boss || list[0].Name != "Борис" || list[0].Target != "Иван" || list[0].TargetID != ivan ||
		list[0].IP != "127.0.0.1" || list[0].Agent != agent {
		t.Errorf("dismissal = %+v", list)
	}
	if list, _ := srv.Audit.List(ctx, audit.Filter{UserID: ivan}); len(list) != 1 { // whom it was about counts too
		t.Errorf("Ivan's events = %+v", list)
	}
	if list, _ := srv.Audit.List(ctx, audit.Filter{Kind: "key_revoked"}); len(list) != 1 || list[0].Target != "n8n" || list[0].UserID != anna {
		t.Errorf("revoked key = %+v", list)
	}

	// the page: administrators only, filters, the next page, CSV
	send(anna, "GET", "/settings/events", nil, 403)
	if page := send(anna, "GET", "/settings", nil, 200); strings.Contains(page, `href="/settings/events"`) {
		t.Error("an employee is offered the log")
	}
	page := send(boss, "GET", "/settings/events", nil, 200)
	for _, w := range []string{`class="pill audit-user_fired">Сотрудник уволен`, `href="/u/` + ivan + `"`, "Неверный ключ API", "GET /api/v1/settings/portal", "Chrome 130 · Windows", "127.0.0.1", `value="180" selected`} {
		if !strings.Contains(page, w) {
			t.Errorf("the page has no %s", w)
		}
	}
	page = send(boss, "GET", "/settings/events?kind=hook_saved", nil, 200)
	if !strings.Contains(page, "CRM в n8n · https://n8n.example.com/w") || strings.Contains(page, "pill audit-user_fired") {
		t.Error("the filter by kind")
	}
	tomorrow := time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	if page := send(boss, "GET", "/settings/events?from="+tomorrow, nil, 200); !strings.Contains(page, "Событий нет") {
		t.Error("the filter by period")
	}
	csv := send(boss, "GET", "/settings/events?format=csv&kind=user_fired", nil, 200)
	if !strings.Contains(csv, "Сотрудник уволен;Борис;Иван;127.0.0.1;") || strings.Count(csv, "\n") != 2 {
		t.Errorf("csv = %q", csv)
	}
	send(boss, "GET", "/settings/events?kind=nonsense", nil, 303)

	// REST: an administrator's key with settings:read
	tokBoss, err := srv.Keys.Create(ctx, boss, "audit", []string{"settings:read"}, 0)
	must(err)
	tokAnna, err := srv.Keys.Create(ctx, anna, "audit", []string{"settings:read"}, 0)
	must(err)
	send(tokAnna, "GET", "/api/v1/settings/events", nil, 403)
	var res struct{ Events []audit.Event }
	must(json.Unmarshal([]byte(send(tokBoss, "GET", "/api/v1/settings/events?kind=user_fired&limit=5", nil, 200)), &res))
	if len(res.Events) != 1 || res.Events[0].TargetID != ivan {
		t.Errorf("api = %+v", res)
	}

	// the period of keeping
	send(anna, "POST", "/settings/events", url.Values{"days": {"30"}}, 403)
	send(boss, "POST", "/settings/events", url.Values{"days": {"7"}}, 303)
	send(boss, "POST", "/settings/events", url.Values{"days": {"30"}}, 303)
	if days, _ := srv.Audit.Days(ctx); days != 30 {
		t.Errorf("days = %d", days)
	}
	_, err = pool.Exec(ctx, `UPDATE audit_log SET at = now() - interval '31 days' WHERE kind = 'logout'`)
	must(err)
	n, err := srv.Audit.Prune(ctx, time.Now())
	must(err)
	if n != 1 {
		t.Errorf("pruned %d", n)
	}
}
