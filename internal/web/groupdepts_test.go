package web

import (
	"bytes"
	"context"
	"encoding/json"
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

	"go24/internal/apikeys"
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

// TestGroupDepts: departments of a group (GO24DEV-833) — the owner adds a department; its people and those of its
// sub-departments (with the head) become members by themselves, newcomers too, those who left go out; those added by
// hand stay, and one a department brought is not taken out by hand; REST reads and replaces the departments.
func TestGroupDepts(t *testing.T) {
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
	boss, anna, ivan, oleg, vera := newUser("Борис"), newUser("Анна"), newUser("Иван"), newUser("Олег"), newUser("Вера")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, boss)
	must(err)
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, admin bool, method, path string, form url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Admin: admin, AdminMode: admin}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	var root, sales, opt string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, root, vera).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, sales).Scan(&opt))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, ivan, sales, anna, opt)
	must(err)
	if st, _ := do(boss, false, "POST", "/projects", url.Values{"key": {"GD"}, "name": {"Группа"}, "privacy": {"open"}}); st != http.StatusSeeOther {
		t.Fatalf("create: %d", st)
	}
	var gid string
	must(pool.QueryRow(ctx, `SELECT id FROM projects WHERE key = 'GD'`).Scan(&gid))
	_, err = pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')`, gid, oleg)
	must(err)
	members := func() string {
		t.Helper()
		var s string
		must(pool.QueryRow(ctx, `SELECT coalesce(string_agg(u.name || ':' || m.role ||
				CASE WHEN EXISTS (SELECT 1 FROM project_department_people b WHERE b.project_id = m.project_id AND b.user_id = m.user_id) THEN '+' ELSE '' END,
				',' ORDER BY u.name), '') FROM project_members m JOIN users u ON u.id = m.user_id WHERE m.project_id = $1`, gid).Scan(&s))
		return s
	}

	if st, _ := do(oleg, false, "POST", "/p/GD/departments", url.Values{"depts": {"dept:" + sales}}); st == http.StatusSeeOther {
		t.Fatal("a member adds a department")
	}
	do(boss, false, "POST", "/p/GD/departments", url.Values{"depts": {ivan}})
	if got := members(); got != "Борис:owner,Олег:member" {
		t.Fatalf("a person saved as a department: %s", got)
	}
	if st, _ := do(boss, false, "POST", "/p/GD/departments", url.Values{"depts": {"dept:" + sales}}); st != http.StatusSeeOther {
		t.Fatalf("add a department: %d", st)
	}
	if got := members(); got != "Анна:member+,Борис:owner,Вера:member+,Иван:member+,Олег:member" {
		t.Fatalf("members with the department: %s", got)
	}
	st, body := do(boss, false, "GET", "/p/GD", nil)
	if st != http.StatusOK || !strings.Contains(body, `action="/p/GD/departments"`) || !strings.Contains(body, "из отдела") || !strings.Contains(body, "Продажи") ||
		strings.Contains(body, `action="/p/GD/members/`+ivan+`/delete"`) || !strings.Contains(body, `action="/p/GD/members/`+oleg+`/delete"`) {
		t.Fatalf("group page: %d %s", st, body)
	}
	if _, body := do(oleg, false, "GET", "/p/GD", nil); strings.Contains(body, `action="/p/GD/departments"`) || !strings.Contains(body, "Отделы в группе: Продажи") {
		t.Fatalf("a member's page: %s", body)
	}
	if do(boss, false, "POST", "/p/GD/members/"+ivan+"/delete", nil); !strings.Contains(members(), "Иван") {
		t.Fatal("one a department brought taken out by hand")
	}

	// the structure changes: Ivan leaves, a newcomer comes into a sub-department, Anna is dismissed, Oleg (by hand) comes
	// and goes, Vera, made a moderator, stops being the head
	nina := newUser("Нина")
	_, err = pool.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, ivan)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $2)`, nina, opt, oleg)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, anna)
	must(err)
	srv.syncGroupDepts(ctx, "")
	_, err = pool.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, oleg)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE project_members SET role = 'manager' WHERE project_id = $1 AND user_id = $2`, gid, vera)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE departments SET head_id = NULL WHERE id = $1`, sales)
	must(err)
	srv.syncDeptChats(ctx) // the structure's sync brings the groups along
	if got := members(); got != "Борис:owner,Вера:manager,Нина:member+,Олег:member" {
		t.Fatalf("members after the structure changed: %s", got)
	}

	// REST
	key := func(who string) string {
		k, err := keys.Create(ctx, who, "k", apikeys.AllScopes, time.Hour)
		must(err)
		return k
	}
	bossKey, olegKey := key(boss), key(oleg)
	call := func(method, path, token string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if st, body := call("GET", "/api/v1/projects/GD/departments", olegKey, nil); st != http.StatusOK || !strings.Contains(body, sales) || !strings.Contains(body, "Продажи") {
		t.Fatalf("departments by REST: %d %s", st, body)
	}
	if st, _ := call("PUT", "/api/v1/projects/GD/departments", olegKey, map[string]any{"departments": []string{opt}}); st != http.StatusForbidden {
		t.Fatalf("a member by REST: %d", st)
	}
	if st, _ := call("PUT", "/api/v1/projects/GD/departments", bossKey, map[string]any{"departments": []string{"00000000-0000-0000-0000-000000000000"}}); st != http.StatusUnprocessableEntity {
		t.Fatalf("an unknown department: %d", st)
	}
	if st, body := call("PUT", "/api/v1/projects/GD/departments", bossKey, map[string]any{"departments": []string{}}); st != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("no departments: %d %s", st, body)
	}
	if got := members(); got != "Борис:owner,Вера:manager,Олег:member" {
		t.Fatalf("members without departments: %s", got)
	}
}
