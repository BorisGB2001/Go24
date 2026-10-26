package tasks

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEfficiencyScore: the rules of a violation within a period, without a database.
func TestEfficiencyScore(t *testing.T) {
	d := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.UTC) }
	p := func(t time.Time) *time.Time { return &t }
	from, end := d(1, 0), d(17, 0)
	list := []effTask{
		{ID: "ok", Created: d(2, 9), Deadline: p(d(10, 18)), ClosedAt: p(d(9, 12))},                       // closed in time
		{ID: "late", Created: d(2, 9), Deadline: p(d(5, 18)), ClosedAt: p(d(6, 12))},                      // closed late
		{ID: "open-late", Created: d(2, 9), Deadline: p(d(12, 18))},                                       // still open, overdue
		{ID: "old-late", Created: d(1, 0).AddDate(0, -1, 0), Deadline: p(d(1, 0).AddDate(0, 0, -3))},      // overdue since last month
		{ID: "future", Created: d(2, 9), Deadline: p(d(20, 18))},                                          // not due yet
		{ID: "closed-before", Created: d(1, 0).AddDate(0, -1, 0), ClosedAt: p(d(1, 0).AddDate(0, 0, -1))}, // not in work
		{ID: "rework", Created: d(2, 9), reworks: []time.Time{d(8, 10)}},
		{ID: "after", Created: d(18, 9)}, // made after the period
	}
	total, violated, vs := score(list, from, end)
	if total != 6 || violated != 4 {
		t.Fatalf("total %d violated %d: %+v", total, violated, vs)
	}
	if percent(total, violated) != 33 || percent(0, 0) != 100 {
		t.Fatalf("percent %d", percent(total, violated))
	}
	for _, v := range vs {
		if v.TaskID == "old-late" && !v.At.Equal(from) {
			t.Fatalf("overdue from before the period counts from its start: %v", v.At)
		}
	}
	// before the deadline of the 5th only the task overdue since last month is violated
	if total, violated, _ := score(list, from, d(5, 17)); violated != 1 || total != 6 {
		t.Fatalf("on the 5th: %d of %d", violated, total)
	}
}

// TestEfficiency: only the responsible's tasks set by someone else count; overdue and rework are violations;
// «Не учитывать в эффективности» leaves a task out (the creator sets it, the responsible may not).
func TestEfficiency(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, worker := newUser("boss"), newUser("worker")
	p, err := s.CreateProject(ctx, boss, "EF", "Эффективность", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, p)
	must(err)
	must(s.SetMember(ctx, proj, worker, "member"))
	now := time.Now()
	past := now.Add(-2 * time.Hour)
	create := func(by, title string, deadline *time.Time, control bool) string {
		key, err := s.CreateTask(ctx, by, proj, NewTask{Title: title, ResponsibleID: worker, Deadline: deadline, RequiresControl: control})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	create(boss, "В срок", nil, false)
	late := create(boss, "Просрочена", &past, false)
	rework := create(boss, "На доработку", nil, true)
	create(worker, "Себе", &past, false) // set to oneself: not counted
	must(s.ApplyAction(ctx, worker, rework, ActionComplete))
	must(s.ApplyAction(ctx, boss, rework, ActionDisapprove))

	from := now.Add(-24 * time.Hour)
	e, err := s.EfficiencyOf(ctx, worker, from, now.Add(time.Hour), now.Add(time.Minute))
	must(err)
	if e.Total != 3 || e.Violated != 2 || e.Percent != 33 || len(e.Violations) != 2 || len(e.Days) == 0 {
		t.Fatalf("efficiency: %+v", e)
	}
	if err := s.SetNoEfficiency(ctx, worker, late, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("the responsible leaves a task out: %v", err)
	}
	must(s.SetNoEfficiency(ctx, boss, late, true))
	e, err = s.EfficiencyOf(ctx, worker, from, now.Add(time.Hour), now.Add(time.Minute))
	must(err)
	if e.Total != 2 || e.Violated != 1 || e.Percent != 50 {
		t.Fatalf("without the late task: %+v", e)
	}
	if tk, _ := s.TaskByID(ctx, late); !tk.NoEfficiency {
		t.Fatal("flag not saved")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'no_efficiency'`, late).Scan(&n))
	if n != 1 {
		t.Fatalf("history rows %d", n)
	}
	// the boss has no tasks of their own: 100 %
	if e, err := s.EfficiencyOf(ctx, boss, from, now, now); err != nil || e.Percent != 100 || e.Total != 0 {
		t.Fatalf("no tasks: %+v %v", e, err)
	}
}
