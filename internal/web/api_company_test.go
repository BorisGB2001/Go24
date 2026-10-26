package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestCompanyAPI: «Компания» through REST API v1 — employees found and read, the structure read and changed by an
// administrator only, the key owner's working day started, paused and finished, another person's days seen by their
// head only, schedules and a person's days by their schedule, absences added and removed with the rights of the page.
func TestCompanyAPI(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ($1, $2, $1 || '@go24.test', 'Инженер')
			RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, olga, admin := user("boss", "Борис Начальников"), user("anna", "Анна Сотрудникова"), user("olga", "Ольга Посторонняя"), user("adm", "Админ")
	vera := user("vera", "Вера Заместителева")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET birthday = '1990-05-12', birthday_show_year = false WHERE id = $1`, anna)
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	rw := []string{"company:read", "company:write"}
	bossKey, annaKey, olgaKey, adminKey := key(boss, rw...), key(anna, rw...), key(olga, rw...), key(admin, rw...)
	readOnly, noScope := key(anna, "company:read"), key(anna, "tasks:read")
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	do := func(k, method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		return b
	}
	decode := func(b []byte, v any) {
		t.Helper()
		must(json.Unmarshal(b, v))
	}

	// the structure: an administrator adds a department under the company, puts the head and Anna into it
	do(annaKey, "POST", "/api/v1/departments", `{"name":"Отдел"}`, 403)
	do(readOnly, "GET", "/api/v1/departments", "", 200)
	do(noScope, "GET", "/api/v1/departments", "", 403)
	var created struct{ ID string }
	decode(do(adminKey, "POST", "/api/v1/departments", `{"name":"Отдел продаж","head_id":"`+boss+`"}`, 201), &created)
	dept := created.ID
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, dept)
	must(err)
	do(annaKey, "PATCH", "/api/v1/departments/"+dept, `{"name":"Х"}`, 403)
	do(adminKey, "PATCH", "/api/v1/departments/"+dept, `{"name":"Продажи","deputies":["`+vera+`"]}`, 204)
	var depts []struct {
		ID, Name            string
		ParentID            string `json:"parent_id"`
		HeadID              string `json:"head_id"`
		Deputies, Employees []struct{ ID string }
	}
	decode(do(annaKey, "GET", "/api/v1/departments", "", 200), &depts)
	var found bool
	for _, d := range depts {
		if d.ID == dept {
			found = true
			if d.Name != "Продажи" || d.HeadID != boss || d.ParentID == "" || len(d.Deputies) != 1 || len(d.Employees) != 1 ||
				d.Employees[0].ID != anna {
				t.Fatalf("department: %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("no department %s in %+v", dept, depts)
	}

	// teams: an administrator adds one under the department with the boss at its head; the head changes its
	// people but not the head; Anna — not; the list of users by team and the teams in a card
	do(annaKey, "POST", "/api/v1/teams", `{"name":"Альфа"}`, 403)
	decode(do(adminKey, "POST", "/api/v1/teams", `{"department_id":"`+dept+`","name":"Альфа","head_id":"`+boss+`","members":["`+anna+`"]}`, 201), &created)
	team := created.ID
	do(annaKey, "PATCH", "/api/v1/teams/"+team, `{"name":"Х"}`, 403)
	do(bossKey, "PATCH", "/api/v1/teams/"+team, `{"description":"Новый продукт","head_id":"`+anna+`","deputies":["`+vera+`"]}`, 204)
	var tm struct {
		Name, Description, Department string
		HeadID                        string `json:"head_id"`
		DepartmentID                  string `json:"department_id"`
		Deputies, Members             []struct{ ID string }
	}
	decode(do(readOnly, "GET", "/api/v1/teams/"+team, "", 200), &tm)
	if tm.Name != "Альфа" || tm.Description != "Новый продукт" || tm.HeadID != boss || tm.DepartmentID != dept || tm.Department != "Продажи" ||
		len(tm.Deputies) != 1 || tm.Deputies[0].ID != vera || len(tm.Members) != 1 || tm.Members[0].ID != anna {
		t.Fatalf("team: %+v", tm)
	}
	var teams []struct{ ID string }
	decode(do(annaKey, "GET", "/api/v1/teams?department_id="+dept, "", 200), &teams)
	if len(teams) != 1 || teams[0].ID != team {
		t.Fatalf("teams of the department: %+v", teams)
	}
	var byTeam []struct {
		ID    string
		Teams []struct{ ID, Name string }
	}
	decode(do(annaKey, "GET", "/api/v1/users?team_id="+team, "", 200), &byTeam)
	if len(byTeam) != 3 {
		t.Fatalf("people of the team: %+v", byTeam)
	}
	for _, u := range byTeam {
		if len(u.Teams) != 1 || u.Teams[0].Name != "Альфа" {
			t.Fatalf("teams of %s: %+v", u.ID, u.Teams)
		}
	}
	do(bossKey, "DELETE", "/api/v1/teams/"+team, "", 403)
	do(adminKey, "DELETE", "/api/v1/teams/"+team, "", 204)
	do(adminKey, "GET", "/api/v1/teams/"+team, "", 404)

	// employees: search, the department, the card; the year of birth hidden from others
	var users []struct {
		ID, Name, Birthday string
		Departments        []struct{ ID string }
	}
	decode(do(olgaKey, "GET", "/api/v1/users?q=Сотрудникова", "", 200), &users)
	if len(users) != 1 || users[0].ID != anna || users[0].Birthday != "05-12" || len(users[0].Departments) != 1 {
		t.Fatalf("search: %+v", users)
	}
	decode(do(olgaKey, "GET", "/api/v1/users?department_id="+dept, "", 200), &users)
	if len(users) != 1 || users[0].ID != anna {
		t.Fatalf("department's people: %+v", users)
	}
	do(olgaKey, "GET", "/api/v1/users?preset=nobody", "", 422)
	do(olgaKey, "GET", "/api/v1/users?guests=1", "", 403)
	var me struct{ ID, Birthday, Position string }
	decode(do(annaKey, "GET", "/api/v1/users/me", "", 200), &me)
	if me.ID != anna || me.Birthday != "1990-05-12" || me.Position != "Инженер" {
		t.Fatalf("me: %+v", me)
	}
	do(annaKey, "GET", "/api/v1/users/00000000-0000-0000-0000-000000000000", "", 404)

	// the working day: own only; Anna's days are seen by her head, not by Olga
	var st struct {
		State   string
		Workday struct {
			State  string
			Report string
			Start  struct{ IP string }
			End    *struct{ IP string }
		}
	}
	decode(do(annaKey, "GET", "/api/v1/worktime/status", "", 200), &st)
	if st.State != "none" {
		t.Fatalf("before the start: %+v", st)
	}
	do(readOnly, "POST", "/api/v1/worktime/start", "", 403)
	decode(do(annaKey, "POST", "/api/v1/worktime/start", "", 200), &st)
	if st.State != "running" {
		t.Fatalf("started: %+v", st)
	}
	do(annaKey, "POST", "/api/v1/worktime/resume", "", 422) // not paused
	decode(do(annaKey, "POST", "/api/v1/worktime/pause", "", 200), &st)
	if st.State != "paused" {
		t.Fatalf("paused: %+v", st)
	}
	decode(do(annaKey, "POST", "/api/v1/worktime/finish", `{"report":"Звонки клиентам"}`, 200), &st)
	if st.State != "done" || st.Workday.Report != "Звонки клиентам" || st.Workday.Start.IP == "" || st.Workday.End == nil {
		t.Fatalf("finished: %+v", st)
	}
	do(annaKey, "POST", "/api/v1/worktime/fly", "", 422)
	var days []struct {
		UserID     string `json:"user_id"`
		Day, State string
	}
	decode(do(bossKey, "GET", "/api/v1/worktime/days?user_id="+anna, "", 200), &days)
	if len(days) != 1 || days[0].UserID != anna || days[0].State != "done" || days[0].Day != time.Now().Format(time.DateOnly) {
		t.Fatalf("days for the head: %+v", days)
	}
	do(olgaKey, "GET", "/api/v1/worktime/days?user_id="+anna, "", 403)
	do(annaKey, "GET", "/api/v1/worktime/days?from=2026-13-01", "", 422)
	decode(do(annaKey, "GET", "/api/v1/worktime/days", "", 200), &days)
	if len(days) != 1 {
		t.Fatalf("own days: %+v", days)
	}

	// schedules: a fixed one for the department; Anna's days by it
	ws := company.WorkSchedule{Name: "Пятидневка", Kind: "fixed", Times: []company.ScheduleTime{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00",
		End: "17:00", Break: 60}}, Members: []string{"dept:" + dept}}
	must(srv.Company.SaveWorkSchedule(ctx, admin, &ws))
	var list []struct {
		ID          int64
		Name        string
		Departments int
	}
	decode(do(annaKey, "GET", "/api/v1/worktime/schedules", "", 200), &list)
	if len(list) != 1 || list[0].Name != "Пятидневка" || list[0].Departments != 1 {
		t.Fatalf("schedules: %+v", list)
	}
	var one struct {
		Kind   string
		People []struct{ ID string }
	}
	decode(do(annaKey, "GET", "/api/v1/worktime/schedules/"+jsonID(list[0].ID), "", 200), &one)
	if one.Kind != "fixed" || len(one.People) != 1 || one.People[0].ID != anna {
		t.Fatalf("schedule: %+v", one)
	}
	do(annaKey, "GET", "/api/v1/worktime/schedules/999999", "", 404)
	var us struct {
		Schedule *struct{ Name string }
		Days     map[string]struct {
			Work       bool
			Start, End string
		}
	}
	decode(do(olgaKey, "GET", "/api/v1/users/"+anna+"/schedule?from=2026-10-26&to=2026-11-01", "", 200), &us)
	if us.Schedule == nil || us.Schedule.Name != "Пятидневка" || len(us.Days) != 7 || !us.Days["2026-10-26"].Work ||
		us.Days["2026-10-26"].Start != "08:00" || us.Days["2026-11-01"].Work {
		t.Fatalf("Anna's schedule: %+v", us)
	}
	us.Schedule, us.Days = nil, nil
	decode(do(olgaKey, "GET", "/api/v1/users/me/schedule?from=2026-10-26&to=2026-10-26", "", 200), &us)
	if us.Schedule != nil || len(us.Days) != 1 {
		t.Fatalf("Olga without a schedule: %+v", us)
	}

	// absences: own, by the head for Anna; Olga can neither add nor remove Anna's
	do(olgaKey, "POST", "/api/v1/absences", `{"user_id":"`+anna+`","kind":"vacation","from":"2026-11-02","to":"2026-11-06"}`, 403)
	do(annaKey, "POST", "/api/v1/absences", `{"kind":"holiday","from":"2026-11-02","to":"2026-11-06"}`, 422)
	decode(do(bossKey, "POST", "/api/v1/absences", `{"user_id":"`+anna+`","kind":"vacation","from":"2026-11-02","to":"2026-11-06",
		"comment":"Отпуск"}`, 201), &created)
	var abs []struct {
		ID, Kind, From, To, Comment string
		UserID                      string `json:"user_id"`
	}
	decode(do(olgaKey, "GET", "/api/v1/absences?from=2026-11-01&to=2026-11-30&user_id="+anna, "", 200), &abs)
	if len(abs) != 1 || abs[0].ID != created.ID || abs[0].Kind != "vacation" || abs[0].From != "2026-11-02" || abs[0].Comment != "Отпуск" {
		t.Fatalf("absences: %+v", abs)
	}
	decode(do(olgaKey, "GET", "/api/v1/absences?from=2026-12-01&to=2026-12-31", "", 200), &abs)
	if len(abs) != 0 {
		t.Fatalf("December: %+v", abs)
	}
	do(olgaKey, "DELETE", "/api/v1/absences/"+created.ID, "", 403)
	do(annaKey, "DELETE", "/api/v1/absences/"+created.ID, "", 204)

	// an empty department removed by an administrator
	do(bossKey, "DELETE", "/api/v1/departments/"+dept, "", 403)
	do(adminKey, "DELETE", "/api/v1/departments/"+dept, "", 422) // not empty
	decode(do(adminKey, "POST", "/api/v1/departments", `{"name":"Склад","parent_id":"`+dept+`"}`, 201), &created)
	do(adminKey, "DELETE", "/api/v1/departments/"+created.ID, "", 204)
}

func jsonID(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
