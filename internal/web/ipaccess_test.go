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
	"go24/internal/ipaccess"
	"go24/internal/tasks"
)

// TestIPAccessHTTP: «Ограничение доступа по IP» — an administrator makes rules for employees and departments (with
// sub-departments); a named employee opens go24 (and REST with their key) only from the addresses of their rules,
// others from anywhere; a refusal is written once in «Журнал событий»; rules shutting out the administrator saving
// them and wrong addresses are not kept.
func TestIPAccessHTTP(t *testing.T) {
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
	var boss, anna, ivan, sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ((SELECT id FROM departments WHERE parent_id IS NULL LIMIT 1), 'Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Восток') RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ivan, east)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		IPAccess: ipaccess.NewStore(pool), AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna}),
		ivan: cookie(auth.Session{UserID: ivan})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	// the test server is reached over loopback: the browser's address is the last of X-Forwarded-For (see ClientIP)
	send := func(who, ip, method, path, ctype, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("X-Forwarded-For", ip)
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s from %s: %d, want %d: %.300s", method, path, ip, res.StatusCode, want, raw)
		}
		last = res
		return string(raw)
	}
	post := func(who, ip, path string, v url.Values) string {
		t.Helper()
		send(who, ip, "POST", path, "application/x-www-form-urlencoded", v.Encode(), 303)
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg")
	}
	rules := func() []ipaccess.Rule {
		t.Helper()
		rs, err := srv.IPAccess.Rules(ctx)
		must(err)
		return rs
	}

	send(anna, "1.2.3.4", "GET", "/my", "", "", 200) // no rules: from anywhere
	send(anna, "1.2.3.4", "GET", "/settings/ip", "", "", 403)
	page := send(boss, "1.2.3.4", "GET", "/settings/ip", "", "", 200)
	for _, w := range []string{"Ограничение доступа по IP", "Ваш адрес сейчас: 1.2.3.4", "Правил нет", `name="nets"`, "Все сотрудники"} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}
	if page := send(boss, "1.2.3.4", "GET", "/settings", "", "", 200); !strings.Contains(page, `href="/settings/ip"`) {
		t.Error("no «Ограничение доступа по IP» in «Настройки»")
	}

	// wrong ones are not kept
	if m := post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {anna}, "nets": {"10.0.0.0/8\n300.1.1.1"}}); !strings.Contains(m, "«300.1.1.1»") {
		t.Errorf("bad address: %q", m)
	}
	if m := post(boss, "1.2.3.4", "/settings/ip", url.Values{"nets": {"10.0.0.0/8"}}); !strings.HasPrefix(m, "Укажите, для кого") {
		t.Errorf("nobody: %q", m)
	}
	if m := post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {anna}, "nets": {" "}}); m != "Укажите хотя бы один разрешённый адрес" {
		t.Errorf("no addresses: %q", m)
	}
	if len(rules()) != 0 {
		t.Fatalf("rules = %+v", rules())
	}

	// Anna — from the office network only; the department «Продажи» (Ivan is in its sub-department) — from 8.8.8.8
	if m := post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {anna}, "title": {"Офис"}, "nets": {"10.0.0.0/8, 192.168.1.1-192.168.1.50"}}); m != "Сохранено" {
		t.Errorf("saved: %q", m)
	}
	post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {"dept:" + sales}, "nets": {"8.8.8.8"}})
	rs := rules()
	if len(rs) != 2 || rs[0].Title != "Офис" || strings.Join(rs[0].Nets, " ") != "10.0.0.0/8 192.168.1.1-192.168.1.50" ||
		strings.Join(rs[0].Users, " ") != anna || len(rs[1].Depts) != 1 || rs[1].ID == "" || rs[0].ID == rs[1].ID {
		t.Fatalf("rules = %+v", rs)
	}
	denied := send(anna, "1.2.3.4", "GET", "/my", "", "", 403)
	if !strings.Contains(denied, "Доступ к go24 с адреса 1.2.3.4 запрещён") {
		t.Errorf("denied page: %q", denied)
	}
	send(anna, "1.2.3.4", "GET", "/tasks", "", "", 403)
	send(anna, "10.20.30.40", "GET", "/my", "", "", 200)
	send(anna, "192.168.1.50", "GET", "/my", "", "", 200)
	send(anna, "192.168.1.51", "GET", "/my", "", "", 403)
	send(ivan, "8.8.8.8", "GET", "/my", "", "", 200)
	send(ivan, "10.0.0.1", "GET", "/my", "", "", 403)
	send(boss, "1.2.3.4", "GET", "/my", "", "", 200) // no rule names him

	// Ivan is named by two rules: the addresses of either
	page = send(boss, "1.2.3.4", "GET", "/settings/ip", "", "", 200)
	if !strings.Contains(page, `value="dept:`+sales+`"`) || !strings.Contains(page, "Продажи") || !strings.Contains(page, "10.0.0.0/8\n192.168.1.1-192.168.1.50") {
		t.Errorf("rules on the page:\n%s", page)
	}
	post(boss, "1.2.3.4", "/settings/ip", url.Values{"id": {rs[0].ID}, "title": {"Офис"}, "who": {anna, ivan}, "nets": {"10.0.0.0/8"}})
	send(ivan, "10.0.0.1", "GET", "/my", "", "", 200)
	send(ivan, "8.8.8.8", "GET", "/my", "", "", 200)
	if rs := rules(); len(rs) != 2 || len(rs[0].Users) != 2 || len(rs[0].Nets) != 1 {
		t.Errorf("changed rules = %+v", rs)
	}

	// «Все сотрудники» from 10.0.0.0/8 would shut Boris out at 1.2.3.4: not kept; from the office — kept
	if m := post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {"all"}, "nets": {"10.0.0.0/8"}}); m != "Правила закроют вам доступ с вашего текущего адреса 1.2.3.4 — добавьте его в разрешённые" {
		t.Errorf("self: %q", m)
	}
	send(boss, "1.2.3.4", "GET", "/my", "", "", 200)
	post(boss, "10.0.0.7", "/settings/ip", url.Values{"who": {"all"}, "nets": {"10.0.0.0/8"}})
	send(boss, "1.2.3.4", "GET", "/my", "", "", 403)
	send(boss, "10.0.0.7", "GET", "/my", "", "", 200)
	send(ivan, "8.8.8.8", "GET", "/my", "", "", 200) // his department's rule still lets him

	// REST: the employee's key is held by the rules too; the rules by an administrator's key
	tokBoss, err := srv.Keys.Create(ctx, boss, "ip", []string{"settings:read", "settings:write", "tasks:read"}, 0)
	must(err)
	tokAnna, err := srv.Keys.Create(ctx, anna, "ip", []string{"tasks:read", "settings:read"}, 0)
	must(err)
	if res := send(tokAnna, "1.2.3.4", "GET", "/api/v1/tasks", "", "", 403); !strings.Contains(res, "запрещён администратором") {
		t.Errorf("api denied: %s", res)
	}
	send(tokAnna, "10.1.1.1", "GET", "/api/v1/tasks", "", "", 200)
	send(tokAnna, "10.1.1.1", "GET", "/api/v1/settings/ip-access", "", "", 403)
	var got struct {
		Rules []ipaccess.Rule
		IP    string
	}
	must(json.Unmarshal([]byte(send(tokBoss, "10.0.0.7", "GET", "/api/v1/settings/ip-access", "", "", 200)), &got))
	if len(got.Rules) != 3 || got.IP != "10.0.0.7" || !got.Rules[2].All {
		t.Errorf("api rules = %+v", got)
	}
	send(tokBoss, "1.2.3.4", "GET", "/api/v1/settings/ip-access", "", "", 403)
	send(tokBoss, "10.0.0.7", "PUT", "/api/v1/settings/ip-access", "application/json", `{"rules":[{"all":true,"nets":["9.9.9.9"]}]}`, 422)
	must(json.Unmarshal([]byte(send(tokBoss, "10.0.0.7", "PUT", "/api/v1/settings/ip-access", "application/json", `{"rules":[]}`, 200)), &got))
	if len(got.Rules) != 0 {
		t.Errorf("rules after PUT [] = %+v", got.Rules)
	}
	send(anna, "1.2.3.4", "GET", "/my", "", "", 200)

	// «Удалить»
	post(boss, "1.2.3.4", "/settings/ip", url.Values{"who": {anna}, "nets": {"10.0.0.0/8"}})
	if m := post(boss, "1.2.3.4", "/settings/ip/"+rules()[0].ID+"/delete", url.Values{}); m != "Правило удалено" || len(rules()) != 0 {
		t.Errorf("deleted: %q, %+v", m, rules())
	}

	// the log: Anna's many refused requests from 1.2.3.4 — one line; the changes of the rules
	list, err := srv.Audit.List(ctx, audit.Filter{})
	must(err)
	count := map[string]int{}
	for _, e := range list {
		if e.Kind == "ip_denied" && e.UserID == anna {
			count[e.IP]++
		}
		count[e.Kind]++
	}
	if count["1.2.3.4"] != 1 || count["192.168.1.51"] != 1 || count["ip_rules_saved"] != 7 { // the pages and the key at 1.2.3.4 — within a minute
		t.Errorf("events = %v", count)
	}
}
