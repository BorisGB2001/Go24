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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestAdminsHTTP: «Администраторы» — an administrator makes an employee one (not a guest), it works at once without
// signing in again (the pages, the admin mode, REST) and stops at once when taken away; the Authentik group's ones
// and oneself are not taken away here; both are written in «Журнал событий».
func TestAdminsHTTP(t *testing.T) {
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
	var boss, anna, gost string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('gost', 'Гость', 'gost@go24.test', true) RETURNING id`).Scan(&gost))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	send := func(who, method, path, ctype, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
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
	const form = "application/x-www-form-urlencoded"
	post := func(who, path string, v url.Values, want int) string {
		t.Helper()
		return send(who, "POST", path, form, v.Encode(), want)
	}
	msg := func() string {
		t.Helper()
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg")
	}

	send(anna, "GET", "/settings/admins", "", "", 403)
	post(anna, "/settings/admins", url.Values{"user": {anna}}, 403)
	page := send(boss, "GET", "/settings/admins", "", "", 200)
	for _, w := range []string{"Борис", "Группа Authentik", "(это вы)", "go24-admins", `name="user"`} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}
	if strings.Contains(page, `/settings/admins/`+boss+`/remove`) {
		t.Error("the group's administrator can be removed on the page")
	}
	if page := send(boss, "GET", "/settings", "", "", 200); !strings.Contains(page, `href="/settings/admins"`) {
		t.Error("no «Администраторы» in «Настройки»")
	}

	post(boss, "/settings/admins", url.Values{"user": {gost}}, 303)
	if msg() != "Гость не может быть администратором" {
		t.Errorf("guest: %q", msg())
	}
	post(boss, "/settings/admins", url.Values{"user": {"00000000-0000-0000-0000-000000000000"}}, 404)
	post(boss, "/settings/admins", url.Values{"user": {anna}}, 303)
	if msg() != "Администратор назначен" {
		t.Errorf("added: %q", msg())
	}
	// Anna, with the cookie made before, is an administrator at once: the settings and the admin mode
	send(anna, "GET", "/settings/admins", "", "", 200)
	post(anna, "/admin/mode", url.Values{}, 303)
	c := last.Cookies()
	if len(c) != 1 {
		t.Fatalf("admin mode cookies = %v", c)
	}
	sess, err := srv.Sessions.Get(&http.Request{Header: http.Header{"Cookie": {c[0].String()}}})
	must(err)
	if !sess.AdminMode || sess.Admin { // the admin of go24 is never kept in the cookie
		t.Errorf("admin mode session = %+v", sess)
	}
	as[anna] = c[0]
	if page := send(anna, "GET", "/settings/admins", "", "", 200); !strings.Contains(page, "В go24: Борис") ||
		strings.Contains(page, `/settings/admins/`+anna+`/remove`) {
		t.Errorf("Anna's page:\n%s", page)
	}
	post(anna, "/settings/admins/"+anna+"/remove", url.Values{}, 303)
	if !strings.HasPrefix(msg(), "Снять права администратора с себя нельзя") {
		t.Errorf("self: %q", msg())
	}
	post(anna, "/settings/admins/"+boss+"/remove", url.Values{}, 303)
	if msg() != "Администратор из группы Authentik снимается только в Authentik" {
		t.Errorf("group: %q", msg())
	}

	// REST: the list, taking away, making again
	tokBoss, err := srv.Keys.Create(ctx, boss, "admins", []string{"settings:read", "settings:write"}, 0)
	must(err)
	tokAnna, err := srv.Keys.Create(ctx, anna, "admins", []string{"settings:read", "settings:write"}, 0)
	must(err)
	var res struct{ Admins []Admin }
	must(json.Unmarshal([]byte(send(tokAnna, "GET", "/api/v1/settings/admins", "", "", 200)), &res))
	if len(res.Admins) != 2 || res.Admins[0].Name != "Анна" || !res.Admins[0].Granted || res.Admins[0].Group || res.Admins[0].By != "Борис" ||
		res.Admins[0].At == nil || !res.Admins[1].Group || res.Admins[1].Granted {
		t.Errorf("admins = %+v", res.Admins)
	}
	send(tokBoss, "DELETE", "/api/v1/settings/admins/"+anna, "", "", 204)
	// taken away: at once, the admin mode in her cookie too
	send(anna, "GET", "/settings/admins", "", "", 403)
	send(tokAnna, "GET", "/api/v1/settings/admins", "", "", 403)
	send(tokAnna, "POST", "/api/v1/settings/admins", "application/json", `{"user_id":"`+anna+`"}`, 403)
	send(tokBoss, "POST", "/api/v1/settings/admins", "application/json", `{"user_id":"`+gost+`"}`, 422)
	must(json.Unmarshal([]byte(send(tokBoss, "POST", "/api/v1/settings/admins", "application/json", `{"user_id":"`+anna+`"}`, 200)), &res))
	if len(res.Admins) != 2 {
		t.Errorf("admins after POST = %+v", res.Admins)
	}

	list, err := srv.Audit.List(ctx, audit.Filter{})
	must(err)
	var kinds []string
	for i := len(list) - 1; i >= 0; i-- {
		if k := list[i].Kind; k == "admin_granted" || k == "admin_revoked" {
			kinds = append(kinds, k)
			if list[i].Target != "Анна" || list[i].TargetID != anna || list[i].UserID != boss {
				t.Errorf("event = %+v", list[i])
			}
		}
	}
	if strings.Join(kinds, " ") != "admin_granted admin_revoked admin_granted" {
		t.Errorf("events = %v", kinds)
	}
}
