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

// TestScrumDodRoutes: «Команда и DoD» of a scrum project — the team and the task types with their checklists; the
// DoD block in a sprint task's card, «Сохранить и завершить», completing blocked until the DoD is ticked; «Скорость
// команды» on the completed sprints.
func TestScrumDodRoutes(t *testing.T) {
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
	_, err = ts.CreateProject(ctx, boss, "SD", "Скрам", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "SD")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))

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
	must(ts.SetProjectKind(ctx, p, "scrum"))
	p, _ = ts.ProjectByKey(ctx, boss, "SD")
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Экран входа"})
	must(err)
	tid, _ := ts.TaskIDByKey(ctx, key)
	must(ts.CreateSprint(ctx, p, "Спринт 1"))
	plan, err := ts.ScrumPlanFor(ctx, boss, p)
	must(err)
	sp := plan.Sprints[0].ID
	must(ts.MoveToSprint(ctx, boss, tid, sp))
	must(ts.StartSprint(ctx, p, sp))

	page, _ := do(boss, "GET", "/p/SD/dod", nil)
	has(page, "Скрам-команда", "Владелец продукта", `name="master"`, "Definition of Done", `value="Задача"`, `value="Ошибка"`)
	if _, loc := do(anna, "POST", "/p/SD/dod", url.Values{"op": {"team"}, "master": {anna}}); loc == "/p/SD/dod" {
		t.Fatalf("a member set the team: %q", loc)
	}
	if _, loc := do(boss, "POST", "/p/SD/dod", url.Values{"op": {"team"}, "master": {anna}}); loc != "/p/SD/dod" {
		t.Fatalf("team: %q", loc)
	}
	types, err := ts.ScrumTypes(ctx, p.ID)
	must(err)
	// the scrum master sets the checklist
	if _, loc := do(anna, "POST", "/p/SD/dod", url.Values{"op": {"type"}, "id": {types[0].ID}, "name": {"Задача"},
		"items": {"Тесты\r\nРевью"}, "required": {"on"}}); loc != "/p/SD/dod#dod" {
		t.Fatalf("type: %q", loc)
	}
	card, _ := do(anna, "GET", "/t/"+key, nil)
	has(card, `id="dod"`, "Тесты", "Ревью", `name="item"`, "Сохранить и завершить", "отметьте все пункты Definition of Done")
	if _, loc := do(anna, "POST", "/t/"+key+"/action", url.Values{"action": {"complete"}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("completed without DoD: %q", loc)
	}
	types, _ = ts.ScrumTypes(ctx, p.ID)
	if _, loc := do(anna, "POST", "/t/"+key+"/dod", url.Values{"item": {types[0].Items[0].ID, types[0].Items[1].ID}, "complete": {"1"}}); loc != "/t/"+key+"#dod" {
		t.Fatalf("save and complete: %q", loc)
	}
	tk, err := ts.TaskByID(ctx, tid)
	must(err)
	if tk.Status != tasks.StatusCompleted {
		t.Fatalf("status = %s", tk.Status)
	}
	if _, loc := do(anna, "POST", "/p/SD/sprints", url.Values{"op": {"complete"}, "id": {sp}}); loc != "/p/SD/sprints" {
		t.Fatalf("the scrum master completes the sprint: %q", loc)
	}
	page, _ = do(boss, "GET", "/p/SD/sprints", nil)
	has(page, "Скорость команды", `class="done"`)
}
