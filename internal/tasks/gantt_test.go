package tasks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestLinkShift(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		typ            string
		ps, pe, ss, se int
		want           int
	}{
		{"finish_start", 2, 4, 3, 5, 2}, // starts the day after the predecessor ends
		{"finish_start", 2, 4, 5, 6, 0},
		{"finish_start", 2, 4, 9, 9, 0}, // a gap stays
		{"start_start", 5, 7, 4, 4, 1},
		{"start_start", 5, 7, 5, 6, 0},
		{"finish_finish", 2, 4, 1, 2, 2},
		{"start_finish", 6, 8, 1, 3, 3},
		{"start_finish", 6, 8, 1, 6, 0},
	} {
		if got := linkShift(c.typ, d(c.ps), d(c.pe), d(c.ss), d(c.se)); got != c.want {
			t.Errorf("%s %d..%d → %d..%d: shift %d, want %d", c.typ, c.ps, c.pe, c.ss, c.se, got, c.want)
		}
	}
}

// TestGanttShift: moving a task later moves its successors (and theirs) as far as the links need, keeping their
// length Gantt does; completed successors stay, moving earlier pulls nobody back.
func TestGanttShift(t *testing.T) {
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
	var boss, worker string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('b', 'Борис Г') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('w', 'Иван Петров') RETURNING id`).Scan(&worker))
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	d := func(day int) *time.Time { x := time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC); return &x }
	task := func(title string, from, to int) (string, string) {
		key, err := s.CreateTask(ctx, boss, p, NewTask{Title: title, ResponsibleID: worker})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		must(s.SetPlanDates(ctx, boss, id, d(from), d(to)))
		return id, key
	}
	a, ka := task("Проект", 2, 4)
	b, kb := task("Вёрстка", 5, 7)
	c, _ := task("Тесты", 5, 5)
	e, _ := task("Отчёт", 3, 4)
	done, _ := task("Готово", 1, 1)
	must(s.AddLink(ctx, boss, b, ka, ""))              // B after A
	must(s.AddLink(ctx, boss, c, kb, "start_start"))   // C starts with B
	must(s.AddLink(ctx, boss, e, ka, "finish_finish")) // E ends with A
	must(s.AddLink(ctx, boss, done, ka, ""))
	want := func(id string, from, to int) {
		t.Helper()
		cd, err := s.GetCard(ctx, boss, id)
		must(err)
		if !cd.StartPlan.Equal(*d(from)) || !cd.EndPlan.Equal(*d(to)) {
			t.Fatalf("%s: %v..%v, want %d..%d", cd.Title, cd.StartPlan, cd.EndPlan, from, to)
		}
	}
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'completed', closed_at = now() WHERE id = $1`, done)
	must(err)

	must(s.SetPlanDates(ctx, boss, a, d(4), d(6))) // two days later
	want(b, 7, 9)                                  // B after A, its three days kept
	want(c, 7, 7)                                  // C follows B down the chain
	want(e, 5, 6)                                  // E ends with A
	want(done, 1, 1)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'start_plan'`, b).Scan(&n))
	if n != 2 { // set, then shifted
		t.Fatalf("the shift is in the successor's history: %d", n)
	}

	must(s.SetPlanDates(ctx, boss, a, d(1), d(2))) // earlier: the successors stay
	want(b, 7, 9)
	want(e, 5, 6)

	// the task card moves the successors too
	ca, err := s.GetCard(ctx, boss, a)
	must(err)
	must(s.UpdateTask(ctx, boss, a, TaskUpdate{Version: ca.Version, Title: ca.Title, ResponsibleID: ca.ResponsibleID,
		StartPlan: d(8), EndPlan: d(10)}))
	want(b, 11, 13)
	want(c, 11, 11)
}
