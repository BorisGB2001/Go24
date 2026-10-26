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
	"go24/internal/worktime"
)

// TestWorksheetHTTP: the forgotten day is closed by a change the manager approves from «Рабочее время»; the table
// shows a manager their people, an employee — only themself; a stranger does not open somebody's day.
func TestWorksheetHTTP(t *testing.T) {
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
	boss, anna, olga := user("boss", "Борис Начальников"), user("anna", "Анна Сотрудникова"), user("olga", "Ольга Посторонняя")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, dept)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Now()
	y := now.AddDate(0, 0, -1)
	yday := time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, time.Local)
	must(srv.Company.WorkAction(ctx, anna, "start", "", yday.Add(9*time.Hour), company.Place{}))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, id := range []string{boss, anna, olga} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		as[id] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ip := ""
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if ip != "" {
			req.Header.Set("X-Forwarded-For", ip)
		}
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
	dayURL := "/worktime/d/" + anna + "/" + yday.Format(time.DateOnly)
	if page := do(anna, "GET", "/worktime", nil, 200); !strings.Contains(page, dayURL) || !strings.Contains(page, "wd-forgot") {
		t.Fatal("the forgotten day is not offered to finish")
	}
	if loc := do(anna, "POST", dayURL, url.Values{"end": {"18:00"}, "reason": {"забыла завершить"}, "back": {"/worktime"}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatalf("change: %s", loc)
	}
	if page := do(anna, "GET", "/worktime", nil, 200); strings.Contains(page, "wd-forgot") {
		t.Fatal("the day with a change waiting is offered again")
	}
	do(olga, "GET", dayURL, nil, 403)
	if page := do(anna, "GET", "/worktime/table", nil, 200); !strings.Contains(page, "Анна Сотрудникова") || strings.Contains(page, "Борис Начальников") {
		t.Fatal("an employee's table")
	}
	page := do(boss, "GET", "/worktime/table", nil, 200)
	if !strings.Contains(page, "Анна Сотрудникова") || !strings.Contains(page, "ws-pend") {
		t.Fatal("the manager's table has no Анна with the change waiting")
	}
	pending, err := srv.Company.PendingChanges(ctx, []string{anna}, false)
	must(err)
	if len(pending) != 1 {
		t.Fatalf("pending %d", len(pending))
	}
	if page := do(boss, "GET", "/worktime", nil, 200); !strings.Contains(page, "забыла завершить") {
		t.Fatal("the manager does not see the change to approve")
	}
	do(anna, "POST", "/worktime/changes/"+strconv.FormatInt(pending[0].ID, 10), url.Values{"decision": {"approve"}}, 403)
	do(boss, "POST", "/worktime/changes/"+strconv.FormatInt(pending[0].ID, 10), url.Values{"decision": {"approve"}}, 303)
	d, err := srv.Company.Day(ctx, anna, yday)
	must(err)
	if d.Ended == nil || d.Worked(now) != 9*3600 {
		t.Fatalf("approved day %+v", d)
	}
	if page := do(anna, "GET", dayURL, nil, 200); !strings.Contains(page, "забыла завершить") || !strings.Contains(page, "Борис Начальников") {
		t.Fatal("the history of the day")
	}

	// «Офисные сети»: started only from the office, finished outside — the day page and the table say so
	sched := worktime.Default()
	sched.Offices, sched.OfficeOnly = []worktime.Office{{Net: "192.168.7.0/24", Name: "Главный офис"}}, true
	must(srv.Tasks.SetSchedule(ctx, boss, sched))
	ip = "8.8.8.8"
	if loc := do(anna, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303); !strings.Contains(loc, "msg=") {
		t.Fatalf("started outside the office: %s", loc)
	}
	ip = "192.168.7.20"
	do(anna, "POST", "/worktime/day", url.Values{"action": {"start"}}, 303)
	ip = "8.8.8.8"
	do(anna, "POST", "/worktime/day", url.Values{"action": {"finish"}}, 303)
	td, err := srv.Company.Today(ctx, anna, time.Now())
	must(err)
	if td == nil || td.Start.IP != "192.168.7.20" || td.Start.Away() || !td.End.Away() || td.End.IP != "8.8.8.8" {
		t.Fatalf("today %+v", td)
	}
	if page := do(anna, "GET", "/worktime/d/"+anna+"/"+time.Now().Format(time.DateOnly), nil, 200); !strings.Contains(page, "Главный офис") ||
		!strings.Contains(page, "IP 8.8.8.8") || !strings.Contains(page, "вне офиса") {
		t.Fatal("the day page has no places")
	}
	if page := do(boss, "GET", "/worktime/table", nil, 200); !strings.Contains(page, `class="ws-away"`) {
		t.Fatal("the table has no day out of office")
	}
}
