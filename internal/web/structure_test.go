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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestStructureDrag: «Структура компании» has the search for everyone and the drags for administrators; a person
// dragged onto another department and a department under another one answer 204, a refused drag {"error"}, and
// those who are not administrators get 403.
func TestStructureDrag(t *testing.T) {
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
	admin, plain := newUser("Админов"), newUser("Простов")
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Ромашка"))
	root, _, err := cs.Tree(ctx)
	must(err)
	must(cs.AddDepartment(ctx, root.ID, "ИТ", ""))
	must(cs.AddDepartment(ctx, root.ID, "Продажи", ""))
	_, flat, err := cs.Tree(ctx)
	must(err)
	dept := map[string]string{}
	for _, d := range flat {
		dept[d.Name] = d.ID
	}
	must(cs.SetDepartments(ctx, "", plain, []string{dept["ИТ"]}))

	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Company: cs,
		BaseURL:  "http://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, plain} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		req.AddCookie(cookie[who])
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	st, body := do(admin, "GET", "/company/structure", nil)
	if st != http.StatusOK || !strings.Contains(body, `class="structfind"`) || !strings.Contains(body, "data-struct-edit") ||
		!strings.Contains(body, `data-dept="`+dept["ИТ"]+`"`) || !strings.Contains(body, "js/structure.js") {
		t.Fatalf("structure for the administrator: %d", st)
	}
	if _, body = do(plain, "GET", "/company/structure", nil); !strings.Contains(body, `class="structfind"`) || strings.Contains(body, "data-struct-edit") {
		t.Fatal("structure for an employee: the search, no drags")
	}

	moveUser := url.Values{"op": {"move_user"}, "user": {plain}, "from": {dept["ИТ"]}, "to": {dept["Продажи"]}}
	if st, _ = do(plain, "POST", "/company/departments", moveUser); st != http.StatusForbidden {
		t.Fatalf("an employee drags: %d", st)
	}
	if st, body = do(admin, "POST", "/company/departments", moveUser); st != http.StatusNoContent {
		t.Fatalf("the person dragged: %d %s", st, body)
	}
	if p, _ := cs.Profile(ctx, plain); len(p.Departments) != 1 || p.Departments[0].ID != dept["Продажи"] {
		t.Fatalf("after the drag = %+v", p.Departments)
	}
	if st, body = do(admin, "POST", "/company/departments", moveUser); st != http.StatusNotFound || !strings.Contains(body, `"error"`) {
		t.Fatalf("dragged again from where they are not: %d %s", st, body)
	}
	if st, _ = do(admin, "POST", "/company/departments", url.Values{"op": {"move"}, "id": {dept["Продажи"]}, "parent": {dept["ИТ"]}}); st != http.StatusNoContent {
		t.Fatalf("the department dragged: %d", st)
	}
	if st, body = do(admin, "POST", "/company/departments", url.Values{"op": {"move"}, "id": {dept["ИТ"]}, "parent": {dept["Продажи"]}}); st != http.StatusUnprocessableEntity || !strings.Contains(body, "err.department_cycle") {
		t.Fatalf("under its own sub-department: %d %s", st, body)
	}
	_, flat, _ = cs.Tree(ctx)
	for _, d := range flat {
		if d.Name == "Продажи" && (d.ParentID != dept["ИТ"] || d.Depth != 2) {
			t.Fatalf("moved department = %+v", d)
		}
	}
}
