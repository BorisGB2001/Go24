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

// TestTemplateSubtaskRoutes: «Добавить подзадачу» of a template, its page with the subtask templates, editing a
// template, and a task made from the form of a template gets the subtasks.
func TestTemplateSubtaskRoutes(t *testing.T) {
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
	form, _ := do(boss, "GET", "/templates/new?parent="+base, nil)
	has(form, `name="template_parent" value="`+base+`"`, "Новая подзадача шаблона", `href="/templates/`+base+`"`)
	if strings.Contains(form, `name="repeat"`) || strings.Contains(form, `name="for_new_users"`) {
		t.Fatal("a subtask template offers to repeat")
	}
	if _, loc := do(anna, "POST", "/templates/new", url.Values{"title": {"x"}, "project": {"AA"}, "template_parent": {base}}); strings.HasPrefix(loc, "/templates/"+base) {
		t.Fatal("someone else added a subtask template")
	}
	_, loc := do(boss, "POST", "/templates/new", url.Values{"title": {"Учётная запись"}, "project": {"AA"}, "responsible": {anna},
		"template_parent": {base}, "deadline_days": {"1"}, "deadline_hours": {"2"}})
	if !strings.HasPrefix(loc, "/templates/"+base+"?msg=") {
		t.Fatalf("saved subtask template: %q", loc)
	}
	page, _ := do(boss, "GET", "/templates/"+base, nil)
	has(page, "Подзадачи шаблона", "Учётная запись", "через 1 дн. 2 ч", "/templates/new?parent="+base)
	list, _ := do(boss, "GET", "/templates", nil)
	if !strings.Contains(list, "Новый сотрудник") || strings.Contains(list, "Учётная запись") {
		t.Fatal("the list shows the subtask template apart")
	}
	// the base is edited (its fields saved again), now «for new employees»
	if _, loc := do(boss, "POST", "/templates/"+base, url.Values{"title": {"Новый сотрудник: выход"}, "project": {"AA"}, "responsible": {boss},
		"for_new_users": {"on"}}); !strings.HasPrefix(loc, "/templates?msg=") {
		t.Fatalf("edit: %q", loc)
	}
	list, _ = do(boss, "GET", "/templates", nil)
	has(list, "для новых сотрудников")
	edit, _ := do(boss, "GET", "/templates/"+base, nil)
	has(edit, `name="for_new_users" data-new-users checked`)
	newTask, _ := do(boss, "GET", "/tasks/new?template="+base, nil)
	has(newTask, "Вместе с задачей будут созданы подзадачи шаблона: 1")
	_, loc = do(boss, "POST", "/tasks/new", url.Values{"title": {"Выход Анны"}, "project": {"AA"}, "responsible": {boss}, "template": {base}})
	if !strings.HasPrefix(loc, "/t/AA-") {
		t.Fatalf("task from template: %q", loc)
	}
	id, err := ts.TaskIDByKey(ctx, strings.TrimPrefix(loc, "/t/"))
	must(err)
	c, err := ts.GetCard(ctx, boss, id)
	must(err)
	if len(c.Subtasks) != 1 || c.Subtasks[0].Title != "Учётная запись" {
		t.Fatalf("subtasks = %+v", c.Subtasks)
	}
	tpl, err := ts.Template(ctx, boss, base)
	must(err)
	if _, loc := do(boss, "POST", "/templates/"+tpl.Subtasks[0].ID+"/delete", url.Values{}); loc != "/templates/"+base {
		t.Fatalf("delete subtask template: %q", loc)
	}
}
