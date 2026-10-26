package web

import (
	"context"
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

// TestManyResponsible: several responsible people in the new task's form make a task for each of them, as in
// the portal; a regular task makes a template for each; «+» on a kanban column still makes one task.
func TestManyResponsible(t *testing.T) {
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
	boss, anna, petr := newUser("Борис"), newUser("Анна"), newUser("Пётр")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "MR", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "MR")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	must(ts.SetMember(ctx, p, petr, "member"))

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, accept string) (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), res.Header.Get("Location")
	}
	byResp := func(title string) map[string]int {
		t.Helper()
		list, err := ts.ListTasks(ctx, tasks.Filter{UserID: boss, Role: "creator"})
		must(err)
		got := map[string]int{}
		for _, x := range list {
			if x.Title == title {
				got[x.ResponsibleID]++
			}
		}
		return got
	}

	// the new task's form: the responsible field takes several people and has the hint; a template's takes one
	form, _ := do(boss, "GET", "/tasks/new", nil, "")
	if !strings.Contains(form, `data-name="responsible" data-multiple`) || !strings.Contains(form, "data-resp-many=") {
		t.Fatal("the new task's form has a single responsible")
	}

	// one person (and the same one twice): one task, its card opens
	_, loc := do(boss, "POST", "/tasks/new", url.Values{"project": {"MR"}, "title": {"Один"}, "responsible": {anna, anna}}, "")
	if !strings.HasPrefix(loc, "/t/MR-") || len(byResp("Один")) != 1 {
		t.Fatalf("one responsible: %q %v", loc, byResp("Один"))
	}

	// two people: a task of each one's own with the same fields and checklist, then «Поручил»
	_, loc = do(boss, "POST", "/tasks/new", url.Values{"project": {"MR"}, "title": {"Сдать отчёт"}, "responsible": {anna, petr},
		"priority": {"2"}, "auditors": {boss}, "checklist": {"Цифры\nГрафики"}}, "")
	if loc != "/my?role=creator" {
		t.Fatalf("two responsible: redirect %q", loc)
	}
	if got := byResp("Сдать отчёт"); got[anna] != 1 || got[petr] != 1 || len(got) != 2 {
		t.Fatalf("two responsible: tasks %v", got)
	}
	list, err := ts.ListTasks(ctx, tasks.Filter{UserID: boss, Role: "creator"})
	must(err)
	for _, x := range list {
		if x.Title != "Сдать отчёт" {
			continue
		}
		c, err := ts.GetCard(ctx, boss, x.ID)
		must(err)
		if c.Priority != 2 || len(c.Checklist) != 2 || !slices.ContainsFunc(c.Auditors, func(u tasks.User) bool { return u.ID == boss }) {
			t.Fatalf("a copy for %s: priority %d, checklist %d, auditors %v", x.ResponsibleID, c.Priority, len(c.Checklist), c.Auditors)
		}
	}

	// a regular task for two: a template of each
	do(boss, "POST", "/tasks/new", url.Values{"project": {"MR"}, "title": {"Планёрка"}, "responsible": {anna, petr}, "repeat": {"on"},
		"period": {"weekly"}, "every": {"1"}, "week_days": {"1"}, "at": {"09:00"}, "starts_on": {"2026-11-02"}}, "")
	tpls, err := ts.Templates(ctx, boss)
	must(err)
	var who []string
	for _, x := range tpls {
		if x.Title == "Планёрка" {
			who = append(who, x.ResponsibleID)
		}
	}
	if slices.Sort(who); len(who) != 2 || !slices.Contains(who, anna) || !slices.Contains(who, petr) {
		t.Fatalf("regular task for two: templates of %v", who)
	}

	// «+» on a kanban column answers one task
	do(boss, "POST", "/p/MR/tasks", url.Values{"title": {"С доски"}, "responsible": {anna, petr}}, "application/json")
	if got := byResp("С доски"); len(got) != 1 || got[anna] != 1 {
		t.Fatalf("kanban «+»: tasks %v", got)
	}
}
