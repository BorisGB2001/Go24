package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/menu"
	"go24/internal/tasks"
)

// TestMenuHTTP: «Настроить меню» — an employee's own order, hidden items and links are drawn by the server, the first
// item is the start page; a reset brings the company's menu back; only an administrator sets it for all.
func TestMenuHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, Menus: menu.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false), ivan: cookie(ivan, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, body string, want int) (string, *http.Response) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		req.Header.Set("Accept", "application/json")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res
	}
	// the order of the menu's items on a page (data-mid, the shown ones and then the hidden)
	order := func(who string) (shown, hidden []string) {
		t.Helper()
		page, _ := send(who, "GET", "/settings", "", 200)
		nav, rest, _ := strings.Cut(page, `data-menu-hidden`)
		rest, _, _ = strings.Cut(rest, `data-menu-tools`)
		pick := func(s string) (out []string) {
			for _, part := range strings.Split(s, `data-mid="`)[1:] {
				id, _, _ := strings.Cut(part, `"`)
				out = append(out, id)
			}
			return out
		}
		return pick(nav), pick(rest)
	}
	start := func(who string) string {
		t.Helper()
		_, res := send(who, "GET", "/", "", http.StatusFound)
		return res.Header.Get("Location")
	}

	shown, hidden := order(anna)
	if len(shown) != len(menu.Builtin)-2 || shown[0] != "collabs" || hidden != nil { // no messenger and no applications here
		t.Fatalf("go24's menu = %v %v", shown, hidden)
	}
	if start(anna) != "/my" {
		t.Errorf("default start = %s", start(anna))
	}

	// Анна: «Проекты» first, «Лента» hidden, a link of her own; the server cleans what it does not know
	send(anna, "POST", "/menu", `{"order":["projects","tasks","feed","bogus"],"hidden":["feed"],"custom":[{"title":"Канбан","url":"/crm/deals?view=kanban","new_tab":true}]}`, 200)
	shown, hidden = order(anna)
	if shown[0] != "projects" || shown[1] != "company" || hidden[0] != "feed" || len(hidden) != 1 {
		t.Errorf("own menu = %v %v", shown, hidden)
	}
	page, _ := send(anna, "GET", "/settings", "", 200)
	if !strings.Contains(page, `href="/crm/deals?view=kanban" target="_blank"`) || !strings.Contains(page, `data-menu-act="reset"`) || strings.Contains(page, `data-menu-act="apply-all"`) {
		t.Error("the own link, «Сбросить» or the administrator's tools are wrong")
	}
	if start(anna) != "/projects" {
		t.Errorf("start = %s", start(anna))
	}
	send(anna, "POST", "/menu", `{"order":[],"custom":[{"title":"x","url":"javascript:alert(1)"}]}`, 422)
	send(anna, "POST", "/menu?all=1", `{"order":["kb"]}`, 403)
	send(anna, "POST", "/menu/reset?all=1", "", 403)

	// the administrator's menu for all: Иван (no menu of his own) gets it, Анна keeps hers until she resets it
	send(boss, "POST", "/menu?all=1", `{"order":["mail","feed"],"hidden":["kb"]}`, 200)
	if shown, hidden = order(ivan); shown[0] != "mail" || hidden[0] != "kb" {
		t.Errorf("company's menu = %v %v", shown, hidden)
	}
	if start(ivan) != "/mail" {
		t.Errorf("company's start = %s", start(ivan))
	}
	if shown, _ = order(anna); shown[0] != "projects" {
		t.Errorf("Анна's own menu is lost: %v", shown)
	}
	send(anna, "POST", "/menu/reset", "", 204)
	if shown, _ = order(anna); shown[0] != "mail" {
		t.Errorf("after the reset = %v", shown)
	}
	send(boss, "POST", "/menu/reset?all=1", "", 204)
	if shown, hidden = order(ivan); shown[0] != "collabs" || hidden != nil || start(ivan) != "/my" {
		t.Errorf("go24's menu is not back: %v %v", shown, hidden)
	}
	// an outside link first is no start page
	send(ivan, "POST", "/menu", `{"order":["c1"],"custom":[{"id":"c1","title":"Сайт","url":"https://example.com"}]}`, 200)
	if start(ivan) != "/my" {
		t.Errorf("outside start = %s", start(ivan))
	}
}
