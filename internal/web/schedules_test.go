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
	do(anna, "POST", path+"/delete", nil, 403)
	do(admin, "POST", path+"/delete", nil, 303)
	do(admin, "GET", path, nil, 404)
}

func mustID(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
