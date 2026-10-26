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
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestWorkReportsHTTP: an administrator sets daily reports; finishing the day opens the report form; the report goes
// to the manager, who marks it; the employee comments; a stranger does not open it.
func TestWorkReportsHTTP(t *testing.T) {
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

	do(anna, "POST", "/settings/worktime/reports", url.Values{"period": {"day"}}, 403)
	do(admin, "POST", "/settings/worktime/reports", url.Values{"period": {"week"}, "weekday": {"5"},
		"dept_id": {dept, ""}, "dept_period": {"day", "day"}, "dept_weekday": {"5", "5"}, "dept_monthday": {"31", "31"}}, 303)
	if page := do(admin, "GET", "/settings/worktime", nil, 200); !strings.Contains(page, `value="`+dept+`" selected`) {
		t.Fatal("the department's rule is not shown")
	}
	if page := do(anna, "GET", "/worktime", nil, 200); !strings.Contains(page, "wd-report-due") {
		t.Fatal("no reminder of the report in the top bar")
	}
	do(anna, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303)
	if loc := do(anna, "POST", "/worktime/day", url.Values{"action": {"finish"}, "back": {"/my"}}, 303); loc != "/worktime/reports/new" {
		t.Fatalf("finishing the day leads to %s", loc)
	}
	if page := do(anna, "GET", "/worktime/reports/new", nil, 200); !strings.Contains(page, "Борис Начальников") {
		t.Fatal("the form does not say whom the report goes to")
	}
	loc := do(anna, "POST", "/worktime/reports", url.Values{"body": {"Сделала сверку"}}, 303)
	path, _, _ := strings.Cut(loc, "?")
	if !strings.HasPrefix(path, "/worktime/reports/") {
		t.Fatalf("sent to %s", loc)
	}
	do(olga, "GET", path, nil, 404)
	do(anna, "POST", path+"/mark", url.Values{"mark": {"good"}}, 403)
	if page := do(boss, "GET", path, nil, 200); !strings.Contains(page, "Сделала сверку") || !strings.Contains(page, "/mark") {
		t.Fatal("the manager does not see the report to mark")
	}
	do(boss, "POST", path+"/mark", url.Values{"mark": {"good"}, "comment": {"Отлично"}}, 303)
	do(anna, "POST", path+"/comments", url.Values{"body": {"Спасибо"}}, 303)
	if page := do(anna, "GET", path, nil, 200); !strings.Contains(page, "wr-good") || !strings.Contains(page, "Отлично") || !strings.Contains(page, "Спасибо") {
		t.Fatal("the mark and the discussion")
	}
	if page := do(boss, "GET", "/worktime/reports", nil, 200); !strings.Contains(page, "Анна Сотрудникова") || !strings.Contains(page, path) {
		t.Fatal("the manager's list")
	}
	if page := do(olga, "GET", "/worktime/reports?tab=team", nil, 200); strings.Contains(page, path) {
		t.Fatal("a stranger's list has the report")
	}
	if page := do(anna, "GET", "/notifications", nil, 200); !strings.Contains(page, "оценил(а) ваш рабочий отчёт") && !strings.Contains(page, "marked your work report") {
		t.Fatal("no notification of the mark")
	}
}
