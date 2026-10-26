package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

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

// TestTaskListMenu: task lists — a click on a header sorts by the column (kept for the user), ☰ of a
// row opens the menu of what the user may do with the task, a click on the deadline its editor; the forms of the
// menu return to the list.
func TestTaskListMenu(t *testing.T) {
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
	boss, anna, outsider := newUser("Борис"), newUser("Анна"), newUser("Чужой")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "LM", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "LM")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	soon, later := time.Now().Add(24*time.Hour), time.Now().Add(72*time.Hour)
	beta, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Бета", ResponsibleID: anna, Deadline: &soon})
	must(err)
	alpha, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Альфа", ResponsibleID: anna, Deadline: &later})
	must(err)

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
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
		return res.StatusCode, string(b), res.Header.Get("Location")
	}
	order := func(page string) bool { // «Альфа» before «Бета»
		t.Helper()
		a, b := strings.Index(page, ">Альфа<"), strings.Index(page, ">Бета<")
		if a < 0 || b < 0 {
			t.Fatalf("the list has not both tasks")
		}
		return a < b
	}

	// the usual order is by the deadline; the headers sort, each row has its menu and its deadline editor
	_, list, _ := do(boss, "GET", "/tasks", nil)
	if order(list) {
		t.Fatal("the usual order is not by the deadline")
	}
	for _, want := range []string{`name="sort" value="title"`, `name="sort" value="-activity"`, `data-lazy="/t/` + beta + `/rowmenu?back=%2Ftasks"`,
		`data-lazy="/t/` + beta + `/rowmenu?part=deadline&back=%2Ftasks"`} {
		if !strings.Contains(list, want) {
			t.Fatalf("the list has no %s", want)
		}
	}
	if strings.Contains(list, "/prefs/sort\"><input type=\"hidden\" name=\"back\"") {
		t.Fatal("«Сортировка по умолчанию» without a sort")
	}

	// a click on «Название» sorts by it, for this user only, on every list; the next click — the other way round
	if code, _, loc := do(boss, "POST", "/prefs/sort", url.Values{"sort": {"title"}, "back": {"/tasks?all=1"}}); code != http.StatusSeeOther || loc != "/tasks?all=1" {
		t.Fatalf("sort: %d %s", code, loc)
	}
	_, list, _ = do(boss, "GET", "/tasks", nil)
	if !order(list) || !strings.Contains(list, `name="sort" value="-title"`) || !strings.Contains(list, "i-arrow-up") {
		t.Fatal("the list is not sorted by the title")
	}
	if _, my, _ := do(boss, "GET", "/my?role=creator", nil); !order(my) {
		t.Fatal("«Мои задачи» are not sorted by the title")
	}
	if _, other, _ := do(anna, "GET", "/tasks", nil); order(other) {
		t.Fatal("another user's list is sorted too")
	}
	do(boss, "POST", "/prefs/sort", url.Values{"sort": {"-title"}, "back": {"/tasks"}})
	if _, list, _ = do(boss, "GET", "/tasks", nil); order(list) || !strings.Contains(list, "i-arrow-down") {
		t.Fatal("the list is not sorted by the title backwards")
	}
	do(boss, "POST", "/prefs/sort", url.Values{"sort": {"-password"}, "back": {"/tasks"}})
	if v, _ := ts.Sort(ctx, boss); v != "-title" {
		t.Fatalf("an unknown sort was kept: %q", v)
	}
	do(boss, "POST", "/prefs/sort", url.Values{"sort": {""}, "back": {"/tasks"}})
	if _, list, _ = do(boss, "GET", "/tasks", nil); order(list) {
		t.Fatal("«Сортировка по умолчанию» did not bring the usual order back")
	}

	// the menu of a row: the responsible starts the task and delegates it, may not delete it nor move the deadline
	_, menu, _ := do(anna, "GET", "/t/"+beta+"/rowmenu?back=/my", nil)
	for _, want := range []string{`action="/t/` + beta + `/action"`, `value="start"`, `href="/t/` + beta + `#delegate"`, `href="/tasks/new?copy=` + beta + `"`,
		`action="/t/` + beta + `/mark"`, `name="back" value="/my"`} {
		if !strings.Contains(menu, want) {
			t.Fatalf("anna's menu has no %s:\n%s", want, menu)
		}
	}
	if strings.Contains(menu, "/delete") {
		t.Fatal("the responsible may delete the task")
	}
	if _, dl, _ := do(anna, "GET", "/t/"+beta+"/rowmenu?part=deadline&back=/my", nil); strings.Contains(dl, "datetime-local") {
		t.Fatal("the responsible may move the deadline")
	}
	// the creator moves the deadline and deletes the task from the list
	_, dl, _ := do(boss, "GET", "/t/"+beta+"/rowmenu?part=deadline&back=/tasks", nil)
	if !strings.Contains(dl, `type="datetime-local"`) || !strings.Contains(dl, `name="op" value="clear"`) {
		t.Fatalf("the creator's deadline editor:\n%s", dl)
	}
	if _, _, loc := do(boss, "POST", "/t/"+beta+"/deadline", url.Values{"op": {"clear"}, "back": {"/tasks"}}); loc != "/tasks" {
		t.Fatalf("the deadline editor returns to %q", loc)
	}
	betaID, err := ts.TaskIDByKey(ctx, beta)
	must(err)
	if task, err := ts.TaskByID(ctx, betaID); err != nil || task.Deadline != nil {
		t.Fatalf("the deadline is not cleared: %v", err)
	}
	_, menu, _ = do(boss, "GET", "/t/"+alpha+"/rowmenu?back=/tasks", nil)
	if !strings.Contains(menu, `action="/t/`+alpha+`/delete"`) || !strings.Contains(menu, `href="/tasks/new?parent=`+alpha+`"`) {
		t.Fatalf("the creator's menu:\n%s", menu)
	}
	if _, _, loc := do(boss, "POST", "/t/"+alpha+"/delete", url.Values{"back": {"/tasks?all=1"}}); !strings.HasPrefix(loc, "/tasks?all=1&msg=") {
		t.Fatalf("deleting from the list returns to %q", loc)
	}
	// someone outside the project gets no menu
	if code, menu, _ := do(outsider, "GET", "/t/"+beta+"/rowmenu", nil); code < 400 || strings.Contains(menu, "/action") {
		t.Fatalf("an outsider's menu: %d", code)
	}
	// pages grids: «На странице» is kept for the user and the list starts from its first page again;
	// the footer counts all the tasks, the rows show their checklist and files
	must(ts.AddChecklistItem(ctx, boss, betaID, "Шаг"))
	for i := range 11 {
		_, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: fmt.Sprintf("Задача %02d", i), ResponsibleID: anna})
		must(err)
	}
	do(boss, "POST", "/prefs/sort", url.Values{"sort": {"title"}, "back": {"/tasks"}})
	if n, _ := ts.PageSize(ctx, boss); n != tasks.DefaultPageSize {
		t.Fatalf("the first page size is %d", n)
	}
	if _, _, loc := do(boss, "POST", "/prefs/pagesize", url.Values{"size": {"10"}, "back": {"/tasks?all=1&page=3"}}); loc != "/tasks?all=1" {
		t.Fatalf("«На странице» returns to %q", loc)
	}
	do(boss, "POST", "/prefs/pagesize", url.Values{"size": {"7"}, "back": {"/tasks"}})
	if n, _ := ts.PageSize(ctx, boss); n != 10 {
		t.Fatalf("an unknown page size was kept: %d", n)
	}
	_, list, _ = do(boss, "GET", "/tasks", nil)
	for _, want := range []string{`data-bulk-picked>0</b> / 12`, `<b class="on">1</b>`, `href="/tasks?page=2"`, `<option selected>10</option>`,
		`href="/t/` + beta + `#checklist" class="subcnt"`, ">Задача 08<"} {
		if !strings.Contains(list, want) {
			t.Fatalf("the first page has no %s", want)
		}
	}
	if strings.Contains(list, ">Задача 09<") {
		t.Fatal("the first page has 11 tasks")
	}
	_, list, _ = do(boss, "GET", "/tasks?page=2", nil)
	if !strings.Contains(list, ">Задача 10<") || strings.Contains(list, ">Бета<") || !strings.Contains(list, `<b class="on">2</b>`) {
		t.Fatal("the second page")
	}
	if _, list, _ = do(boss, "GET", "/tasks?page=9", nil); !strings.Contains(list, `<b class="on">2</b>`) {
		t.Fatal("a page past the end does not show the last one")
	}
	if _, other, _ := do(anna, "GET", "/my", nil); !strings.Contains(other, `<option selected>50</option>`) || strings.Contains(other, `class="pager"`) {
		t.Fatal("another user's list has the page size too")
	}
}
