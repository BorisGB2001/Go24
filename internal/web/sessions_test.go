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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestSessionsHTTP: the profile's «Безопасность» — the employee sees their sessions (browser, address, this one) and
// ends one or all but this one («Выйти на всех устройствах», cookies of before the sessions were kept too); an
// administrator ends anyone's; another employee can do neither; an ended session is signed out at the next request,
// signing out ends it; all is in «Журнал событий».
func TestSessionsHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Company: company.NewStore(pool),
		Audit: audit.NewStore(pool), AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		OIDC: auth.NewOIDC("http://auth.test:9000/application/o/go24/", "go24", "x", ""), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	const chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	const firefox = "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0"
	// signIn is what the sign-in does: a kept session and its cookie
	signIn := func(id, agent string, admin bool) (*http.Cookie, string) {
		t.Helper()
		sid, err := srv.devices().Start(ctx, id, "10.0.0.1", agent, time.Now().Add(srv.Sessions.TTL()))
		must(err)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin, SID: sid}))
		return rec.Result().Cookies()[0], sid
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	send := func(c *http.Cookie, method, path string, want int) string {
		t.Helper()
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader(url.Values{}.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(c)
		req.Header.Set("Accept-Language", "ru")
		req.Header.Set("User-Agent", chrome)
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		last = res
		return string(raw)
	}
	alive := func(c *http.Cookie, want bool) {
		t.Helper()
		code := 302
		if want {
			code = 200
		}
		send(c, "GET", "/u/"+anna, code)
		if !want && last.Header.Get("Location") != "/login" {
			t.Fatalf("an ended session goes to %q", last.Header.Get("Location"))
		}
	}

	a1, sid1 := signIn(anna, chrome, false)
	a2, sid2 := signIn(anna, firefox, false)
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna})) // a cookie of before the sessions were kept
	legacy := rec.Result().Cookies()[0]
	iv, _ := signIn(ivan, chrome, false)
	bs, _ := signIn(boss, chrome, true)

	page := send(a1, "GET", "/u/"+anna+"?tab=security", 200)
	for _, w := range []string{"Активные сеансы", "Chrome 130 · Windows", "Firefox 131 · Linux", "этот браузер", "Выйти на всех устройствах",
		"/u/" + anna + "/sessions/" + sid2 + "/end", `href="http://auth.test:9000/if/user/#/settings"`} {
		if !strings.Contains(page, w) {
			t.Errorf("«Безопасность» has no %q", w)
		}
	}
	if !strings.Contains(send(a1, "GET", "/u/"+anna, 200), `href="/u/`+anna+`?tab=security"`) {
		t.Error("no «Безопасность» tab in one's own profile")
	}
	// another employee: no tab, no ending
	if page := send(iv, "GET", "/u/"+anna+"?tab=security", 200); strings.Contains(page, "Активные сеансы") || strings.Contains(page, `href="/u/`+anna+`?tab=security"`) {
		t.Error("another employee sees the sessions")
	}
	send(iv, "POST", "/u/"+anna+"/sessions/"+sid2+"/end", 403)
	send(iv, "POST", "/u/"+anna+"/sessions/end", 403)

	// the session's last use is noted
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE user_sessions SET seen_at = now() - interval '1 hour', ip = '' WHERE id = $1`, sid1))
	alive(a1, true)
	var ip string
	var fresh bool
	must(pool.QueryRow(ctx, `SELECT ip, seen_at > now() - interval '1 minute' FROM user_sessions WHERE id = $1`, sid1).Scan(&ip, &fresh))
	if ip == "" || !fresh {
		t.Errorf("the session's use is not noted: ip %q, fresh %v", ip, fresh)
	}

	// one session ended: that browser is out at once, the rest stay
	send(a1, "POST", "/u/"+anna+"/sessions/"+sid2+"/end", 303)
	if u, _ := url.Parse(last.Header.Get("Location")); u.Query().Get("msg") != "Сеанс завершён" {
		t.Errorf("message %q", u.Query().Get("msg"))
	}
	alive(a2, false)
	alive(a1, true)
	alive(legacy, true)
	send(a1, "POST", "/u/"+anna+"/sessions/"+sid2+"/end", 404) // already ended

	// «Выйти на всех устройствах»: all but this one, the old cookie too
	a3, sid3 := signIn(anna, firefox, false)
	send(a3, "POST", "/u/"+anna+"/sessions/end", 303)
	alive(a1, false)
	alive(legacy, false)
	alive(a3, true)
	list, err := srv.devices().List(ctx, anna)
	must(err)
	if len(list) != 1 || list[0].ID != sid3 {
		t.Fatalf("live sessions after «Выйти на всех устройствах»: %+v", list)
	}

	// an administrator ends all of anyone's; ending this very session is signing out
	if page := send(bs, "GET", "/u/"+anna+"?tab=security", 200); !strings.Contains(page, "Завершить все сеансы сотрудника Анна?") ||
		strings.Contains(page, "/if/user/") {
		t.Error("the administrator's «Безопасность» of an employee")
	}
	send(bs, "POST", "/u/"+anna+"/sessions/end", 303)
	alive(a3, false)
	a4, sid4 := signIn(anna, chrome, false)
	send(a4, "POST", "/u/"+anna+"/sessions/"+sid4+"/end", 303)
	if last.Header.Get("Location") != "/login" {
		t.Errorf("ending one's own session goes to %q", last.Header.Get("Location"))
	}
	alive(a4, false)

	// signing out ends the session
	a5, sid5 := signIn(anna, chrome, false)
	send(a5, "POST", "/logout", 302)
	var ended bool
	must(pool.QueryRow(ctx, `SELECT ended_at IS NOT NULL FROM user_sessions WHERE id = $1`, sid5).Scan(&ended))
	if !ended {
		t.Error("signing out does not end the session")
	}
	alive(a5, false)

	var one, all int
	must(pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE kind = 'session_ended'),
		count(*) FILTER (WHERE kind = 'sessions_ended' AND user_id::text = $1 AND target_id::text = $2) FROM audit_log`, boss, anna).Scan(&one, &all))
	if one != 2 || all != 1 {
		t.Errorf("«Журнал событий»: %d sessions ended, %d «Выйти на всех устройствах» by the administrator; want 2, 1", one, all)
	}
}
