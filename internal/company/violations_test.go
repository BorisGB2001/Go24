package company

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/worktime"
)

// TestNotifyViolations: a late start, an early finish and a short day are told to the manager once each; a person
// with a schedule is told about by its rules; nobody is told when the rules name nobody.
func TestNotifyViolations(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, hr := user("boss", "Борис"), user("anna", "Анна"), user("hr", "Кадры")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Склад', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, dept)
	must(err)
	s := NewStore(pool)
	sched := worktime.Schedule{Start: "09:00", End: "18:00", LeaveBefore: "18:00", MinDuration: "08:00", NotifyHead: true}
	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local) // a Tuesday
	at := func(d time.Time, h, m int) time.Time {
		return d.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	tell := func(now time.Time, want ...string) {
		t.Helper()
		got, err := s.NotifyViolations(ctx, anna, sched, now)
		must(err)
		if !slices.Equal(got, want) {
			t.Fatalf("told %v, want %v", got, want)
		}
	}
	inbox := func(who string) (list []string) {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'workday_violation' ORDER BY id`, who)
		must(err)
		for rows.Next() {
			var b string
			must(rows.Scan(&b))
			list = append(list, b)
		}
		return list
	}

	tell(at(day, 9, 0)) // no day yet
	must(s.WorkAction(ctx, anna, "start", "", at(day, 9, 40), Place{}))
	tell(at(day, 9, 40), "late")
	tell(at(day, 9, 41)) // told once
	must(s.WorkAction(ctx, anna, "finish", "", at(day, 15, 0), Place{}))
	tell(at(day, 15, 0), "early", "short")
	must(s.WorkAction(ctx, anna, "reopen", "", at(day, 15, 10), Place{}))
	must(s.WorkAction(ctx, anna, "finish", "", at(day, 15, 30), Place{}))
	tell(at(day, 15, 30)) // finished again after «Продолжить» — not repeated
	if got := inbox(boss); !slices.Equal(got, []string{"late|13.10.2026", "early|13.10.2026", "short|13.10.2026"}) {
		t.Errorf("the manager got %v", got)
	}
	if got := inbox(anna); got != nil {
		t.Errorf("the person is told of their own violations: %v", got)
	}

	// the schedule's rules: not the manager, the HR officer
	ws := &WorkSchedule{Name: "Склад", Kind: "fixed", Times: []ScheduleTime{{Days: []int{1, 2, 3, 4, 5}, Start: "08:00", End: "17:00"}},
		NotifyTo: []string{hr, hr, "junk"}, Members: []string{"dept:" + dept}}
	must(s.SaveWorkSchedule(ctx, boss, ws))
	got, err := s.WorkSchedule(ctx, ws.ID)
	must(err)
	if got.NotifyHead || !slices.Equal(got.NotifyTo, []string{hr}) {
		t.Fatalf("schedule notify: %v %v", got.NotifyHead, got.NotifyTo)
	}
	next := day.AddDate(0, 0, 1)
	must(s.WorkAction(ctx, anna, "start", "", at(next, 8, 30), Place{}))
	tell(at(next, 8, 30), "late")
	if got := inbox(hr); !slices.Equal(got, []string{"late|14.10.2026"}) {
		t.Errorf("the HR officer got %v", got)
	}
	if got := inbox(boss); len(got) != 3 {
		t.Errorf("the manager is told by a schedule without them: %v", got)
	}

	// nobody to tell: the violation is not even remembered
	ws.NotifyTo = nil
	must(s.SaveWorkSchedule(ctx, boss, ws))
	third := day.AddDate(0, 0, 2)
	must(s.WorkAction(ctx, anna, "start", "", at(third, 9, 0), Place{}))
	tell(at(third, 9, 0))
	var told []string
	must(pool.QueryRow(ctx, `SELECT violations_told FROM workdays WHERE user_id = $1 AND day = $2`, anna, third.Format(time.DateOnly)).Scan(&told))
	if len(told) != 0 {
		t.Errorf("told %v with nobody to tell", told)
	}
}
