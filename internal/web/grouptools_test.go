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

// TestGroupTools: «Возможности группы» (GO24DEV-824) — the group's tools are tabs of its page; the owner and
// moderators switch them off and name them, an ordinary member does not; a switched-off tool leaves the tabs and does
// not open (no new tasks, no drive, no calendar of the group); a collab keeps its own set; REST reads and changes them.
func TestGroupTools(t *testing.T) {
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
	if st, _ := do(boss, false, "POST", "/projects", url.Values{"key": {"GT"}, "name": {"Группа"}, "privacy": {"open"}}); st != http.StatusSeeOther {
		t.Fatalf("create: %d", st)
	}
	var gid string
	must(pool.QueryRow(ctx, `SELECT id FROM projects WHERE key = 'GT'`).Scan(&gid))
	_, err = pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')`, gid, anna)
	must(err)

	// all tools on: the tabs of the group, the form for its owner only
	st, body := do(boss, false, "GET", "/p/GT", nil)
	if st != http.StatusOK || !strings.Contains(body, `href="/disk/p/GT"`) || !strings.Contains(body, `href="/calendar?project=GT"`) ||
		!strings.Contains(body, `href="/feed?project=`+gid+`"`) || !strings.Contains(body, `action="/p/GT/tools"`) {
		t.Fatalf("group page: %d %s", st, body)
	}
	if _, body := do(anna, false, "GET", "/p/GT", nil); strings.Contains(body, `action="/p/GT/tools"`) {
		t.Fatal("a member sees the features form")
	}
	if st, _ := do(anna, false, "POST", "/p/GT/tools", url.Values{"tool_feed": {"on"}}); st == http.StatusSeeOther {
		t.Fatal("a member changes the features")
	}

	// the owner switches off tasks and the drive, renames the calendar
	if st, _ := do(boss, false, "POST", "/p/GT/tools", url.Values{"tool_feed": {"on"}, "tool_calendar": {"on"}, "tool_chat": {"on"},
		"name_calendar": {"  Встречи  "}}); st != http.StatusSeeOther {
		t.Fatalf("save: %d", st)
	}
	var off []string
	var names map[string]string
	must(pool.QueryRow(ctx, `SELECT tools_off, tool_names FROM projects WHERE id = $1`, gid).Scan(&off, &names))
	if strings.Join(off, ",") != "drive,tasks" || names["calendar"] != "Встречи" || len(names) != 1 {
		t.Fatalf("saved: %q %v", off, names)
	}
	st, body = do(anna, false, "GET", "/p/GT", nil)
	if st != http.StatusOK || strings.Contains(body, `href="/disk/p/GT"`) || !strings.Contains(body, "Встречи") ||
		strings.Contains(body, `/tasks/new?project=GT`) || !strings.Contains(body, "Задачи в этой группе выключены") {
		t.Fatalf("group page with tools off: %d %s", st, body)
	}
	if st, _ := do(anna, false, "POST", "/p/GT/tasks", url.Values{"title": {"Нельзя"}}); st == http.StatusSeeOther {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, gid).Scan(&n))
		if n != 0 {
			t.Fatal("a task made in a group with tasks off")
		}
	}
	p, err := ts.ProjectByKey(ctx, anna, "GT")
	must(err)
	if _, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Нельзя"}); err != tasks.ErrToolOff {
		t.Fatalf("store makes a task: %v", err)
	}
	if st, _ := do(anna, false, "GET", "/disk/p/GT", nil); st == http.StatusOK {
		t.Fatal("the drive of the group opens")
	}

	// the calendar off too: it does not open; on again with its usual name
	do(boss, false, "POST", "/p/GT/tools", url.Values{"tool_feed": {"on"}, "tool_chat": {"on"}, "name_calendar": {"Встречи"}})
	if st, _ := do(anna, false, "GET", "/calendar?project=GT", nil); st == http.StatusOK {
		t.Fatal("the calendar of the group opens")
	}
	do(boss, false, "POST", "/p/GT/tools", url.Values{"tool_feed": {"on"}, "tool_tasks": {"on"}, "tool_calendar": {"on"}, "tool_drive": {"on"}, "tool_chat": {"on"}})
	if st, body := do(anna, false, "GET", "/p/GT", nil); st != http.StatusOK || !strings.Contains(body, `href="/disk/p/GT"`) || strings.Contains(body, "Встречи") {
		t.Fatalf("all on again: %d %s", st, body)
	}
	if st, _ := do(anna, false, "POST", "/p/GT/tasks", url.Values{"title": {"Можно"}}); st != http.StatusSeeOther {
		t.Fatalf("task in the group: %d", st)
	}
	if st, _ := do(anna, false, "GET", "/disk/p/GT", nil); st != http.StatusOK {
		t.Fatalf("drive: %d", st)
	}
	do(boss, false, "POST", "/p/GT/tools", url.Values{"tool_feed": {"on"}, "name_feed": {strings.Repeat("я", 51)}}) // back with a message
	must(pool.QueryRow(ctx, `SELECT tools_off, tool_names FROM projects WHERE id = $1`, gid).Scan(&off, &names))
	if len(off) != 0 || len(names) != 0 {
		t.Fatalf("a long name saved: %q %v", off, names)
	}

	// REST
	key := func(who string) string {
		k, err := keys.Create(ctx, who, "k", apikeys.AllScopes, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey := key(boss), key(anna)
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
	if st, _ := call("PATCH", "/api/v1/projects/GT/tools", annaKey, []map[string]any{{"id": "chat", "on": false}}); st != http.StatusForbidden {
		t.Fatalf("a member by REST: %d", st)
	}
	if st, body := call("PATCH", "/api/v1/projects/GT/tools", bossKey, []map[string]any{{"id": "chat", "on": false, "name": "Обсуждение"}}); st != http.StatusOK ||
		!strings.Contains(body, `"id": "chat"`) || !strings.Contains(body, `"name": "Обсуждение"`) || !strings.Contains(body, `"on": false`) {
		t.Fatalf("patch: %d %s", st, body)
	}
	if st, _ := call("PATCH", "/api/v1/projects/GT/tools", bossKey, []map[string]any{{"id": "wiki", "on": true}}); st != http.StatusUnprocessableEntity {
		t.Fatalf("unknown tool: %d", st)
	}
	if st, body := call("GET", "/api/v1/projects/GT", annaKey, nil); st != http.StatusOK || !strings.Contains(body, `"tools": [`) || !strings.Contains(body, "Обсуждение") {
		t.Fatalf("project by REST: %d %s", st, body)
	}
	if st, body := call("GET", "/api/v1/projects/GT/tools", annaKey, nil); st != http.StatusOK || strings.Count(body, `"on": true`) != 4 {
		t.Fatalf("tools by REST: %d %s", st, body)
	}

	// «Права доступа» to the group's tasks (GO24DEV-861): the form for the owner, creating narrowed to the moderators
	if _, body := do(boss, false, "GET", "/p/GT", nil); !strings.Contains(body, `action="/p/GT/task-perms"`) || !strings.Contains(body, "Создание задач") {
		t.Fatal("no rights form for the owner")
	}
	if _, body := do(anna, false, "GET", "/p/GT", nil); strings.Contains(body, `action="/p/GT/task-perms"`) {
		t.Fatal("a member sees the rights form")
	}
	if st, _ := do(anna, false, "POST", "/p/GT/task-perms", url.Values{"create": {"owner"}, "edit": {"owner"}, "delete": {"owner"}}); st == http.StatusSeeOther {
		t.Fatal("a member changes the rights")
	}
	if st, _ := do(boss, false, "POST", "/p/GT/task-perms", url.Values{"create": {"managers"}, "edit": {"managers"}, "delete": {"owner"}}); st != http.StatusSeeOther {
		t.Fatalf("save rights: %d", st)
	}
	if st, body := do(anna, false, "GET", "/p/GT", nil); st != http.StatusOK || strings.Contains(body, `/tasks/new?project=GT`) {
		t.Fatalf("a member is offered to create: %d", st)
	}
	var n0, n1 int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, gid).Scan(&n0))
	do(anna, false, "POST", "/p/GT/tasks", url.Values{"title": {"Нельзя"}})
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE project_id = $1`, gid).Scan(&n1))
	if n1 != n0 {
		t.Fatal("a member made a task with «Создание задач» for the moderators")
	}
	if st, body := call("PATCH", "/api/v1/projects/GT", bossKey, map[string]any{"task_perms": map[string]string{"create": "members"}}); st != http.StatusOK ||
		!strings.Contains(body, `"create": "members"`) || !strings.Contains(body, `"delete": "owner"`) {
		t.Fatalf("rights by REST: %d %s", st, body)
	}
	if st, _ := call("PATCH", "/api/v1/projects/GT", annaKey, map[string]any{"task_perms": map[string]string{"create": "owner"}}); st != http.StatusForbidden {
		t.Fatalf("a member sets the rights by REST: %d", st)
	}
	if st, _ := do(anna, false, "POST", "/p/GT/tasks", url.Values{"title": {"Можно снова"}}); st != http.StatusSeeOther {
		t.Fatalf("a member creates again: %d", st)
	}
}
