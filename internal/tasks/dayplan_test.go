package tasks

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestDayPlan: «План на день» holds the tasks the person put in (only those they see), with the time logged today;
// the choices are one's open tasks not in the plan; a task completed today stays till the end of the day and leaves
// the plan the day after; the day's tasks list the logged time.
func TestDayPlan(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, worker, stranger := user("Борис Г"), user("Иван Петров"), user("Чужой")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	task := func(title string) string {
		key, err := s.CreateTask(ctx, boss, p, NewTask{Title: title, ResponsibleID: worker})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	report, call := task("Отчёт"), task("Позвонить")
	now := time.Now()

	choices, err := s.DayPlanChoices(ctx, worker)
	must(err)
	if len(choices) != 2 {
		t.Fatalf("both open tasks are to choose from: %+v", choices)
	}
	if err := s.SetDayPlan(ctx, stranger, report, true); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrForbidden) {
		t.Fatalf("one who does not see the task cannot plan it: %v", err)
	}
	must(s.SetDayPlan(ctx, worker, report, true))
	must(s.SetDayPlan(ctx, worker, report, true)) // twice — once
	must(s.SetDayPlan(ctx, worker, call, true))
	if on, err := s.InDayPlan(ctx, worker, report); err != nil || !on {
		t.Fatalf("in the plan: %v %v", on, err)
	}
	if choices, _ = s.DayPlanChoices(ctx, worker); len(choices) != 0 {
		t.Fatalf("planned tasks are not offered again: %+v", choices)
	}
	must(s.AddTime(ctx, worker, report, 1800, now, ""))
	must(s.AddTime(ctx, worker, report, 600, now.AddDate(0, 0, -1), "")) // yesterday's time does not count
	plan, err := s.DayPlan(ctx, worker, now)
	must(err)
	if len(plan) != 2 || plan[0].ID != report || plan[0].TodaySecs != 1800 || plan[1].TodaySecs != 0 || plan[0].Timer {
		t.Fatalf("plan: %+v", plan)
	}
	must(s.StartTimer(ctx, worker, call))
	must2 := func(q string, args ...any) { _, err := pool.Exec(ctx, q, args...); must(err) }
	must2(`UPDATE task_timers SET started_at = now() - interval '10 minutes' WHERE user_id = $1`, worker)
	plan, _ = s.DayPlan(ctx, worker, now.Add(time.Second))
	if !plan[1].Timer || plan[1].TodaySecs < 590 {
		t.Fatalf("the running timer counts: %+v", plan[1])
	}
	if other, _ := s.DayPlan(ctx, boss, now); len(other) != 0 {
		t.Fatalf("the plan is the person's own: %+v", other)
	}

	// completed today: stays till the end of the day, then leaves the plan
	must2(`UPDATE tasks_all SET status = 'completed', closed_at = $2 WHERE id = $1`, report, now)
	if plan, _ = s.DayPlan(ctx, worker, now); len(plan) != 2 || !plan[0].Status.Closed() {
		t.Fatalf("completed today stays: %+v", plan)
	}
	if plan, _ = s.DayPlan(ctx, worker, now.AddDate(0, 0, 1)); len(plan) != 1 || plan[0].ID != call {
		t.Fatalf("completed yesterday leaves: %+v", plan)
	}
	must(s.SetDayPlan(ctx, worker, call, false))
	if plan, _ = s.DayPlan(ctx, worker, now); len(plan) != 0 {
		t.Fatalf("taken out: %+v", plan)
	}

	// the day's tasks: the logged time of that day, for those who see the tasks
	day, err := s.DayTasks(ctx, boss, worker, now)
	must(err)
	if len(day) != 1 || day[0].Title != "Отчёт" || day[0].Secs != 1800 {
		t.Fatalf("day tasks: %+v", day)
	}
	if day, _ = s.DayTasks(ctx, stranger, worker, now); len(day) != 0 {
		t.Fatalf("a stranger does not see the tasks: %+v", day)
	}
}
