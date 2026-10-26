package tasks

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/worktime"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRepeatNext(t *testing.T) {
	sched := worktime.Default()
	start := at("2026-10-12 00:00") // Monday
	ten := 3
	cases := []struct {
		name  string
		r     Repeat
		after string
		made  int
		want  string // "" = none
	}{
		{"daily at 9, later today", Repeat{Period: "daily", Every: 1, At: "09:00"}, "2026-10-13 08:00", 0, "2026-10-13 09:00"},
		{"daily, past today's time", Repeat{Period: "daily", Every: 1, At: "09:00"}, "2026-10-13 09:00", 0, "2026-10-14 09:00"},
		{"every 2 days from the start", Repeat{Period: "daily", Every: 2, At: "09:00"}, "2026-10-13 10:00", 0, "2026-10-14 09:00"},
		{"working days skip the weekend", Repeat{Period: "daily", Every: 1, WorkdaysOnly: true, At: "09:00"}, "2026-10-16 10:00", 0, "2026-10-19 09:00"},
		{"weekly Mon+Wed", Repeat{Period: "weekly", Every: 1, WeekDays: []int{1, 3}, At: "10:00"}, "2026-10-12 11:00", 0, "2026-10-14 10:00"},
		{"every 2 weeks on Monday", Repeat{Period: "weekly", Every: 2, WeekDays: []int{1}, At: "10:00"}, "2026-10-12 11:00", 0, "2026-10-26 10:00"},
		{"monthly on the 31st → last day", Repeat{Period: "monthly", Every: 1, MonthDay: 31, At: "18:00"}, "2026-11-01 00:00", 0, "2026-11-30 18:00"},
		{"yearly 5 March", Repeat{Period: "yearly", Every: 1, MonthDay: 5, YearMonth: 3, At: "09:00"}, "2026-10-13 00:00", 0, "2027-03-05 09:00"},
		{"ends on a date", Repeat{Period: "weekly", Every: 1, WeekDays: []int{1}, At: "09:00", EndsOn: ptrTime(at("2026-10-18 00:00"))}, "2026-10-12 10:00", 0, ""},
		{"N times done", Repeat{Period: "daily", Every: 1, At: "09:00", Times: &ten}, "2026-10-13 10:00", 3, ""},
		{"starts later", Repeat{Period: "daily", Every: 1, At: "09:00", StartsOn: at("2026-11-02 00:00")}, "2026-10-13 10:00", 0, "2026-11-02 09:00"},
	}
	for _, c := range cases {
		r := c.r
		r.On = true
		if r.StartsOn.IsZero() {
			r.StartsOn = start
		}
		got := r.Next(at(c.after), c.made, sched)
		s := ""
		if got != nil {
			s = got.Format("2006-01-02 15:04")
		}
		if s != c.want {
			t.Errorf("%s: next = %q, want %q", c.name, s, c.want)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// TestTemplates: a template saves with its schedule, makes a task with its fields (deadline counted from now,
// checklist, people), and a due regular template makes its task once and moves to the next run.
func TestTemplates(t *testing.T) {
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
	boss, worker, watcher, stranger := user("boss"), user("worker"), user("watcher"), user("stranger")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	day := 24 * 60
	tpl := Template{ProjectKey: "GO", Title: "Отчёт за неделю", Description: "Собрать цифры", ResponsibleID: worker,
		Priority: 2, DeadlineAfter: &day, RequiresControl: true, Auditors: []string{watcher, ""}, Checklist: []string{"Продажи", " ", "Расходы"},
		Repeat: Repeat{On: true, Period: "weekly", Every: 1, WeekDays: []int{}, At: "09:00"}}
	if _, err := s.SaveTemplate(ctx, boss, tpl); err == nil || !strings.Contains(err.Error(), "repeat_weekdays") {
		t.Fatalf("weekly without weekdays: %v", err)
	}
	tpl.Repeat.WeekDays = []int{1, 2, 3, 4, 5, 6, 7}
	id, err := s.SaveTemplate(ctx, boss, tpl)
	must(err)
	if _, err := s.SaveTemplate(ctx, stranger, Template{ProjectKey: "GO", Title: "x"}); err == nil {
		t.Fatal("a stranger saved a template into the project")
	}
	if _, err := s.Template(ctx, stranger, id); err != ErrForbidden {
		t.Fatalf("someone else's template: %v", err)
	}
	got, err := s.Template(ctx, boss, id)
	must(err)
	if got.NextRun == nil || !got.NextRun.After(time.Now()) || len(got.Checklist) != 2 || len(got.Auditors) != 1 {
		t.Fatalf("saved = %+v", got)
	}

	now := time.Now()
	key, err := s.CreateFromTemplate(ctx, boss, got, now)
	must(err)
	tid, _ := s.TaskIDByKey(ctx, key)
	c, err := s.GetCard(ctx, boss, tid)
	must(err)
	if c.Title != "Отчёт за неделю" || c.ResponsibleID != worker || c.Priority != 2 || !c.RequiresControl ||
		len(c.Checklist) != 2 || c.Checklist[0].Title != "Продажи" || len(c.Auditors) != 1 ||
		c.Deadline == nil || c.Deadline.Sub(now) < 23*time.Hour || c.Deadline.Sub(now) > 25*time.Hour {
		t.Fatalf("task from template = %+v", c)
	}

	// the run is due: one task, then the next run is in the future
	_, err = pool.Exec(ctx, `UPDATE task_templates SET next_run = now() - interval '3 days' WHERE id = $1`, id)
	must(err)
	made, errs := s.RunDue(ctx, time.Now())
	if len(errs) != 0 || len(made) != 1 {
		t.Fatalf("run due: made %v errs %v", made, errs)
	}
	got, _ = s.Template(ctx, boss, id)
	if got.CreatedCount != 1 || got.NextRun == nil || !got.NextRun.After(time.Now()) || got.LastRun == nil {
		t.Fatalf("after run: %+v", got)
	}
	var tplRef string
	must(pool.QueryRow(ctx, `SELECT template_id::text FROM tasks t JOIN projects p ON p.id = t.project_id WHERE p.key || '-' || t.number = $1`, made[0]).Scan(&tplRef))
	if tplRef != id {
		t.Fatalf("task not linked to its template: %q", tplRef)
	}
	if made, _ := s.RunDue(ctx, time.Now()); len(made) != 0 {
		t.Fatalf("ran twice: %v", made)
	}
	must(s.PauseTemplate(ctx, boss, id, false))
	got, _ = s.Template(ctx, boss, id)
	if got.Repeat.On || got.NextRun != nil {
		t.Fatalf("paused: %+v", got)
	}
	must(s.DeleteTemplate(ctx, boss, id))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE template_id IS NULL`).Scan(&n))
	if n != 2 {
		t.Fatalf("tasks of a deleted template: %d left unlinked, want 2", n)
	}
}

// TestTemplateSubtasks: «Подзадачи шаблона» — a task made from the base template (by hand and on its schedule) gets
// a subtask from each subtask template, deeper ones too; subtask templates never repeat and are not listed apart.
func TestTemplateSubtasks(t *testing.T) {
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
	boss, worker, stranger := user("boss"), user("worker"), user("stranger")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	base, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Новый сотрудник",
		Repeat: Repeat{On: true, Period: "daily", Every: 1, At: "09:00"}})
	must(err)
	two := 120
	acc, err := s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Учётная запись", ParentID: base,
		ResponsibleID: worker, DeadlineAfter: &two, Checklist: []string{"Почта"}, Repeat: Repeat{On: true, Period: "daily", Every: 1, At: "09:00"}})
	must(err)
	_, err = s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Пропуск", ParentID: acc})
	must(err)
	_, err = s.SaveTemplate(ctx, boss, Template{ProjectKey: "GO", Title: "Рабочее место", ParentID: base})
	must(err)
	if _, err := s.SaveTemplate(ctx, stranger, Template{ProjectKey: "GO", Title: "x", ParentID: base}); err == nil {
		t.Fatal("a stranger added a subtask template")
	}

	list, err := s.Templates(ctx, boss)
	must(err)
	if len(list) != 1 || list[0].ID != base || list[0].SubCount != 2 {
		t.Fatalf("list = %+v", list)
	}
	got, err := s.Template(ctx, boss, base)
	must(err)
	if len(got.Subtasks) != 2 || got.Subtasks[0].Title != "Учётная запись" || got.Subtasks[0].Repeat.On ||
		got.Subtasks[0].SubCount != 1 || got.Subtasks[1].Title != "Рабочее место" {
		t.Fatalf("subtasks = %+v", got.Subtasks)
	}
	sub, err := s.Template(ctx, boss, acc)
	must(err)
	if sub.ParentID != base || sub.ParentTitle != "Новый сотрудник" || sub.AfterHours() != 2 {
		t.Fatalf("subtask template = %+v", sub)
	}
	// saving it again keeps its base and does not make it regular
	sub.Repeat.On, sub.ParentID = true, ""
	_, err = s.SaveTemplate(ctx, boss, sub)
	must(err)
	if sub, _ = s.Template(ctx, boss, acc); sub.ParentID != base || sub.Repeat.On {
		t.Fatalf("resaved = %+v", sub)
	}

	children := func(key string) []string {
		t.Helper()
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		rows, err := pool.Query(ctx, `SELECT p.key || '-' || t.number || ' ' || t.title FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.parent_id = $1 ORDER BY t.number`, id)
		must(err)
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		must(err)
		return out
	}
	key, err := s.CreateFromTemplate(ctx, boss, got, time.Now())
	must(err)
	kids := children(key)
	if len(kids) != 2 || !strings.HasSuffix(kids[0], " Учётная запись") || !strings.HasSuffix(kids[1], " Рабочее место") {
		t.Fatalf("subtasks of %s = %v", key, kids)
	}
	if deeper := children(strings.Fields(kids[0])[0]); len(deeper) != 1 || !strings.HasSuffix(deeper[0], " Пропуск") {
		t.Fatalf("deeper = %v", deeper)
	}

	// on its schedule: the same subtasks
	_, err = pool.Exec(ctx, `UPDATE task_templates SET next_run = now() - interval '1 hour' WHERE id = $1`, base)
	must(err)
	made, errs := s.RunDue(ctx, time.Now())
	if len(errs) != 0 || len(made) != 1 || len(children(made[0])) != 2 {
		t.Fatalf("run due: made %v errs %v", made, errs)
	}

	// the base goes with its subtask templates; the tasks stay
	must(s.DeleteTemplate(ctx, boss, base))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_templates`).Scan(&n))
	if n != 0 || len(children(key)) != 2 {
		t.Fatalf("after delete: %d templates", n)
	}
}

// TestTemplateNewUsers: «Шаблон для новых сотрудников» — each employee who comes after the mark gets the task once,
// with the template's subtasks, the author as the one who set it; earlier employees, guests, dismissed people and
// imported ones who never signed in get nothing; the template never repeats; the mark taken off and put back on
// again starts from then.
func TestTemplateNewUsers(t *testing.T) {
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
	user := func(sub string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, sub).Scan(&id))
		return id
	}
	hr, old := user("hr"), user("old")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, hr, "HR", "hr", "")
	must(err)
	day := 24 * 60
	id, err := s.SaveTemplate(ctx, hr, Template{ProjectKey: "HR", Title: "Добро пожаловать", DeadlineAfter: &day,
		Checklist: []string{"Прочитать регламент"}, ForNewUsers: true, Repeat: Repeat{On: true, Period: "daily", Every: 1, At: "09:00"}})
	must(err)
	_, err = s.SaveTemplate(ctx, hr, Template{ProjectKey: "HR", Title: "Получить ноутбук", ParentID: id, ForNewUsers: true})
	must(err)
	got, err := s.Template(ctx, hr, id)
	must(err)
	if !got.ForNewUsers || got.Repeat.On || got.NextRun != nil || got.Subtasks[0].ForNewUsers {
		t.Fatalf("saved = %+v", got)
	}
	if err := s.PauseTemplate(ctx, hr, id, true); err == nil {
		t.Fatal("a template for new employees made regular")
	}

	newbie, guest, gone, imported := user("newbie"), user("guest"), user("gone"), user("legacy:77")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, gone)
	must(err)
	_, _ = old, imported
	made, errs := s.RunNewUsers(ctx, time.Now())
	if len(errs) != 0 || len(made) != 1 {
		t.Fatalf("made %v errs %v", made, errs)
	}
	tid, _ := s.TaskIDByKey(ctx, made[0])
	c, err := s.GetCard(ctx, hr, tid)
	must(err)
	if c.Title != "Добро пожаловать" || c.ResponsibleID != newbie || c.CreatorID != hr || c.Deadline == nil || len(c.Checklist) != 1 {
		t.Fatalf("task = %+v", c)
	}
	var kids int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id = $1`, tid).Scan(&kids))
	if kids != 1 {
		t.Fatalf("subtasks: %d", kids)
	}
	if made, _ := s.RunNewUsers(ctx, time.Now()); len(made) != 0 {
		t.Fatalf("set twice: %v", made)
	}
	if got, _ = s.Template(ctx, hr, id); got.CreatedCount != 1 {
		t.Fatalf("count = %d", got.CreatedCount)
	}

	// the mark off and on again: who came in between does not get it, who comes after does
	got.ForNewUsers = false
	_, err = s.SaveTemplate(ctx, hr, got)
	must(err)
	user("between")
	got.ForNewUsers = true
	_, err = s.SaveTemplate(ctx, hr, got)
	must(err)
	if made, _ := s.RunNewUsers(ctx, time.Now()); len(made) != 0 {
		t.Fatalf("came while off: %v", made)
	}
	later := user("later")
	made, _ = s.RunNewUsers(ctx, time.Now())
	if len(made) != 1 {
		t.Fatalf("later: %v", made)
	}
	var resp string
	must(pool.QueryRow(ctx, `SELECT t.responsible_id::text FROM task_template_hires h JOIN tasks t ON t.id = h.task_id
		WHERE h.template_id = $1 AND h.user_id = $2`, id, later).Scan(&resp))
	if resp != later {
		t.Fatalf("responsible %s, want %s", resp, later)
	}
}
