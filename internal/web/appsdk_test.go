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
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestAppSDKHTTP: go24.js in an application's frame, as BX24.js — the library is public; /apps/frame
// gives the frame's page the employee's new tokens (working in REST), the place and its options, the settings and
// the application's hosts, only to whom the application is for; app.info, app.option.* (set — administrators) and
// user.option.* (each employee's own) over REST; the pages of the frame load the host script and the picker.
func TestAppSDKHTTP(t *testing.T) {
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
	app := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle, CRM: crm.NewStore(pool),
		Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool), Audit: audit.NewStore(pool), AdminGroups: []string{"go24-admins"},
		BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
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
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	has := func(page string, yes bool, want ...string) {
		t.Helper()
		for _, w := range want {
			if strings.Contains(page, w) != yes {
				t.Fatalf("page has %q: %v, want %v", w, !yes, yes)
			}
		}
	}
	const js = "application/json"

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Доставка", HandlerURL: app.URL + "/page", Scopes: []string{"crm:read", "placement"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	form := "application/x-www-form-urlencoded"

	// the library: public, no account
	req, _ := http.NewRequest("GET", hs.URL+"/go24.js", nil)
	res, err := http.DefaultClient.Do(req)
	must(err)
	lib, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "javascript") || !strings.Contains(string(lib), "window.go24 =") {
		t.Fatalf("go24.js: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// the application's page loads the host script, the picker and the frame of the application
	has(send(anna, "GET", "/apps/"+id+"?placement=CALENDAR&options="+url.QueryEscape(`{"day":"1"}`), "", "", 200), true,
		"js/apphost.js", "data-app-select", `data-app="`+id+`"`, `data-placement="CALENDAR"`, "data-app-post", "Выберите сотрудников")
	has(send(boss, "GET", "/settings/apps", "", "", 200), true, "/go24.js")

	// /apps/frame: Анна's new tokens, the place, the hosts
	var fr struct {
		Origins   []string          `json:"origins"`
		Auth      map[string]any    `json:"auth"`
		Placement string            `json:"placement"`
		Options   map[string]string `json:"options"`
		Admin     bool              `json:"admin"`
		UserID    string            `json:"user_id"`
		AppOpts   map[string]any    `json:"app_options"`
		UserOpts  map[string]any    `json:"user_options"`
	}
	must(json.Unmarshal([]byte(send(anna, "POST", "/apps/frame", form, "app="+id+"&placement=CALENDAR&options="+url.QueryEscape(`{"day":"1"}`), 200)), &fr))
	annaTok, _ := fr.Auth["access_token"].(string)
	if len(fr.Origins) != 1 || fr.Origins[0] != app.URL || fr.Placement != "CALENDAR" || fr.Options["day"] != "1" || fr.Admin || fr.UserID != anna ||
		fr.Auth["domain"] != "go24.example" || !strings.HasPrefix(annaTok, "go24at_") {
		t.Fatalf("frame: %+v", fr)
	}
	has(send(annaTok, "GET", "/api/v1/me", "", "", 200), true, anna)
	send(anna, "POST", "/apps/frame", form, "app=nothing", 404)

	// app.info; app.option.set — administrators only; user.option.* — each employee's own
	has(send(annaTok, "GET", "/api/v1/app", "", "", 200), true, `"code": "`+a.ClientID+`"`, `"user_id": "`+anna+`"`, `"admin": false`)
	key, err := srv.Keys.Create(ctx, boss, "ключ", []string{"crm:read"}, 0)
	must(err)
	has(send(key, "GET", "/api/v1/app/options", "", "", 403), true, "только приложению")
	send(annaTok, "POST", "/api/v1/app/options", js, `{"mode": "fast"}`, 403)
	tk, err := srv.Apps.Issue(ctx, a.ID, boss)
	must(err)
	bossTok := tk.Access
	has(send(bossTok, "POST", "/api/v1/app/options", js, `{"mode": "fast", "limits": {"kg": 30}}`, 200), true, `"mode": "fast"`, `"kg": 30`)
	has(send(annaTok, "GET", "/api/v1/app/options?option=limits", "", "", 200), true, `"kg": 30`)
	if v := strings.TrimSpace(send(annaTok, "GET", "/api/v1/app/options?option=none", "", "", 200)); v != "null" {
		t.Fatalf("no option: %s", v)
	}
	send(annaTok, "POST", "/api/v1/app/user-options", js, `{"tab": "map"}`, 200)
	has(send(bossTok, "GET", "/api/v1/app/user-options", "", "", 200), false, "map")
	has(send(bossTok, "POST", "/api/v1/app/options", js, `{"mode": null}`, 200), false, "fast")
	has(send(bossTok, "POST", "/api/v1/app/options", js, `{"": 1}`, 422), true, "128")
	must(json.Unmarshal([]byte(send(anna, "POST", "/apps/frame", form, "app="+id, 200)), &fr))
	if fr.Placement != "DEFAULT" || fr.UserOpts["tab"] != "map" || fr.AppOpts["mode"] != nil || fr.AppOpts["limits"] == nil {
		t.Fatalf("frame settings: %+v", fr)
	}

	// a card's tab: the place and the card from the binding, not from the page
	send(bossTok, "POST", "/api/v1/placements", js, `{"placement": "CRM_DEAL_DETAIL_TAB", "handler": "`+app.URL+`/deal"}`, 201)
	var tab string
	must(pool.QueryRow(ctx, `SELECT id::text FROM app_placements`).Scan(&tab))
	must(json.Unmarshal([]byte(send(anna, "POST", "/apps/frame", form, "tab="+tab+"&id=42&placement=DEFAULT", 200)), &fr))
	if fr.Placement != "CRM_DEAL_DETAIL_TAB" || fr.Options["ID"] != "42" {
		t.Fatalf("tab frame: %+v", fr)
	}

	// «Кому доступно» — only Борис: Анна's frame gets nothing
	must(srv.Apps.SetAccess(ctx, a.ID, []string{boss}))
	send(anna, "POST", "/apps/frame", form, "app="+id, 404)
	send(anna, "POST", "/apps/frame", form, "tab="+tab+"&id=42", 404)
	send(boss, "POST", "/apps/frame", form, "app="+id, 200)
}
