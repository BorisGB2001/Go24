package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestWorkSchedulesHTTP: an administrator creates a shift schedule of a department; the head of the department plans
// the shifts of their people, an employee only sees the plan; the worksheet and the day count by the shift.
func TestWorkSchedulesHTTP(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, olga, admin := user("boss", "Борис Начальников"), user("anna", "Анна Сотрудникова"), user("olga", "Ольга Посторонняя"), user("adm", "Админ")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, dept)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, id := range []string{boss, anna, olga, admin} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: id == admin}))
		as[id] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := noRedirect.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			return loc
		}
		return string(b)
	}

	do(anna, "POST", "/worktime/schedules", url.Values{"name": {"Склад"}, "kind": {"shift"}}, 403)
	do(anna, "GET", "/worktime/schedules/new", nil, 403)
	if loc := do(admin, "POST", "/worktime/schedules", url.Values{"name": {"Склад"}, "kind": {"shift"}}, 303); !strings.Contains(loc, "/worktime/schedules/new?msg=") {
		t.Fatalf("a schedule without shifts: %s", loc)
	}
	loc := do(admin, "POST", "/worktime/schedules", url.Values{"name": {"Склад"}, "kind": {"shift"}, "tolerance": {"10"},
		"members": {"dept:" + dept}, "excluded": {olga},
		"sh_id": {"", ""}, "sh_name": {"Дневная", ""}, "sh_start": {"08:00", ""}, "sh_end": {"20:00", ""}, "sh_break": {"60", ""}}, 303)
	path, _, _ := strings.Cut(loc, "?")
	if !strings.HasPrefix(path, "/worktime/schedules/") {
		t.Fatalf("created: %s", loc)
	}
	id := strings.TrimPrefix(path, "/worktime/schedules/")
	page := do(anna, "GET", path, nil, 200)
	if !strings.Contains(page, `value="Дневная"`) || !strings.Contains(page, "Анна Сотрудникова") || !strings.Contains(page, "<fieldset disabled") {
		t.Fatal("the schedule is not shown read-only with its shift and people")
	}
	if page := do(anna, "GET", "/worktime/schedules", nil, 200); !strings.Contains(page, path+`/plan"`) {
		t.Fatal("no link to the shift plan in the list")
	}
	ws, err := srv.Company.WorkSchedule(ctx, mustID(t, id))
	must(err)
	shift := ws.Shifts[0].ID
	month := time.Now().Format("2006-01")
	today := time.Now().Format(time.DateOnly)
	cell := "c|" + anna + "|" + today
	if page := do(anna, "GET", path+"/plan", nil, 200); strings.Contains(page, `name="`+cell+`"`) {
		t.Fatal("an employee plans shifts")
	}
	do(anna, "POST", path+"/plan?month="+month, url.Values{cell: {strconv.FormatInt(shift, 10)}}, 403)
	do(boss, "POST", path+"/plan?month="+month, url.Values{cell: {strconv.FormatInt(shift, 10)}}, 303)
	if page := do(boss, "GET", path+"/plan", nil, 200); !strings.Contains(page, `name="`+cell+`"`) || !strings.Contains(page, `selected>Дневная`) {
		t.Fatal("the planned shift is not shown to the head")
	}
	if page := do(anna, "GET", "/worktime/d/"+anna+"/"+today, nil, 200); !strings.Contains(page, "Дневная, 08:00–20:00") {
		t.Fatal("the day does not show the shift")
	}
	if page := do(boss, "GET", "/worktime/table", nil, 200); !strings.Contains(page, "Дневная 08:00–20:00") {
		t.Fatal("the worksheet does not show the shift")
	}
	// «Уведомлять о нарушениях» of the schedule: the people chosen are kept and shown
	do(admin, "POST", path, url.Values{"name": {"Склад"}, "kind": {"shift"}, "members": {"dept:" + dept}, "notify_to": {olga},
		"sh_id": {strconv.FormatInt(shift, 10)}, "sh_name": {"Дневная"}, "sh_start": {"08:00"}, "sh_end": {"20:00"}, "sh_break": {"60"}}, 303)
	if ws, err = srv.Company.WorkSchedule(ctx, mustID(t, id)); err != nil || ws.NotifyHead || len(ws.NotifyTo) != 1 || ws.NotifyTo[0] != olga {
		t.Fatalf("notify of the schedule: %v %v %v", err, ws.NotifyHead, ws.NotifyTo)
	}
	if page := do(admin, "GET", path, nil, 200); !strings.Contains(page, `name="notify_head"`) || !strings.Contains(page, olga) {
		t.Fatal("the schedule page does not show whom to notify")
	}
	// «Отчёт о рабочем дне» and «Устройства для отметки»: olga marks her day only by a terminal or an app, with a report
	form := url.Values{"name": {"Склад"}, "kind": {"shift"}, "members": {"dept:" + dept, olga}, "devices_shown": {"1"},
		"sh_id": {strconv.FormatInt(shift, 10)}, "sh_name": {"Дневная"}, "sh_start": {"08:00"}, "sh_end": {"20:00"}, "sh_break": {"60"}}
	if loc := do(admin, "POST", path, form, 303); !strings.Contains(loc, "msg=") { // no device chosen
		t.Fatalf("saved without devices: %s", loc)
	}
	form["report_req"], form["devices"] = []string{"required"}, []string{"terminal", "app"}
	do(admin, "POST", path, form, 303)
	if page := do(admin, "GET", path, nil, 200); !strings.Contains(page, `name="report_req" value="required" checked`) ||
		!strings.Contains(page, `name="devices" value="app" checked`) || strings.Contains(page, `name="devices" value="browser" checked`) {
		t.Fatal("the schedule page does not show the report and the devices")
	}
	if page := do(olga, "GET", "/worktime", nil, 200); !strings.Contains(page, "не разрешает отмечать рабочий день в браузере") ||
		strings.Contains(page, `name="action" value="start"`) {
		t.Fatal("the working day window offers the browser")
	}
	if loc := do(olga, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatalf("started from the browser: %s", loc)
	}
	form["devices"] = []string{"browser"}
	do(admin, "POST", path, form, 303)
	do(olga, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303)
	if page := do(olga, "GET", "/worktime", nil, 200); !strings.Contains(page, `required placeholder="Отчёт за день (обязательно)"`) {
		t.Fatal("the report is not asked as required")
	}
	if loc := do(olga, "POST", "/worktime/day", url.Values{"action": {"finish"}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatalf("finished without the report: %s", loc)
	}
	do(olga, "POST", "/worktime/day", url.Values{"action": {"finish"}, "report": {"Склад принят"}}, 303)
	if d, _ := srv.Company.Today(ctx, olga, time.Now()); d == nil || d.State() != "done" || d.Report != "Склад принят" {
		t.Fatalf("olga's day %+v", d)
	}
	do(anna, "POST", path+"/delete", nil, 403)
	do(admin, "POST", path+"/delete", nil, 303)
	do(admin, "GET", path, nil, 404)

	// the company's rules: the manager is told of a late start, once, and sees it in the notifications
	do(admin, "POST", "/settings/worktime", url.Values{"start": {"00:00"}, "end": {"23:59"}, "late_after": {"00:00"}, "notify_head": {"on"}}, 303)
	do(anna, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303)
	do(anna, "POST", "/worktime/day", url.Values{"action": {"pause"}}, 303)
	do(anna, "POST", "/worktime/day", url.Values{"action": {"resume"}}, 303)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'workday_violation' AND actor_id = $2
		AND url = $3`, boss, anna, "/worktime/d/"+anna+"/"+today).Scan(&n))
	if n != 1 {
		t.Fatalf("the manager got %d notifications of the late start", n)
	}
	if page := do(boss, "GET", "/notifications", nil, 200); !strings.Contains(page, "<b>Анна Сотрудникова</b> опоздал(а)") {
		t.Fatal("the late start is not in the manager's notifications")
	}
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
