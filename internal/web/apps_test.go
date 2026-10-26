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
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestAppsHTTP: «Приложения» — administrators register an application and install it (ONAPPINSTALL goes signed to
// the install handler with the installer's tokens; a handler answering 500 leaves it not installed); the page opens in
// a frame with the employee's tokens; /oauth/authorize gives a code only for the handler's host, /oauth/token
// exchanges it once and refreshes; the access token works in REST with the application's sections only; a new key,
// uninstalling and deleting cut the tokens off.
func TestAppsHTTP(t *testing.T) {
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

	// the application's server: remembers the install event, answers 500 while broken
	var (
		mu      sync.Mutex
		events  []map[string]any
		sigOK   bool
		broken  = true
		secretF func() string
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		var ev map[string]any
		_ = json.Unmarshal(raw, &ev)
		events = append(events, ev)
		sigOK = r.Header.Get("X-Go24-Signature") == hooks.Sign(secretF(), raw) && r.Header.Get("X-Go24-Event") == "ONAPPINSTALL"
		if broken {
			w.WriteHeader(500)
		}
	}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	send := func(who, method, path, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else if who != "" {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
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
	post := func(who, path string, v url.Values) string {
		t.Helper()
		send(who, "POST", path, v.Encode(), 303)
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg")
	}

	send(anna, "GET", "/settings/apps", "", 403)
	send(anna, "POST", "/settings/apps/new", url.Values{"name": {"x"}}.Encode(), 403)
	if m := post(boss, "/settings/apps/new", url.Values{"name": {"Доставка"}, "scopes": {"tasks:read"}}); m != "Укажите адрес http(s) обработчика приложения" {
		t.Fatalf("no handler: %q", m)
	}
	if m := post(boss, "/settings/apps/new", url.Values{"name": {"Доставка"}, "handler_url": {app.URL + "/page"}, "install_url": {app.URL + "/install"},
		"menu_title": {"Расчёт доставки"}, "scopes": {"tasks:read", "bogus"}}); m != "" {
		t.Fatalf("save: %q", m)
	}
	var a apps.App
	{
		list, err := srv.Apps.List(ctx)
		must(err)
		if len(list) != 1 || !strings.HasPrefix(list[0].ClientID, "local.") || len(list[0].ClientSecret) != 64 || strings.Join(list[0].Scopes, ",") != "tasks:read" {
			t.Fatalf("saved: %+v", list)
		}
		a = list[0]
	}
	secretF = func() string { a, _ := srv.Apps.Get(ctx, a.ID); return a.ClientSecret }
	page := send(boss, "GET", "/settings/apps", "", 200)
	for _, want := range []string{a.ClientID, a.ClientSecret, "Установить", "/oauth/authorize"} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings page lacks %q", want)
		}
	}
	if page := send(boss, "GET", "/settings", "", 200); !strings.Contains(page, `href="/settings/apps"`) {
		t.Fatal("«Настройки» lack «Приложения»")
	}

	// not installed: neither the page nor OAuth
	send(anna, "GET", "/apps/"+a.ID, "", 404)
	send("", "GET", "/oauth/token?grant_type=refresh_token&client_id="+a.ClientID+"&client_secret="+a.ClientSecret, "", 401)

	// the install handler fails: not installed, the error is shown
	if m := post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"install"}}); !strings.Contains(m, "HTTP 500") {
		t.Fatalf("broken install: %q", m)
	}
	if got, _ := srv.Apps.Get(ctx, a.ID); got.Installed() || got.InstallError != "HTTP 500" {
		t.Fatalf("broken install marked: %+v", got)
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	if m := post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"install"}}); m != "Приложение установлено" {
		t.Fatalf("install: %q", m)
	}
	mu.Lock()
	ev := events[len(events)-1]
	ok := sigOK
	mu.Unlock()
	authBlock, _ := ev["auth"].(map[string]any)
	if ev["event"] != "ONAPPINSTALL" || !ok || authBlock["user_id"] != boss || authBlock["domain"] != "go24.example" ||
		authBlock["client_endpoint"] != "https://go24.example/api/v1/" || authBlock["application_token"] != a.ApplicationToken {
		t.Fatalf("install event: %v (signature ok %v)", ev, ok)
	}
	installToken, _ := authBlock["access_token"].(string)
	if me := send(installToken, "GET", "/api/v1/me", "", 200); !strings.Contains(me, boss) {
		t.Fatalf("install token in REST: %s", me)
	}

	// the page in a frame: a form with Анна's tokens to the handler
	page = send(anna, "GET", "/apps/"+a.ID, "", 200)
	if last.Header.Get("Cache-Control") != "no-store" || !strings.Contains(page, `target="go24app"`) || !strings.Contains(page, `action="`+app.URL+`/page?APP_SID=`) ||
		!strings.Contains(page, "DOMAIN=go24.example") || !strings.Contains(page, `name="PLACEMENT" value="DEFAULT"`) {
		t.Fatalf("app page: %.2000s", page)
	}
	i := strings.Index(page, `name="AUTH_ID" value="`)
	annaToken := page[i+len(`name="AUTH_ID" value="`):]
	annaToken = annaToken[:strings.Index(annaToken, `"`)]
	if me := send(annaToken, "GET", "/api/v1/me", "", 200); !strings.Contains(me, anna) {
		t.Fatalf("Анна's token: %s", me)
	}
	send(annaToken, "GET", "/api/v1/tasks", "", 200)
	send(annaToken, "GET", "/api/v1/projects", "", 403) // the application has no «projects:read»
	if page := send(anna, "GET", "/apps", "", 200); !strings.Contains(page, "Расчёт доставки") {
		t.Fatal("the list lacks the application")
	}

	// the left menu: «Приложения» and the application's own item
	if page := send(anna, "GET", "/apps", "", 200); !strings.Contains(page, `data-mid="apps"`) || !strings.Contains(page, `data-mid="app.`+a.ID+`"`) {
		t.Fatal("the menu lacks the application")
	}

	// «Кому доступно»: only the department «Продажи» (its sub-department counts) — Анна is out and loses her tokens
	var sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', (SELECT id FROM departments WHERE parent_id IS NULL LIMIT 1)) RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Восток', $1) RETURNING id`, sales).Scan(&east))
	if m := post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"access"}, "to": {"dept:" + sales}}); m != "Сохранено" {
		t.Fatalf("access: %s", m)
	}
	if page := send(boss, "GET", "/settings/apps", "", 200); !strings.Contains(page, "Продажи") {
		t.Fatal("the settings lack «Кому доступно»")
	}
	send(annaToken, "GET", "/api/v1/me", "", 401)
	send(anna, "GET", "/apps/"+a.ID, "", 403)
	send(anna, "GET", "/oauth/authorize?client_id="+a.ClientID, "", 403)
	if page := send(anna, "GET", "/apps", "", 200); strings.Contains(page, "Расчёт доставки") {
		t.Fatal("the list shows the application to Анна")
	}
	if page := send(anna, "GET", "/apps", "", 200); strings.Contains(page, `data-mid="apps"`) || strings.Contains(page, `data-mid="app.`) {
		t.Fatal("the menu shows the application to Анна")
	}
	must2 := func(_ any, err error) { must(err) }
	must2(pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, east))
	page = send(anna, "GET", "/apps/"+a.ID, "", 200)
	i = strings.Index(page, `name="AUTH_ID" value="`)
	annaToken = page[i+len(`name="AUTH_ID" value="`):]
	annaToken = annaToken[:strings.Index(annaToken, `"`)]
	send(annaToken, "GET", "/api/v1/me", "", 200)
	must2(pool.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, anna))
	send(annaToken, "GET", "/api/v1/me", "", 401) // out of the department — out of the application
	post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"access"}}) // everybody again

		// OAuth: a code only for the handler's host, once
	send(anna, "GET", "/oauth/authorize?client_id="+a.ClientID+"&redirect_uri="+url.QueryEscape("https://evil.example/x"), "", 400)
	send(anna, "GET", "/oauth/authorize?client_id=local.nope", "", 400)
	// signed out: go24 remembers the address and opens it after signing in
	send("", "GET", "/oauth/authorize?client_id="+a.ClientID, "", 302)
	if c := last.Cookies(); last.Header.Get("Location") != "/login" || len(c) != 1 || c[0].Name != backCookie || c[0].Path != "/auth" ||
		c[0].Value != url.QueryEscape("/oauth/authorize?client_id="+a.ClientID) {
		t.Fatalf("signed out: %s %v", last.Header.Get("Location"), last.Cookies())
	}
	send(anna, "GET", "/oauth/authorize?client_id="+a.ClientID+"&state=s1&redirect_uri="+url.QueryEscape(app.URL+"/back?x=1"), "", 302)
	back, err := url.Parse(last.Header.Get("Location"))
	must(err)
	code := back.Query().Get("code")
	if back.Path != "/back" || back.Query().Get("x") != "1" || back.Query().Get("state") != "s1" || code == "" || back.Query().Get("domain") != "go24.example" {
		t.Fatalf("authorize: %s", back)
	}
	tok := func(v url.Values, want int) map[string]any {
		t.Helper()
		var out map[string]any
		must(json.Unmarshal([]byte(send("", "POST", "/oauth/token", v.Encode(), want)), &out))
		return out
	}
	creds := url.Values{"client_id": {a.ClientID}, "client_secret": {a.ClientSecret}}
	with := func(kv ...string) url.Values {
		v := url.Values{}
		for k, vs := range creds {
			v[k] = vs
		}
		for i := 0; i < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}
		return v
	}
	if e := tok(url.Values{"client_id": {a.ClientID}, "client_secret": {"wrong"}, "grant_type": {"authorization_code"}, "code": {code}}, 401); e["error"] != "invalid_client" {
		t.Fatalf("wrong secret: %v", e)
	}
	got := tok(with("grant_type", "authorization_code", "code", code), 200)
	if got["user_id"] != anna || got["scope"] != "tasks:read" || got["member_id"] != apps.MemberID("https://go24.example") {
		t.Fatalf("token: %v", got)
	}
	if e := tok(with("grant_type", "authorization_code", "code", code), 400); e["error"] != "invalid_grant" {
		t.Fatalf("code used twice: %v", e)
	}
	refreshed := tok(with("grant_type", "refresh_token", "refresh_token", got["refresh_token"].(string)), 200)
	send(got["access_token"].(string), "GET", "/api/v1/me", "", 401) // the old pair is gone
	send(refreshed["access_token"].(string), "GET", "/api/v1/me", "", 200)
	tok(with("grant_type", "refresh_token", "refresh_token", got["refresh_token"].(string)), 400)
	if e := tok(with("grant_type", "password"), 400); e["error"] != "unsupported_grant_type" {
		t.Fatalf("grant: %v", e)
	}

	// a new key cuts every token off; so does uninstalling
	post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"secret"}})
	send(refreshed["access_token"].(string), "GET", "/api/v1/me", "", 401)
	send(annaToken, "GET", "/api/v1/me", "", 401)
	tok(with("grant_type", "refresh_token", "refresh_token", refreshed["refresh_token"].(string)), 401) // the old key
	page = send(anna, "GET", "/apps/"+a.ID, "", 200)
	i = strings.Index(page, `name="AUTH_ID" value="`)
	annaToken = page[i+len(`name="AUTH_ID" value="`):]
	annaToken = annaToken[:strings.Index(annaToken, `"`)]
	send(annaToken, "GET", "/api/v1/me", "", 200)
	post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"uninstall"}})
	send(annaToken, "GET", "/api/v1/me", "", 401)
	send(anna, "GET", "/apps/"+a.ID, "", 404)
	post(boss, "/settings/apps/"+a.ID, url.Values{"op": {"delete"}})
	if list, _ := srv.Apps.List(ctx); len(list) != 0 {
		t.Fatalf("deleted: %+v", list)
	}

	var kinds []string
	rows, err := pool.Query(ctx, `SELECT kind FROM audit_log WHERE kind LIKE 'app_%' ORDER BY id`)
	must(err)
	for rows.Next() {
		var k string
		must(rows.Scan(&k))
		kinds = append(kinds, k)
	}
	if strings.Join(kinds, ",") != "app_saved,app_installed,app_saved,app_saved,app_saved,app_uninstalled,app_deleted" {
		t.Fatalf("audit: %v", kinds)
	}
}

func TestLocalPath(t *testing.T) {
	for p, want := range map[string]bool{"/my": true, "/oauth/authorize?client_id=x": true, "//evil.example": false,
		"/\\evil.example": false, "https://evil.example": false, "": false, "/a\nSet-Cookie: x": false} {
		if localPath(p) != want {
			t.Errorf("localPath(%q) = %v", p, !want)
		}
	}
}
