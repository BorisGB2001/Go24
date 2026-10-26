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

// TestChecklistRoutes: the card shows every checklist with its name; lists are added, renamed and deleted, items
// pasted as a list, edited, marked important and dragged; «Сделать задачей» opens the subtask form with the text.
func TestChecklistRoutes(t *testing.T) {
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
	boss, anna, petr, outsider := newUser("Борис"), newUser("Анна"), newUser("Пётр Иванов"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	must(ts.SetMember(ctx, p, petr, "member"))
	key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Собрать отчёт", ResponsibleID: anna,
		Priority: 1, Checklist: []string{"Цифры", "# Оформление", "Графики"}})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
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

	card := func() tasks.TaskCard {
		t.Helper()
		c, err := ts.GetCard(ctx, boss, id)
		must(err)
		return c
	}
	page, _ := do(anna, "GET", "/t/"+key, nil)
	for _, want := range []string{`placeholder="Чек-лист 1"`, `value="Оформление"`, "Графики", `data-list="`, "Добавить чек-лист"} {
		if !strings.Contains(page, want) {
			t.Fatalf("card lacks %q", want)
		}
	}
	if page, _ := do(outsider, "GET", "/t/"+key, nil); strings.Contains(page, "Графики") {
		t.Fatal("a stranger sees the checklist")
	}

	c := card()
	first, second := c.Checklists[0].ID, c.Checklists[1].ID
	if _, loc := do(anna, "POST", "/t/"+key+"/checklists", url.Values{}); loc != "/t/"+key+"#checklist" {
		t.Fatalf("add list: %q", loc)
	}
	_, _ = do(anna, "POST", "/t/"+key+"/checklists/"+second, url.Values{"title": {"Вёрстка"}})
	_, _ = do(anna, "POST", "/t/"+key+"/checklist", url.Values{"list": {first}, "title": {"Итоги\nВыводы @Пётр Иванов"}})
	c = card()
	if len(c.Checklists) != 3 || c.Checklists[1].Title != "Вёрстка" || len(c.Checklists[0].Items) != 3 || len(c.Accomplices) != 1 {
		t.Fatalf("after adding: %+v, accomplices %d", c.Checklists, len(c.Accomplices))
	}
	item := c.Checklists[0].Items[2].ID // «Выводы @Пётр Иванов»
	_, _ = do(anna, "POST", "/t/"+key+"/checklist/"+item, url.Values{"op": {"edit"}, "title": {"Выводы"}})
	_, _ = do(anna, "POST", "/t/"+key+"/checklist/"+item, url.Values{"op": {"important"}, "on": {"1"}})
	_, _ = do(anna, "POST", "/t/"+key+"/checklist/"+item, url.Values{"op": {"move"}, "list": {second}})
	_, _ = do(outsider, "POST", "/t/"+key+"/checklist/"+item, url.Values{"op": {"delete"}})
	c = card()
	moved := c.Checklists[1].Items
	if len(moved) != 2 || moved[1].Title != "Выводы" || !moved[1].Important {
		t.Fatalf("edit, mark, move: %+v", moved)
	}
	if page, _ := do(anna, "GET", "/t/"+key, nil); !strings.Contains(page, `ck-item important`) {
		t.Fatal("an important item is not marked on the card")
	}
	if _, loc := do(anna, "POST", "/t/"+key+"/checklist/"+item, url.Values{"op": {"task"}, "title": {"Выводы"}}); loc !=
		"/tasks/new?parent="+key+"&title=%D0%92%D1%8B%D0%B2%D0%BE%D0%B4%D1%8B" {
		t.Fatalf("to task: %q", loc)
	}
	if form, _ := do(anna, "GET", "/tasks/new?parent="+key+"&title=Выводы", nil); !strings.Contains(form, `value="Выводы"`) {
		t.Fatal("the new task form lacks the item's text")
	}
	_, _ = do(anna, "POST", "/t/"+key+"/checklists/"+second, url.Values{"op": {"delete"}})
	if c = card(); len(c.Checklists) != 2 || len(c.Checklist) != 2 {
		t.Fatalf("after deleting a list: %+v", c.Checklists)
	}
	// «Копировать» keeps the lists in the line form
	if form, _ := do(boss, "GET", "/tasks/new?copy="+key, nil); !strings.Contains(form, "Цифры\nИтоги\n# ") {
		t.Fatal("copy form lacks the lists")
	}
}
