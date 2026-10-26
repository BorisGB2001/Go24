package tasks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestStatusRobots checks the robots of «Мои задачи» (on the task statuses) against a real PostgreSQL.
func TestStatusRobots(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	boss, resp, other := newUser("boss"), newUser("resp"), newUser("other")
	s := NewStore(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	run := func(at time.Time) int {
		t.Helper()
		n, err := s.RunRobotsDue(ctx, at)
		must(err)
		return n
	}
	_, err = s.CreateProject(ctx, boss, "SR", "status robots", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "SR")
	must(err)
	must(s.SetMember(ctx, p, resp, "member"))
	must(s.SetMember(ctx, p, other, "member"))
	po, err := s.ProjectByKey(ctx, other, "SR")
	must(err)

	// no stage robots in «Мои задачи», no unknown status
	if err := s.AddStatusRobot(ctx, boss, StatusPending, "stage", RobotParams{Stage: "x"}); err == nil {
		t.Fatal("a stage robot on a status")
	}
	if err := s.AddStatusRobot(ctx, boss, "nope", "comment", RobotParams{Text: "x"}); err == nil {
		t.Fatal("a robot on an unknown status")
	}
	must(s.AddStatusRobot(ctx, boss, StatusPending, "auditor", RobotParams{To: "creator"}))
	must(s.AddStatusRobot(ctx, boss, StatusPending, "checklist", RobotParams{Text: "Прочитать {title}"}))
	must(s.AddStatusRobot(ctx, boss, StatusInProgress, "deadline", RobotParams{Days: 2}))
	must(s.AddStatusRobot(ctx, boss, StatusCompleted, "notify", RobotParams{Text: "Сделано: {title}", To: "creator", Delay: 1, Unit: "hour"}))
	robots, err := s.StatusRobots(ctx, boss)
	must(err)
	if len(robots) != 4 || robots[0].Status != StatusPending {
		t.Fatalf("robots = %+v", robots)
	}
	if mine, _ := s.StatusRobots(ctx, other); len(mine) != 0 {
		t.Fatalf("another's robots = %d", len(mine))
	}

	// a new task of the boss comes to «Ждёт выполнения»: the boss is an auditor, a checklist item
	key, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Отчёт", ResponsibleID: resp})
	must(err)
	id, _ := s.TaskIDByKey(ctx, key)
	if n := run(time.Now()); n != 2 {
		t.Fatalf("pending ran %d robots", n)
	}
	c, err := s.GetCard(ctx, boss, id)
	must(err)
	if len(c.Auditors) != 1 || c.Auditors[0].ID != boss || len(c.Checklist) != 1 || c.Checklist[0].Title != "Прочитать Отчёт" {
		t.Fatalf("after pending: auditors %+v, checklist %+v", c.Auditors, c.Checklist)
	}

	// another's task: the boss's robots stay out
	_, err = s.CreateTask(ctx, other, po, NewTask{Title: "Чужая", ResponsibleID: resp})
	must(err)
	if n := run(time.Now()); n != 0 {
		t.Fatalf("robots ran in another's task: %d", n)
	}

	// the responsible starts it: a deadline in two days
	must(s.ApplyAction(ctx, resp, id, ActionStart))
	if n := run(time.Now()); n != 1 {
		t.Fatalf("in progress ran %d robots", n)
	}
	if c, _ = s.GetCard(ctx, boss, id); c.Deadline == nil || c.Deadline.Before(time.Now().Add(24*time.Hour)) {
		t.Fatalf("deadline = %v", c.Deadline)
	}

	// completed: the notification waits an hour; renewed before that — nothing
	must(s.ApplyAction(ctx, resp, id, ActionComplete))
	run(time.Now())
	must(s.ApplyAction(ctx, boss, id, ActionRenew))
	run(time.Now().Add(2 * time.Hour))
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_robot' AND body = 'Сделано: Отчёт'`).Scan(&notes))
	if notes != 0 {
		t.Fatalf("a waiting robot ran after the status changed: %d", notes)
	}
	must(s.ApplyAction(ctx, resp, id, ActionComplete))
	run(time.Now().Add(2 * time.Hour))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_robot' AND user_id = $1 AND body = 'Сделано: Отчёт'`, boss).Scan(&notes))
	if notes != 1 {
		t.Fatalf("completed notifications = %d", notes)
	}

	// only the owner turns off or removes a robot; a ring of status robots stops
	must(s.SetStatusRobotActive(ctx, other, robots[0].ID, false))
	must(s.DeleteStatusRobot(ctx, other, robots[1].ID))
	if again, _ := s.StatusRobots(ctx, boss); len(again) != 4 || !again[0].Active {
		t.Fatalf("another changed the robots: %+v", again)
	}
	for _, r := range robots {
		must(s.DeleteStatusRobot(ctx, boss, r.ID))
	}
	must(s.AddStatusRobot(ctx, boss, StatusPending, "status", RobotParams{Status: ActionStart}))
	must(s.AddStatusRobot(ctx, boss, StatusInProgress, "status", RobotParams{Status: ActionPause}))
	rkey, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Круг", ResponsibleID: resp})
	must(err)
	rid, _ := s.TaskIDByKey(ctx, rkey)
	for i := 0; i < 20 && run(time.Now()) > 0; i++ {
	}
	var moves int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'status'`, rid).Scan(&moves))
	if moves == 0 || moves > maxRobotDepth+1 {
		t.Fatalf("ring status changes = %d", moves)
	}
}
