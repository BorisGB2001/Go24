package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/portal"
	"go24/internal/tasks"
)

// TestPortalSettingsHTTP: «Общие настройки» — administrators only change the name, logo, language and formats; the
// name and logo show in the menu, the formats in dates, the language when the browser asks for none; REST.
func TestPortalSettingsHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Portal: portal.NewStore(pool, bundle),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false), "": nil}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, ct string, body io.Reader, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if as[who] != nil {
			req.AddCookie(as[who])
		}
		req.Header.Set("Accept-Language", "ru")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
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
	form := func(fields map[string]string, logo []byte) (string, io.Reader) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if logo != nil {
			fw, _ := mw.CreateFormFile("logo", "logo.svg")
			fw.Write(logo)
		}
		mw.Close()
		return mw.FormDataContentType(), &buf
	}

	if page := send(anna, "GET", "/settings", "", nil, 200); strings.Contains(page, `href="/settings/portal"`) || !strings.Contains(page, `href="/settings/keys"`) {
		t.Error("an employee's «Настройки» list the administrator's pages or miss their own")
	}
	if page := send(boss, "GET", "/settings", "", nil, 200); !strings.Contains(page, `href="/settings/portal"`) || !strings.Contains(page, `href="/settings/webhooks"`) {
		t.Error("the administrator's «Настройки» miss pages")
	}
	send(anna, "GET", "/settings/portal", "", nil, 403)
	ct, body := form(map[string]string{"name": "Ромашка"}, nil)
	send(anna, "POST", "/settings/portal", ct, body, 403)
	ct, body = form(map[string]string{"tz": "Mars/Base"}, nil)
	send(boss, "POST", "/settings/portal", ct, body, 303)
	if srv.Portal.Current().TZ != "" {
		t.Error("a wrong time zone is saved")
	}
	ct, body = form(map[string]string{"name": "Ромашка", "lang": "en-US", "tz": "Europe/Moscow", "date_format": "ymd", "time_format": "24"},
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`))
	send(boss, "POST", "/settings/portal", ct, body, 303)
	set, err := srv.Portal.Get(ctx)
	must(err)
	if set != (portal.Settings{Name: "Ромашка", Lang: "en-US", TZ: "Europe/Moscow", DateFormat: "ymd", TimeFormat: "24", HasLogo: true}) {
		t.Errorf("settings = %+v", set)
	}
	page := send(boss, "GET", "/settings/portal", "", nil, 200)
	for _, want := range []string{`value="Ромашка"`, `<img class="plogo" src="/portal/logo"`, `<span class="pname">Ромашка</span>`, `value="ymd" selected`, "· Ромашка</title>"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	if srv.I18n.T("ru-RU", "format.datetime") != "2006-01-02 15:04" {
		t.Error("the date format is not used")
	}
	// the logo is open to all (the sign-in page), an SVG with no scripts
	req, _ := http.NewRequest("GET", hs.URL+"/portal/logo", nil)
	res, err := http.DefaultClient.Do(req)
	must(err)
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/svg+xml" || !strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("logo: %d %s", res.StatusCode, res.Header)
	}
	// the portal's language when the browser asks for none (send asks for Russian)
	req, _ = http.NewRequest("GET", hs.URL+"/settings", nil)
	req.AddCookie(as[anna])
	res, err = client.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(raw), `lang="en-US"`) {
		t.Errorf("language: %.200s", raw)
	}
	ct, body = form(map[string]string{"name": "Ромашка", "lang": "en-US", "tz": "Europe/Moscow", "logo_delete": "1"}, nil)
	send(boss, "POST", "/settings/portal", ct, body, 303)
	send("", "GET", "/portal/logo", "", nil, 404)

	// REST: any key with settings:read reads, an administrator's key with settings:write changes
	key := func(user string, scopes ...string) string {
		tok, err := srv.Keys.Create(ctx, user, "k", scopes, 0)
		must(err)
		return tok
	}
	api := func(tok, method, body string, want int) portal.Settings {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/settings/portal", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var set portal.Settings
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("%s: %d, want %d: %s", method, res.StatusCode, want, raw)
		}
		_ = json.NewDecoder(res.Body).Decode(&set)
		return set
	}
	if got := api(key(anna, "settings:read"), "GET", "", 200); got.Name != "Ромашка" || got.HasLogo {
		t.Errorf("GET = %+v", got)
	}
	api(key(anna, "settings:read", "settings:write"), "PUT", `{"name":"x"}`, 403)
	api(key(boss, "settings:read"), "PUT", `{"name":"x"}`, 403)
	api(key(boss, "settings:write"), "PUT", `{"date_format":"zz"}`, 422)
	if got := api(key(boss, "settings:write"), "PUT", `{"name":"Лютик","date_format":""}`, 200); got.Name != "Лютик" || got.Lang != "en-US" || got.DateFormat != "" {
		t.Errorf("PUT = %+v", got)
	}
	if srv.I18n.T("ru-RU", "format.date") != "02.01.2006" {
		t.Error("the language's date format is not back")
	}

	// «Формат имени» (the page is in the portal's English): the names of the employees with a card are recomposed; those
	// without keep Authentik's
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE users SET first_name = 'Анна', last_name = 'Петрова', second_name = 'Сергеевна' WHERE id = $1`, anna))
	name := func(id string) (n string) {
		must(pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, id).Scan(&n))
		return
	}
	if page := send(boss, "GET", "/settings/portal", "", nil, 200); !strings.Contains(page, `>Smith J. R.</option>`) {
		t.Error("the page has no sample of the name formats")
	}
	ct, body = form(map[string]string{"name": "Лютик", "name_format": "last_first_second"}, nil)
	send(boss, "POST", "/settings/portal", ct, body, 303)
	if n := name(anna); n != "Петрова Анна Сергеевна" || name(boss) != "Борис" {
		t.Errorf("names: %q %q", n, name(boss))
	}
	if portal.UserName("Иван", "Иванов", "") != "Иванов Иван" { // the profile saved now takes the format too
		t.Error("UserName does not follow the portal")
	}
	api(key(boss, "settings:write"), "PUT", `{"name_format":"xx"}`, 422)
	if got := api(key(boss, "settings:write"), "PUT", `{"name_format":"last_initials"}`, 200); got.NameFormat != "last_initials" || name(anna) != "Петрова А. С." {
		t.Errorf("PUT name_format = %+v, %q", got, name(anna))
	}
	ct, body = form(map[string]string{"name": "Лютик", "name_format": "first_last"}, nil) // back to the default
	send(boss, "POST", "/settings/portal", ct, body, 303)
	if n := name(anna); n != "Анна Петрова" || srv.Portal.Current().NameFormat != "" {
		t.Errorf("default: %q %+v", n, srv.Portal.Current())
	}
}
