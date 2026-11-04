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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/kb"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestPortalSearch: «Поиск» finds a word in people, tasks, projects and pages of the knowledge base, section by
// section, only what the person may see; «Показать все» opens one section; a query typed in the wrong layout is
// retyped; a task's key leads straight to it.
func TestPortalSearch(t *testing.T) {
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
	var boss, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Борис Отгрузкин', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), KB: kb.NewStore(pool), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err = ts.CreateProject(ctx, boss, "SH", "Отгрузки", "Всё про отгрузку")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "SH")
	must(err)
	for _, title := range []string{"Отгрузка 1", "Отгрузка 2", "Отгрузка 3", "Отгрузка 4", "Отгрузка 5", "Отгрузка 6"} {
		_, err = ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: title})
		must(err)
	}
	sp, err := srv.KB.CreateSpace(ctx, boss, "Склад", "", "")
	must(err)
	_, err = srv.KB.CreatePage(ctx, boss, sp, "", "Правила", "Отгрузку оформляет кладовщик")
	must(err)
	must(srv.KB.SetRight(ctx, boss, sp, "user", boss, "full")) // the base is closed to eve

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(who, path string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body), res.Header.Get("Location")
	}

	q := "/search?q=" + url.QueryEscape("отгруз")
	st, body, _ := get(boss, q)
	if st != 200 {
		t.Fatalf("search: %d", st)
	}
	for _, want := range []string{`href="/u/` + boss + `"`, `href="/t/SH-6"`, `href="/p/SH"`, "/kb/" + sp + "/", "in=tasks"} {
		if !strings.Contains(body, want) {
			t.Fatalf("no %q in the results", want)
		}
	}
	if strings.Count(body, `href="/t/SH-`) != searchFirst { // the rest behind «Показать все»
		t.Fatalf("tasks on the page of all = %d", strings.Count(body, `href="/t/SH-`))
	}
	if _, body, _ := get(boss, q+"&in=tasks"); !strings.Contains(body, "SH-1") || strings.Contains(body, "/kb/"+sp+"/") {
		t.Fatal("the section «Задачи» does not show all the tasks, or shows other sections")
	}
	if _, body, _ := get(eve, q); strings.Contains(body, "/kb/"+sp+"/") || strings.Contains(body, "/t/SH-") {
		t.Fatal("eve finds what is closed to her")
	}
	// «jnuhep» is «отгруз» typed in the QWERTY layout
	if _, body, _ := get(boss, "/search?q=jnuhep"); !strings.Contains(body, `href="/t/SH-6"`) || !strings.Contains(body, "отгруз") {
		t.Fatal("the query in the wrong layout is not retyped")
	}
	if st, _, loc := get(boss, "/search?q=sh-2"); st != http.StatusFound || loc != "/t/SH-2" {
		t.Fatalf("a task's key: %d %s", st, loc)
	}
}
