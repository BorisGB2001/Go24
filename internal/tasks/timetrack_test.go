package tasks

import (
	"context"
	"errors"
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestTimeTracking: «Учитывать время, затраченное на задачу» and «Плановые трудозатраты». Without the
// box there is no timer and no new entry (the old ones stay); the estimate comes with a new task and a template; the
// time logged with a running timer passing the estimate notifies the responsible and the creator once, a new estimate
// arms it again; a completed task is left alone.
func TestTimeTracking(t *testing.T) {
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
	boss, dev := newUser("boss"), newUser("dev")
	pk, err := s.CreateProject(ctx, boss, "tt", "Учёт времени", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, boss, pk)
	must(err)
	add := func(in NewTask) string {
		t.Helper()
		in.ResponsibleID = dev
		key, err := s.CreateTask(ctx, boss, proj, in)
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	card := func(id string) TaskCard {
		t.Helper()
		c, err := s.GetCard(ctx, boss, id)
		must(err)
		return c
	}
	errKey := func(err error) string {
		var e *i18n.Error
		if errors.As(err, &e) {
			return e.Key
		}
		return ""
	}
	notes := func(id string) map[string]int {
		t.Helper()
		out := map[string]int{}
		rows, err := pool.Query(ctx, `SELECT user_id::text FROM notifications WHERE task_id = $1 AND kind = 'task_time_over'`, id)
		must(err)
		defer rows.Close()
		for rows.Next() {
			var u string
			must(rows.Scan(&u))
			out[u]++
		}
		return out
	}
	day := time.Now()

	// the box off: no timer, no new entries; turning it on brings them back
	off := add(NewTask{Title: "Без учёта", Params: Params{NoTimeTracking: true}})
	if c := card(off); !c.NoTimeTracking {
		t.Fatal("the box is not kept")
	}
	if err := s.StartTimer(ctx, dev, off); errKey(err) != "err.time_tracking_off" {
		t.Fatalf("a timer without time tracking: %v", err)
	}
	if err := s.AddTime(ctx, dev, off, 600, day, ""); errKey(err) != "err.time_tracking_off" {
		t.Fatalf("an entry without time tracking: %v", err)
	}
	c := card(off)
	p := c.Params
	p.NoTimeTracking = false
	must(s.UpdateTask(ctx, boss, off, TaskUpdate{Version: c.Version, Title: c.Title, Params: p}))
	must(s.AddTime(ctx, dev, off, 600, day, "сделано"))
	must(s.StartTimer(ctx, dev, off))
	must(s.StopTimer(ctx, dev))
	c = card(off)
	p.NoTimeTracking = true
	must(s.UpdateTask(ctx, boss, off, TaskUpdate{Version: c.Version, Title: c.Title, Params: p}))
	if _, total, err := s.TaskTime(ctx, off); err != nil || total < 600 {
		t.Fatalf("the entries made before are gone: %d %v", total, err)
	}

	// the estimate of a new task and of a template
	est := 60
	over := add(NewTask{Title: "С оценкой", EstimateMinutes: &est})
	if c := card(over); c.EstimateMinutes == nil || *c.EstimateMinutes != 60 || c.NoTimeTracking {
		t.Fatalf("a new task's estimate: %v, tracking off %v", c.EstimateMinutes, c.NoTimeTracking)
	}
	tplID, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: pk, Title: "Шаблон", ResponsibleID: dev, Priority: 1,
		EstimateMinutes: &est, Params: Params{NoTimeTracking: true}})
	must(err)
	tpl, err := s.Template(ctx, boss, tplID)
	must(err)
	if in := tpl.NewTask(time.Now()); in.EstimateMinutes == nil || *in.EstimateMinutes != 60 || !in.NoTimeTracking {
		t.Fatalf("a template's estimate and box: %v %v", in.EstimateMinutes, in.NoTimeTracking)
	}

	// passing the estimate: 50 minutes logged, a timer running 15 minutes more
	must(s.AddTime(ctx, dev, over, 50*60, day, ""))
	if n, err := s.TimeOverDue(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("under the estimate: %d %v", n, err)
	}
	must(s.StartTimer(ctx, dev, over))
	if _, err := pool.Exec(ctx, `UPDATE task_timers SET started_at = now() - interval '15 minutes' WHERE user_id = $1`, dev); err != nil {
		t.Fatal(err)
	}
	if n, err := s.TimeOverDue(ctx, time.Now()); err != nil || n != 1 {
		t.Fatalf("over the estimate: %d %v", n, err)
	}
	if got := notes(over); got[dev] != 1 || got[boss] != 1 {
		t.Fatalf("who was told: %v", got)
	}
	if n, _ := s.TimeOverDue(ctx, time.Now()); n != 0 {
		t.Fatal("told twice")
	}
	// a new estimate arms it again
	c = card(over)
	est2 := 62 // 65 minutes logged with the timer: still passed
	must(s.UpdateTask(ctx, boss, over, TaskUpdate{Version: c.Version, Title: c.Title, ResponsibleID: c.ResponsibleID, Priority: c.Priority, EstimateMinutes: &est2, Params: c.Params}))
	if n, _ := s.TimeOverDue(ctx, time.Now()); n != 1 {
		t.Fatal("a new estimate passed too is not told")
	}
	// a completed task, a task without time tracking — nothing
	must(s.StopTimer(ctx, dev))
	done := add(NewTask{Title: "Готово", EstimateMinutes: &est})
	must(s.AddTime(ctx, dev, done, 2*3600, day, ""))
	must(s.ApplyAction(ctx, dev, done, ActionComplete))
	if n, _ := s.TimeOverDue(ctx, time.Now()); n != 0 {
		t.Fatal("a completed task is told")
	}
}
