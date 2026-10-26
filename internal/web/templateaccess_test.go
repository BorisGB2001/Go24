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
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestTemplateAccessRoutes: «Права доступа» on a template's page; one with «Чтение» sees it in the list with its
// author, the form read-only, makes a task by it and cannot save it; the rights are saved by the author only.
func TestTemplateAccessRoutes(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := newUser("Борис"), newUser("Анна")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	base, err := ts.SaveTemplate(ctx, boss, tasks.Template{ProjectKey: "AA", Title: "Новый сотрудник"})
	must(err)

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), res.Header.Get("Location")
	}

	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("page lacks %q", w)
			}
		}
	}
	edit, _ := do(boss, "GET", "/templates/"+base, nil)
	has(edit, "Права доступа", `action="/templates/`+base+`/access"`, `name="read"`, `name="full"`)
	if _, loc := do(anna, "POST", "/templates/"+base+"/access", url.Values{"read": {anna}}); strings.Contains(loc, "msg=") {
		t.Fatalf("someone else shared it: %q", loc)
	}
	if _, loc := do(boss, "POST", "/templates/"+base+"/access", url.Values{"read": {anna}}); !strings.HasPrefix(loc, "/templates/"+base+"?msg=") {
		t.Fatalf("access saved: %q", loc)
	}
	edit, _ = do(boss, "GET", "/templates/"+base, nil)
	has(edit, `value="`+anna+`"`)

	list, _ := do(anna, "GET", "/templates", nil)
	has(list, "Новый сотрудник", "Доступен вам · автор Борис", `action="/templates/`+base+`/run"`)
	if strings.Contains(list, `action="/templates/`+base+`/delete"`) {
		t.Fatal("a reader is offered to delete")
	}
	page, _ := do(anna, "GET", "/templates/"+base, nil)
	has(page, "Шаблон доступен вам для чтения (автор — Борис)", "<fieldset disabled", "/tasks/new?template="+base)
	if strings.Contains(page, "Права доступа") {
		t.Fatal("a reader is offered the rights")
	}
	if _, loc := do(anna, "POST", "/templates/"+base, url.Values{"title": {"Взлом"}, "project": {"AA"}}); strings.HasPrefix(loc, "/templates?msg=") {
		t.Fatal("a reader saved it")
	}
	if _, loc := do(anna, "POST", "/templates/"+base+"/run", nil); !strings.HasPrefix(loc, "/t/AA-") {
		t.Fatalf("task by a shared template: %q", loc)
	}
	if tpl, _ := ts.Template(ctx, boss, base); tpl.Title != "Новый сотрудник" {
		t.Fatalf("template = %+v", tpl)
	}
}
