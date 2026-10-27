package bp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestTaskStep: «Задача» — the description with the fields filled, accomplices and observers, «важная»,
// acceptance, a checklist, {created_task}; «Ожидать завершения задачи» keeps the process (and a parallel branch) on the
// step until the task is completed or deleted.
func TestTaskStep(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	ann, ivan, olga := newUser("ann"), newUser("ivan"), newUser("olga")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, ann, "OPS", "Работы", "")
	must(err)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	fields := []Field{{Key: "topic", Label: "Тема", Kind: "string"}, {Key: "who", Label: "Кто", Kind: "user"}}

	var ve *i18n.Error
	for name, st := range map[string]Step{
		"a notification waits": {Kind: "notify", Text: "x", Wait: true},
		"observers of a chat":  {Kind: "chat", Text: "x", Watchers: []string{ivan}},
		"a string observer":    {Kind: "task", Text: "x", Project: "OPS", Watchers: []string{FieldPrefix + "topic"}},
	} {
		if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: fields, Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Работа", Active: true, Fields: fields, Steps: []Step{
		{Kind: "task", Text: "Сделать: {topic}", Note: "Подробно о {topic}", To: ivan, Project: "OPS", Days: 2,
			Users: []string{FieldPrefix + "who", ivan}, Watchers: []string{"starter"}, High: true, Control: true,
			Items: []string{"Начать {topic}", "Закончить"}, Wait: true},
		{Kind: "log", Text: "Задача {created_task} готова"},
		{Kind: "parallel", Branches: []int{5}, Goto: 6},
		{Kind: "task", Text: "Ветка", To: olga, Project: "OPS", Wait: true},
		{Kind: "notify", To: ann, Text: "Вторая ветка"},
		{Kind: "log", Text: "Всё"},
	}})
	must(err)
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"topic": "отчёт", "who": olga})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Status != "running" || x.Step != 0 || !strings.HasPrefix(x.Values["created_task"], "/tasks/OPS-") {
		t.Fatalf("waits on the task: %s %d %v", x.Status, x.Step, x.Values)
	}
	var task, title, desc, resp string
	var prio int
	var control bool
	must(pool.QueryRow(ctx, `SELECT id, title, description, responsible_id, priority, requires_control FROM tasks
		WHERE project_id = (SELECT id FROM projects WHERE key = 'OPS') AND title LIKE 'Сделать%'`).Scan(&task, &title, &desc, &resp, &prio, &control))
	if title != "Сделать: отчёт" || !strings.HasPrefix(desc, "Подробно о отчёт\n\nРабота: /bp/i/") || resp != ivan || prio != 2 || !control {
		t.Fatalf("task: %q %q %s %d %v", title, desc, resp, prio, control)
	}
	var roles string
	must(pool.QueryRow(ctx, `SELECT string_agg(role || ':' || (SELECT name FROM users WHERE id = user_id), ',' ORDER BY role, user_id)
		FROM task_participants WHERE task_id = $1 AND role IN ('accomplice', 'auditor')`, task).Scan(&roles))
	if roles != "accomplice:olga,auditor:ann" { // the responsible is not an accomplice too
		t.Fatalf("participants: %s", roles)
	}
	var items int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_checklist_items WHERE task_id = $1 AND title IN ('Начать отчёт', 'Закончить')`, task).Scan(&items))
	if items != 2 {
		t.Fatalf("checklist: %d", items)
	}
	// awaiting control is not completed yet
	_, err = pool.Exec(ctx, `UPDATE tasks_all SET status = 'awaiting_control' WHERE id = $1`, task)
	must(err)
	must(errors.Join(s.WakeDue(ctx, time.Now())...))
	if x, _ = s.Instance(ctx, ann, id); x.Step != 0 {
		t.Fatalf("awaiting control: step %d", x.Step)
	}
	_, err = pool.Exec(ctx, `UPDATE tasks_all SET status = 'completed' WHERE id = $1`, task)
	must(err)
	must(errors.Join(s.WakeDue(ctx, time.Now())...))
	if x, _ = s.Instance(ctx, ann, id); x.Status != "running" || x.Step != 2 || len(x.Branches) != 2 {
		t.Fatalf("after the task: %s %d %v", x.Status, x.Step, x.Branches)
	}
	// the branch waits for its task; the other is over
	var branch string
	must(pool.QueryRow(ctx, `SELECT id FROM tasks WHERE title = 'Ветка'`).Scan(&branch))
	must(errors.Join(s.WakeDue(ctx, time.Now())...))
	if x, _ = s.Instance(ctx, ann, id); x.Status != "running" || x.Step != 2 {
		t.Fatalf("the branch waits: %s %d", x.Status, x.Step)
	}
	// deleted: the process goes on
	_, err = pool.Exec(ctx, `UPDATE tasks_all SET deleted_at = now(), deleted_root = id WHERE id = $1`, branch)
	must(err)
	must(errors.Join(s.WakeDue(ctx, time.Now())...))
	if x, _ = s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("after the branch: %s %d", x.Status, x.Step)
	}
	var log string
	must(pool.QueryRow(ctx, `SELECT string_agg(text, '|' ORDER BY id) FROM bp_log WHERE instance_id = $1 AND kind = 'report'`, id).Scan(&log))
	if !strings.HasPrefix(log, "Задача /tasks/OPS-") || !strings.HasSuffix(log, " готова|Всё") {
		t.Fatalf("log: %s", log)
	}
}
