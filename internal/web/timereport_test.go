package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestTimeReportPage: «Затраченное время» (GO24DEV-971) — an employee sees only oneself, the head of a department the
// people of it and its sub-departments, the administrator in admin mode everyone; a person's line opens their tasks;
// a department and a project narrow the people; another's line is closed to one who does not see them; CSV for Excel.
func TestTimeReportPage(t *testing.T) {
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
	boss, vera, ivan, anna := newUser("Борис Админ"), newUser("Вера Руководитель"), newUser("Иван Продажник"), newUser("Анна Оптовик")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, boss)
	must(err)
	var root, sales, opt string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, root, vera).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, sales).Scan(&opt))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, ivan, sales, anna, opt)
	must(err)

	ts := tasks.NewStore(pool)
	pk, err := ts.CreateProject(ctx, boss, "TR", "Отчёт", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, pk)
	must(err)
	pk2, err := ts.CreateProject(ctx, boss, "TS", "Другой", "")
	must(err)
	p2, err := ts.ProjectByKey(ctx, boss, pk2)
	must(err)
	for _, u := range []string{vera, ivan, anna} {
		must(ts.SetMember(ctx, p, u, "member"))
		must(ts.SetMember(ctx, p2, u, "member"))
	}
	task := func(p tasks.Project, title, who string) string {
		key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: title, ResponsibleID: who})
		must(err)
		id, err := ts.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	today := time.Now()
	must(ts.AddTime(ctx, ivan, task(p, "Звонки клиентам", ivan), 5400, today, ""))
	must(ts.AddTime(ctx, anna, task(p2, "Прайс опта", anna), 1800, today, ""))
	must(ts.AddTime(ctx, boss, task(p, "Своё дело Бориса", boss), 600, today, ""))

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	get := func(who string, admin bool, path string, want int) string {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Admin: admin, AdminMode: admin}))
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d", path, res.StatusCode, want)
		}
		return string(b)
	}
	has := func(page string, in, out []string) {
		t.Helper()
		for _, s := range in {
			if !strings.Contains(page, s) {
				t.Errorf("no %q", s)
			}
		}
		for _, s := range out {
			if strings.Contains(page, s) {
				t.Errorf("unexpected %q", s)
			}
		}
	}

	page := get(ivan, false, "/tasks/time", 200)
	has(page, []string{"Затраченное время", "Иван Продажник", "1 ч 30 мин"}, []string{"Анна Оптовик", "Борис Админ", `name="dept"`})
	get(ivan, false, "/tasks/time?user="+anna, 403)

	page = get(vera, false, "/tasks/time", 200)
	has(page, []string{"Иван Продажник", "Анна Оптовик", `name="dept"`, "Итого", "2 ч 00 мин"}, []string{"Борис Админ"})
	page = get(vera, false, "/tasks/time?dept="+opt, 200)
	has(page, []string{"Анна Оптовик"}, []string{"Иван Продажник"})
	page = get(vera, false, "/tasks/time?project="+p.ID, 200)
	has(page, []string{"Иван Продажник"}, []string{"Анна Оптовик"})
	page = get(vera, false, "/tasks/time?user="+ivan, 200)
	has(page, []string{"Задачи: Иван Продажник", "Звонки клиентам", "/t/TR-1"}, []string{"Прайс опта"})

	page = get(boss, true, "/tasks/time", 200)
	has(page, []string{"Борис Админ", "Иван Продажник", "Анна Оптовик"}, nil)
	page = get(boss, false, "/tasks/time", 200) // out of admin mode — only his own
	has(page, []string{"Борис Админ"}, []string{"Иван Продажник"})

	csv := get(vera, false, "/tasks/time?format=csv", 200)
	has(csv, []string{"Сотрудник;Задач;Затрачено;Сверх плана", "Иван Продажник;1;1:30;0", "Анна Оптовик;1;0:30;0"}, nil)
	csv = get(vera, false, "/tasks/time?format=csv&user="+ivan, 200)
	has(csv, []string{"TR-1;Звонки клиентам;1:30;1:30;"}, nil)
}
