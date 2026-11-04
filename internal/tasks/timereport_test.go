package tasks

import (
	"context"
	"testing"
	"time"
)

// TestTimeReport: «Затраченное время» — the time of each person in the period, per person and per task;
// the days at the ends are in, the others are not; a project narrows it; a task whose whole time passed the planned
// effort counts as over; a task in the trash is left out.
func TestTimeReport(t *testing.T) {
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
	boss, dev, idle := newUser("boss"), newUser("dev"), newUser("idle")
	project := func(key string) Project {
		pk, err := s.CreateProject(ctx, boss, key, key, "")
		must(err)
		p, err := s.ProjectByKey(ctx, boss, pk)
		must(err)
		must(s.SetMember(ctx, p, dev, "member"))
		return p
	}
	pa, pb := project("tra"), project("trb")
	add := func(p Project, title string, estimate int) string {
		t.Helper()
		in := NewTask{Title: title, ResponsibleID: dev}
		if estimate > 0 {
			in.EstimateMinutes = &estimate
		}
		key, err := s.CreateTask(ctx, boss, p, in)
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.Local) }
	a1, a2, b1, gone := add(pa, "Вёрстка", 60), add(pa, "Тесты", 0), add(pb, "Сборка", 0), add(pa, "В корзине", 0)
	must(s.AddTime(ctx, dev, a1, 3600, day(1), ""))
	must(s.AddTime(ctx, dev, a1, 1800, day(31), ""))
	must(s.AddTime(ctx, boss, a1, 600, day(5), ""))
	must(s.AddTime(ctx, dev, a2, 900, day(10), ""))
	must(s.AddTime(ctx, dev, a2, 900, day(9).AddDate(0, -1, 0), "")) // September
	must(s.AddTime(ctx, dev, b1, 1200, day(20), ""))
	must(s.AddTime(ctx, dev, gone, 7200, day(20), ""))
	_, err := pool.Exec(ctx, `UPDATE tasks_all SET deleted_at = now() WHERE id = $1`, gone)
	must(err)

	users := []string{boss, dev, idle}
	rows, err := s.TimeReport(ctx, users, day(1), day(31).AddDate(0, 0, 1), "")
	must(err)
	if len(rows) != 2 || rows[0].UserID != dev || rows[0].Seconds != 3600+1800+900+1200 || rows[0].Tasks != 3 || rows[0].Over != 1 ||
		rows[1].UserID != boss || rows[1].Seconds != 600 || rows[1].Over != 1 {
		t.Fatalf("report %+v", rows)
	}
	rows, err = s.TimeReport(ctx, users, day(2), day(31), pa.ID) // 2–30 October, project A
	must(err)
	if len(rows) != 2 || rows[0].UserID != dev || rows[0].Seconds != 900 || rows[0].Tasks != 1 || rows[0].Over != 0 {
		t.Fatalf("narrowed %+v", rows)
	}

	list, err := s.TimeReportTasks(ctx, dev, day(1), day(31).AddDate(0, 0, 1), "")
	must(err)
	if len(list) != 3 || list[0].ID != a1 || list[0].Seconds != 5400 || list[0].TotalSeconds != 6000 || !list[0].Over() ||
		list[0].Key() != list[0].ProjectKey+"-1" || list[1].ID != b1 || list[2].ID != a2 || list[2].TotalSeconds != 1800 || list[2].Over() {
		t.Fatalf("tasks %+v", list)
	}
}
