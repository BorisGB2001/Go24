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
	"slices"
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

// TestSectionsHTTP: «Инструменты» — an administrator switches sections off; their pages, REST and the incoming
// webhook answer «Раздел отключён», the menu and «Настройки» lose them, the rest stay; REST reads and sets them
// (administrators); the change is in «Журнал событий».
func TestSectionsHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	bossKey, err := srv.Keys.Create(ctx, boss, "admin", apikeys.AllScopes, 0)
	must(err)
	annaKey, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	keys := map[string]string{boss: bossKey, anna: annaKey}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	send := func(who, method, path, body string, api bool, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if api {
			req.Header.Set("Authorization", "Bearer "+keys[who])
			req.Header.Set("Content-Type", "application/json")
		} else {
			req.AddCookie(as[who])
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if want != 0 && res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		last = res
		return string(raw)
	}

	send(anna, "GET", "/settings/tools", "", false, 403)
	if page := send(boss, "GET", "/settings/tools", "", false, 200); !strings.Contains(page, `value="crm" checked`) {
		t.Error("CRM is not on at first")
	}
	on := url.Values{}
	for _, sec := range portalSections {
		if sec.ID != "crm" && sec.ID != "mail" {
			on.Add("on", sec.ID)
		}
	}
	send(boss, "POST", "/settings/tools", on.Encode(), false, 303)
	if u, _ := url.Parse(last.Header.Get("Location")); u.Query().Get("msg") != "Разделы сохранены" {
		t.Errorf("message %q", u.Query().Get("msg"))
	}

	// the pages, REST and the webhook of CRM and Mail are off; the rest are not
	for _, p := range []string{"/crm/deals", "/crm", "/mail"} {
		if page := send(anna, "GET", p, "", false, 404); !strings.Contains(page, "отключён администратором") {
			t.Errorf("%s: %.200s", p, page)
		}
	}
	if body := send(anna, "GET", "/api/v1/crm/deals", "", true, 404); !strings.Contains(body, "отключён") {
		t.Errorf("REST of CRM: %s", body)
	}
	send(anna, "GET", "/rest/"+anna+"/"+annaKey+"/crm/deals", "", false, 404)
	send(anna, "GET", "/api/v1/me", "", true, 200)
	for p, want := range map[string]bool{"/crm/deals": true, "/api/v1/mail/messages": true, "/crmx": false, "/calendar": false, "/settings": false} {
		if _, off := srv.offSection(ctx, p); off != want {
			t.Errorf("offSection(%s) = %v", p, off)
		}
	}
	if got := srv.availOn(ctx, []string{"tasks", "crm", "calendar", "mail"}); !slices.Equal(got, []string{"tasks", "calendar"}) {
		t.Errorf("menu items: %v", got)
	}
	settings := send(boss, "GET", "/settings", "", false, 200)
	if strings.Contains(settings, `href="/crm/roles"`) || !strings.Contains(settings, `href="/settings/tools"`) || !strings.Contains(settings, `href="/settings/admins"`) {
		t.Error("«Настройки» with CRM off")
	}

	// REST: read and set (administrators)
	var got struct {
		Sections []struct {
			ID string `json:"id"`
			On bool   `json:"on"`
		} `json:"sections"`
	}
	must(json.Unmarshal([]byte(send(boss, "GET", "/api/v1/settings/sections", "", true, 200)), &got))
	for _, s := range got.Sections {
		if s.On == (s.ID == "crm" || s.ID == "mail") {
			t.Errorf("REST: %s on %v", s.ID, s.On)
		}
	}
	send(anna, "PUT", "/api/v1/settings/sections", `{"off":[]}`, true, 403)
	send(boss, "PUT", "/api/v1/settings/sections", `{"off":["bi","nonsense"]}`, true, 200)
	if _, off := srv.offSection(ctx, "/crm/deals"); off {
		t.Error("CRM is still off")
	}
	if off := srv.sectionsOff(ctx); !slices.Equal(off, []string{"bi"}) {
		t.Errorf("off: %v", off)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE kind = 'sections_saved'`).Scan(&n))
	if n != 2 {
		t.Errorf("«Журнал событий»: %d changes, want 2", n)
	}
}
