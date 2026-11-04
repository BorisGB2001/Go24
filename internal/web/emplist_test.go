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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestEmployeeGrid: «Сотрудники» grid — the columns one chooses are kept for them (back to the default
// ones), the headers sort; the people ticked get a group chat with the viewer (anyone), are moved into a department or
// added to one as the structure's «Права доступа» let one by one, dismissed and returned by administrators only.
func TestEmployeeGrid(t *testing.T) {
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
	newUser := func(name, position string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position, city) VALUES ($1, $1, $1 || '@t', $2, 'Город-' || $1)
			RETURNING id`, name, position).Scan(&id))
		return id
	}
	admin, head, ann, bob, plain := newUser("Админов", "Директор"), newUser("Главов", "Начальник"), newUser("Анна", "Юрист"),
		newUser("Борис", "Аналитик"), newUser("Простов", "Бухгалтер")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	_, all, err := cs.Tree(ctx)
	must(err)
	root := all[0].ID
	deptA, err := cs.CreateDepartment(ctx, root, "Отдел А", head)
	must(err)
	deptA1, err := cs.CreateDepartment(ctx, deptA, "Группа А1", "")
	must(err)
	deptB, err := cs.CreateDepartment(ctx, root, "Отдел Б", "")
	must(err)
	must(cs.SetDepartments(ctx, admin, ann, []string{deptA}))
	must(cs.SetDepartments(ctx, admin, bob, []string{deptB}))
	must(cs.SetStructRights(ctx, company.StructRights{"head": {"staff": "own"}}))
	deptsOf := func(id string) []string {
		p, err := cs.Profile(ctx, id)
		must(err)
		var out []string
		for _, d := range p.Departments {
			out = append(out, d.ID)
		}
		slices.Sort(out)
		return out
	}

	bundle, err := i18n.Load("")
	must(err)
	chats := &fakeChats{owner: map[string]string{}, members: map[string][]string{}, names: map[string]string{}, topics: map[string]string{}}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, Company: cs, Notify: notify.NewStore(pool), Chats: chats,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, head, plain} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: map[string]string{admin: "Админов", head: "Главов", plain: "Простов"}[u], Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}
	bulk := func(who, action, dept string, ids ...string) (int, string) {
		t.Helper()
		st, _, loc := do(who, "POST", "/company/bulk", url.Values{"action": {action}, "dept": {dept}, "id": ids, "back": {"/company?q=x"}})
		return st, loc
	}

	// columns: the default ones, then the viewer's own — kept for them only, in the grid's order; reset
	_, body, _ := do(plain, "GET", "/company", nil)
	if !strings.Contains(body, ">E-mail") || strings.Contains(body, ">Город") || !strings.Contains(body, `value="chat"`) ||
		strings.Contains(body, `value="move"`) || strings.Contains(body, `value="fire"`) {
		t.Fatal("default columns and actions of an employee")
	}
	if st, _, loc := do(plain, "POST", "/company/columns", url.Values{"col": {"birthday", "city", "bogus"}, "back": {"https://evil/"}}); st != http.StatusSeeOther || loc != "/company" {
		t.Fatalf("columns: %d %s", st, loc)
	}
	if cols, _ := cs.Columns(ctx, plain); !slices.Equal(cols, []string{"city", "birthday"}) {
		t.Fatalf("kept columns: %v", cols)
	}
	_, body, _ = do(plain, "GET", "/company", nil)
	if strings.Contains(body, ">E-mail") || !strings.Contains(body, ">Город") || !strings.Contains(body, "Город-Анна") {
		t.Fatal("own columns not shown")
	}
	if _, body, _ = do(admin, "GET", "/company", nil); !strings.Contains(body, ">E-mail") {
		t.Fatal("another's columns changed")
	}
	do(plain, "POST", "/company/columns", url.Values{"col": {"city"}, "reset": {"1"}})
	if cols, _ := cs.Columns(ctx, plain); !slices.Equal(cols, company.DefaultColumns) {
		t.Fatalf("reset columns: %v", cols)
	}

	// sorting by the position, backwards; the header links keep the filter
	_, body, _ = do(plain, "GET", "/company?sort=position&desc=1", nil)
	if i, j := strings.Index(body, "Анна</b>"), strings.Index(body, "Борис</b>"); i < 0 || j < 0 || i > j {
		t.Fatal("not sorted by position backwards") // Юрист before Аналитик
	}
	if !strings.Contains(body, "sort=position") || !strings.Contains(body, "↓") {
		t.Fatal("the sorted header")
	}

	// a chat: the viewer and the people ticked, the viewer once
	if st, loc := bulk(plain, "chat", "", ann, bob, plain); st != http.StatusSeeOther || !strings.HasPrefix(loc, "/im?room=") {
		t.Fatalf("chat: %d %s", st, loc)
	}
	var first string
	for r, o := range chats.owner {
		if o == plain && len(chats.members[r]) == 3 {
			first = r
		}
	}
	if first == "" || !slices.Equal(chats.members[first], []string{plain, ann, bob}) || chats.names[first] != "Простов, Анна, Борис" {
		t.Fatalf("chat members: %v %v", chats.members, chats.names)
	}
	if st, loc := bulk(plain, "chat", "", plain); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("chat with nobody: %d %s", st, loc)
	}

	// departments: an employee may not; a head — in the own department with its sub-departments only
	if st, _ := bulk(plain, "move", deptA1, ann); st != http.StatusForbidden || !slices.Equal(deptsOf(ann), []string{deptA}) {
		t.Fatalf("an employee moves: %d", st)
	}
	if st, _ := bulk(head, "move", deptB, ann); st != http.StatusForbidden || !slices.Equal(deptsOf(ann), []string{deptA}) {
		t.Fatalf("a head moves out of theirs: %d", st)
	}
	if st, _ := bulk(head, "add", deptA, bob); st != http.StatusForbidden || !slices.Equal(deptsOf(bob), []string{deptB}) {
		t.Fatalf("a head takes a stranger: %d", st)
	}
	if st, loc := bulk(head, "move", deptA1, ann); st != http.StatusSeeOther || loc != "/company?q=x" || !slices.Equal(deptsOf(ann), []string{deptA1}) {
		t.Fatalf("a head moves within theirs: %d %s %v", st, loc, deptsOf(ann))
	}
	if st, loc := bulk(admin, "move", "", ann); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("no department: %d %s", st, loc)
	}
	if _, body, _ = do(head, "GET", "/company", nil); !strings.Contains(body, `value="move"`) || strings.Contains(body, `value="fire"`) {
		t.Fatal("the actions of a head")
	}
	want := []string{deptA, deptB}
	slices.Sort(want)
	if st, _ := bulk(admin, "add", deptA, bob, ann); st != http.StatusSeeOther || !slices.Equal(deptsOf(bob), want) {
		t.Fatalf("an administrator adds: %d %v", st, deptsOf(bob))
	}
	if st, _ := bulk(admin, "move", deptB, bob); st != http.StatusSeeOther || !slices.Equal(deptsOf(bob), []string{deptB}) {
		t.Fatalf("an administrator moves: %d %v", st, deptsOf(bob))
	}

	// dismissing: administrators only; not oneself — the ones before still go
	if st, _ := bulk(head, "fire", "", ann); st != http.StatusForbidden {
		t.Fatalf("a head dismisses: %d", st)
	}
	active := func(id string) bool {
		p, err := cs.Profile(ctx, id)
		must(err)
		return p.Active
	}
	if st, loc := bulk(admin, "fire", "", ann, bob, admin); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") || active(ann) || active(bob) || !active(admin) {
		t.Fatalf("dismiss: %d %s", st, loc)
	}
	if _, body, _ = do(admin, "GET", "/company?fired=1", nil); !strings.Contains(body, `value="return"`) || strings.Contains(body, `value="chat"`) ||
		strings.Contains(body, `value="move"`) || !strings.Contains(body, "Анна</b>") {
		t.Fatal("the actions of the dismissed")
	}
	if st, _ := bulk(plain, "chat", "", ann); st != http.StatusSeeOther || active(ann) {
		t.Fatal("a chat with the dismissed")
	}
	if st, _ := bulk(admin, "return", "", ann, bob); st != http.StatusSeeOther || !active(ann) || !active(bob) {
		t.Fatalf("return: %d", st)
	}
}
