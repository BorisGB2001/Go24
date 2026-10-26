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

// TestTaskCopyDelegate: «Копировать» opens the new task's form filled from the task and the copy takes its tags;
// «Делегировать» makes another person the responsible and leaves the former one as an auditor.
func TestTaskCopyDelegate(t *testing.T) {
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
	boss, anna, petr, outsider := newUser("Борис"), newUser("Анна"), newUser("Пётр"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	must(ts.SetMember(ctx, p, petr, "member"))
	key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Собрать отчёт", Description: "Квартальный", ResponsibleID: anna,
		Priority: 2, Auditors: []string{petr}, Checklist: []string{"Цифры", "Графики"}})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
	must(err)
	must(ts.SetTag(ctx, boss, id, "отчёты", true))

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

	// the card offers «Копировать» to everyone in it and «Делегировать» only to the responsible
	if body, _ := do(anna, "GET", "/t/"+key, nil); !strings.Contains(body, "/tasks/new?copy="+key) || !strings.Contains(body, `action="/t/`+key+`/delegate"`) {
		t.Fatal("the responsible's card lacks copy or delegate")
	}
	if body, _ := do(boss, "GET", "/t/"+key, nil); strings.Contains(body, "/delegate") {
		t.Fatal("delegate offered to the creator who is not the responsible")
	}

	form, _ := do(boss, "GET", "/tasks/new?copy="+key, nil)
	for _, want := range []string{`value="Собрать отчёт"`, "Квартальный", "Цифры\nГрафики", `name="copy" value="` + key + `"`, `value="AA" selected`} {
		if !strings.Contains(form, want) {
			t.Fatalf("copy form lacks %q", want)
		}
	}
	if form, _ := do(outsider, "GET", "/tasks/new?copy="+key, nil); strings.Contains(form, "Собрать отчёт") {
		t.Fatal("the copy form leaks a task the person cannot see")
	}
	_, loc := do(boss, "POST", "/tasks/new", url.Values{"project": {"AA"}, "title": {"Собрать отчёт"}, "responsible": {anna},
		"priority": {"2"}, "checklist": {"Цифры\nГрафики"}, "copy": {key}, "result_required": {"on"}, "dates_from_subtasks": {"on"}})
	if !strings.HasPrefix(loc, "/t/AA-") || loc == "/t/"+key {
		t.Fatalf("copy not created: %q", loc)
	}
	copyID, err := ts.TaskIDByKey(ctx, strings.TrimPrefix(loc, "/t/"))
	must(err)
	cp, err := ts.GetCard(ctx, boss, copyID)
	must(err)
	one := []tasks.Task{cp.Task}
	must(ts.FillMarks(ctx, "", one))
	if !slices.Contains(one[0].Tags, "отчёты") || len(cp.Checklist) != 2 {
		t.Fatalf("copy: tags %v, checklist %d", one[0].Tags, len(cp.Checklist))
	}
	// the task parameters from the form: boxes in the card's settings, kept by the other forms of the card
	if cp.Params != (tasks.Params{DatesFromSubtasks: true, ResultRequired: true}) {
		t.Fatalf("copy: parameters %+v", cp.Params)
	}
	card, _ := do(boss, "GET", loc, nil)
	for _, want := range []string{`name="result_required" checked`, `name="auto_complete" >`, `name="dates_from_subtasks" value="on"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("the card lacks %q", want)
		}
	}

	// delegating: only the responsible; the newcomer leaves the auditors, the former responsible joins them
	do(boss, "POST", "/t/"+key+"/delegate", url.Values{"to": {petr}})
	if c, _ := ts.GetCard(ctx, boss, id); c.ResponsibleID != anna {
		t.Fatal("the creator delegated a task of someone else")
	}
	do(anna, "POST", "/t/"+key+"/delegate", url.Values{"to": {petr}})
	c, err := ts.GetCard(ctx, boss, id)
	must(err)
	var auditors []string
	for _, u := range c.Auditors {
		auditors = append(auditors, u.ID)
	}
	if c.ResponsibleID != petr || !slices.Equal(auditors, []string{anna}) {
		t.Fatalf("after delegating: responsible %s, auditors %v", c.ResponsibleID, auditors)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'responsible' AND task_id = $2`, petr, id).Scan(&n))
	if n != 1 {
		t.Fatalf("the new responsible got %d notifications", n)
	}
	// to the creator: they do not become an auditor of their own task when it comes back
	do(petr, "POST", "/t/"+key+"/delegate", url.Values{"to": {boss}})
	c, err = ts.GetCard(ctx, boss, id)
	must(err)
	if c.ResponsibleID != boss || len(c.Auditors) != 2 {
		t.Fatalf("delegated to the creator: responsible %s, auditors %d", c.ResponsibleID, len(c.Auditors))
	}
	// a completed task is not delegated
	must(ts.ApplyAction(ctx, boss, id, tasks.ActionComplete))
	if body, _ := do(boss, "GET", "/t/"+key, nil); strings.Contains(body, "/delegate") {
		t.Fatal("delegate offered on a completed task")
	}
	must(func() error {
		if err := ts.Delegate(ctx, boss, id, anna); err == nil {
			return io.ErrUnexpectedEOF
		}
		return nil
	}())
}
