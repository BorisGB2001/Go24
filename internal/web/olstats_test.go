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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestOpenLinesStatsHTTP: «Статистика» of the open lines as a page and CSV; an administrator picks any operator, an
// operator sees only their own dialogues.
func TestOpenLinesStatsHTTP(t *testing.T) {
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
	var boss, anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wid, err := ol.SaveWidget(ctx, boss, openlines.Widget{Name: "Сайт магазина", Active: true, Operators: []string{anna, eve}})
	must(err)
	for _, who := range []string{anna, anna, eve} {
		sid, tok, err := ol.Visit(ctx, wid, "", "")
		must(err)
		must(ol.VisitorSay(ctx, wid, tok, "Здравствуйте"))
		must(ol.Reply(ctx, who, sid, "Добрый день"))
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(x auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, x))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	get := func(who, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(as[who])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	code, page := get(boss, "/openlines/stats?by=operators")
	if code != http.StatusOK || !strings.Contains(page, "Анна") || !strings.Contains(page, "Ева") || !strings.Contains(page, `name="operator"`) {
		t.Fatalf("the administrator's report %d", code)
	}
	_, csv := get(boss, "/openlines/stats?by=lines&csv=1")
	if !strings.Contains(csv, "Сайт магазина;3;0;0;") || !strings.Contains(csv, "\nВсего;3;0;0;") {
		t.Fatalf("csv %q", csv)
	}
	// the operator: their own two, no operator filter (also when asked for another one)
	_, csv = get(anna, "/openlines/stats?by=operators&operator="+eve+"&csv=1")
	if !strings.Contains(csv, "Анна;2;") || strings.Contains(csv, "Ева") {
		t.Fatalf("the operator's csv %q", csv)
	}
	if _, page = get(anna, "/openlines/stats"); strings.Contains(page, `name="operator"`) {
		t.Fatal("an operator picks another operator")
	}
}
