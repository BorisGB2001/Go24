package portalrest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/anon"
	"go24/internal/db"
)

// fakePortal answers user.get and department.get (two pages of users).
func fakePortal(t *testing.T) *httptest.Server {
	users := []map[string]any{
		{"ID": "1", "ACTIVE": true, "USER_TYPE": "employee", "NAME": "Борис", "LAST_NAME": "Настоящев", "SECOND_NAME": "Петрович",
			"EMAIL": "boris.real@acme.ru", "PERSONAL_GENDER": "M", "PERSONAL_BIRTHDAY": "1980-02-23T03:00:00+03:00", "PERSONAL_CITY": "Тайнинск",
			"PERSONAL_MOBILE": "+79140000001", "WORK_PHONE": "8 (4132) 11-22-33", "UF_PHONE_INNER": 1777, "WORK_POSITION": "Главный администратор",
			"UF_DEPARTMENT": []int{1, 5}, "UF_EMPLOYMENT_DATE": "2019-01-10"},
		{"ID": "2", "ACTIVE": true, "USER_TYPE": "employee", "NAME": "Мария", "LAST_NAME": "Реальная", "EMAIL": "maria.real@acme.ru",
			"PERSONAL_GENDER": "F", "WORK_POSITION": "Менеджер", "UF_DEPARTMENT": []string{"5"}},
	}
	fired := []map[string]any{{"ID": "3", "ACTIVE": false, "USER_TYPE": "employee", "NAME": "Иван", "LAST_NAME": "Ушедший",
		"EMAIL": "ivan.real@acme.ru", "UF_DEPARTMENT": []int{5}}}
	depts := []map[string]any{
		{"ID": "1", "NAME": "Ромашка", "SORT": 500, "UF_HEAD": "1"},
		{"ID": "5", "NAME": "Группа поддержки ПО", "SORT": 500, "PARENT": "1", "UF_HEAD": "2"},
	}
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		out := map[string]any{}
		switch {
		case strings.HasSuffix(r.URL.Path, "/user.get.json"):
			f := p["FILTER"].(map[string]any)
			if f["ACTIVE"] == false {
				out["result"] = fired
			} else if p["start"].(float64) == 0 { // first page, then the next one
				out["result"], out["next"] = users[:1], 1
			} else {
				out["result"] = users[1:]
			}
		case strings.HasSuffix(r.URL.Path, "/department.get.json"):
			out["result"] = depts
		case strings.HasSuffix(r.URL.Path, "/sonet_group.get.json"):
			if p["IS_ADMIN"] != "Y" {
				t.Error("sonet_group.get without IS_ADMIN sees only the webhook owner's groups")
			}
			out["result"] = []map[string]any{{"ID": "5", "NAME": "Отдел продаж", "DESCRIPTION": "Группа Мария Реальная", "OWNER_ID": "1", "CLOSED": "N"}}
		case strings.HasSuffix(r.URL.Path, "/tasks.task.list.json"):
			out["result"] = map[string]any{"tasks": []map[string]any{
				{"id": "101", "parentId": "0", "title": "Позвонить: Мария Реальная", "description": "[B]Важно[/B] [USER=2]Мария Реальная[/USER]",
					"status": "5", "priority": "2", "groupId": "5", "createdBy": "1", "responsibleId": "2", "accomplices": []string{"3"},
					"auditors": []string{"1"}, "deadline": "2026-10-20T18:00:00+03:00", "createdDate": "2026-10-01T09:00:00+03:00",
					"closedDate": "2026-10-02T12:00:00+03:00", "taskControl": "Y", "commentsCount": "1", "timeEstimate": "3600"},
				{"id": "102", "parentId": "101", "title": "Подзадача", "status": "2", "groupId": "0", "createdBy": "2", "responsibleId": "99",
					"createdDate": "2026-10-03T09:00:00+03:00", "commentsCount": "0"}}}
		case strings.HasSuffix(r.URL.Path, "/batch.json"):
			res := map[string]any{}
			for name := range p["cmd"].(map[string]any) {
				switch name {
				case "g5":
					res[name] = []map[string]any{{"USER_ID": "2", "ROLE": "K"}, {"USER_ID": "1", "ROLE": "A"}}
				case "c101":
					res[name] = []map[string]any{{"ID": "900", "AUTHOR_ID": "2", "POST_MESSAGE": "Готово, Борис Настоящев", "POST_DATE": "2026-10-02T11:00:00+03:00"}}
				case "k101":
					res[name] = []map[string]any{{"ID": "1", "PARENT_ID": "0", "TITLE": "Чек-лист 1", "IS_COMPLETE": "N", "SORT_INDEX": "0"},
						{"ID": "2", "PARENT_ID": "1", "TITLE": "Узнать у Мария", "IS_COMPLETE": "Y", "SORT_INDEX": "1"}}
				default:
					res[name] = []any{}
				}
			}
			out["result"] = map[string]any{"result": res}
		default:
			out["error"], out["error_description"] = "ERROR_METHOD_NOT_FOUND", "no"
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
}

func TestImport(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	if _, err := pool.Exec(ctx, `INSERT INTO departments (name, sort) VALUES ('Моя компания', 0)`); err != nil {
		t.Fatal(err)
	}
	srv := fakePortal(t)
	defer srv.Close()
	if _, err := New("http://x/rest/1/abc/"); err == nil {
		t.Fatal("plain http accepted")
	}
	c, err := New(srv.URL + "/rest/1/secret/")
	if err != nil {
		t.Fatal(err)
	}
	c.http, c.Gap = srv.Client(), 0
	o := Options{Portal: "acme", Anon: anon.New("seed:acme", "acme.import"), Seed: "seed:acme"}

	st, err := ImportUsers(ctx, c, pool, o)
	if err != nil {
		t.Fatal(err)
	}
	if st.Users != 3 || st.Active != 2 || st.Departments != 2 {
		t.Fatalf("stats = %+v", st)
	}
	n, err := ImportDepartments(ctx, c, pool, o)
	if err != nil || n != 2 {
		t.Fatalf("departments: %d %v", n, err)
	}

	// nothing real reached the database
	var dump string
	if err := pool.QueryRow(ctx, `SELECT string_agg(concat_ws('|', email, name, last_name, first_name, second_name, position,
		work_phone, mobile_phone, inner_phone, birthday, city, employment_date), E'\n') FROM users`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, real := range []string{"Борис", "Настоящев", "Петрович", "Мария", "Реальная", "Иван", "Ушедший", "boris.real", "acme.ru",
		"79140000001", "11-22-33", "1777", "1980-02-23", "Тайнинск", "2019-01-10"} {
		if strings.Contains(dump, real) {
			t.Errorf("real value %q leaked:\n%s", real, dump)
		}
	}
	// positions are real job titles, but shuffled between the two people who had one
	var positions string
	_ = pool.QueryRow(ctx, `SELECT string_agg(position, ',' ORDER BY position) FROM users WHERE position <> ''`).Scan(&positions)
	if positions != "Главный администратор,Менеджер" {
		t.Errorf("positions = %q", positions)
	}
	// structure: the portal's top department took over go24's company node; heads linked; people placed
	var root, head, sub, subHead string
	if err := pool.QueryRow(ctx, `SELECT d.name, u.oidc_sub FROM departments d JOIN users u ON u.id = d.head_id WHERE d.parent_id IS NULL`).Scan(&root, &head); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT d.name, u.oidc_sub FROM departments d JOIN users u ON u.id = d.head_id WHERE d.legacy_id = 5`).Scan(&sub, &subHead); err != nil {
		t.Fatal(err)
	}
	if root != "Ромашка" || head != "portal:acme:1" || sub != "Группа поддержки ПО" || subHead != "portal:acme:2" {
		t.Fatalf("structure: %s %s %s %s", root, head, sub, subHead)
	}
	var inactive bool
	_ = pool.QueryRow(ctx, `SELECT NOT active FROM users WHERE oidc_sub = 'portal:acme:3'`).Scan(&inactive)
	if !inactive {
		t.Error("dismissed employee imported as active")
	}
	// re-import gives the same pseudonyms
	var before, after string
	_ = pool.QueryRow(ctx, `SELECT string_agg(name, ',' ORDER BY oidc_sub) FROM users`).Scan(&before)
	o.Anon = anon.New("seed:acme", "acme.import")
	if _, err := ImportUsers(ctx, c, pool, o); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT string_agg(name, ',' ORDER BY oidc_sub) FROM users`).Scan(&after)
	if before != after {
		t.Errorf("pseudonyms changed: %s → %s", before, after)
	}

	// tasks: the group became a project with its people, tasks outside groups a participants-only project,
	// subtasks, comments and checklists came along, no real name in any text; a second run adds nothing
	for run := 0; run < 2; run++ {
		ts, err := ImportTasks(ctx, c, pool, o)
		if err != nil {
			t.Fatal(err)
		}
		want := TaskStats{Projects: 2, Tasks: 2, Comments: 1, Checks: 1}
		if run == 1 {
			want = TaskStats{}
		}
		if ts != want {
			t.Fatalf("run %d: stats = %+v, want %+v", run, ts, want)
		}
	}
	var members, vis, status, parent, texts string
	var control bool
	var est int
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `SELECT string_agg(m.role, ',' ORDER BY m.role) FROM project_members m JOIN projects p ON p.id = m.project_id WHERE p.key = 'CR5'`).Scan(&members))
	must(pool.QueryRow(ctx, `SELECT visibility FROM projects WHERE key = 'ACME'`).Scan(&vis))
	must(pool.QueryRow(ctx, `SELECT t.status, t.requires_control, t.estimate_minutes FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE p.key = 'CR5' AND t.number = 1`).Scan(&status, &control, &est))
	must(pool.QueryRow(ctx, `SELECT pp.key || '-' || pt.number FROM tasks t JOIN tasks pt ON pt.id = t.parent_id JOIN projects pp ON pp.id = pt.project_id
		JOIN projects p ON p.id = t.project_id WHERE p.key = 'ACME'`).Scan(&parent))
	must(pool.QueryRow(ctx, `SELECT concat_ws('|', (SELECT string_agg(title || description, '|') FROM tasks), (SELECT string_agg(body, '|') FROM task_comments),
		(SELECT string_agg(title, '|') FROM task_checklist_items), (SELECT string_agg(description, '|') FROM projects))`).Scan(&texts))
	if members != "member,owner" || vis != "participants" || status != "completed" || !control || est != 60 || parent != "CR5-1" {
		t.Fatalf("members %q, visibility %q, status %q, control %v, estimate %d, parent %q", members, vis, status, control, est, parent)
	}
	for _, real := range []string{"Мария", "Реальная", "Борис", "Настоящев", "[B]", "[USER"} {
		if strings.Contains(texts, real) {
			t.Errorf("%q in imported texts: %s", real, texts)
		}
	}
	var notified int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&notified))
	if notified != 0 {
		t.Errorf("the import notified people: %d", notified)
	}
}
