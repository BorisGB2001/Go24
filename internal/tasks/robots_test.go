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

// TestRobots checks the robots of kanban stages against a real PostgreSQL (see TestStoreScenario).
func TestRobots(t *testing.T) {
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
	owner, resp, boss := newUser("owner"), newUser("resp"), newUser("boss")
	s := NewStore(pool)
	var comments []string
	s.Comment = func(ctx context.Context, userID, taskID, body string) error {
		comments = append(comments, body)
		_, err := s.PostComment(ctx, userID, taskID, body)
		return err
	}
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

	_, err = s.CreateProject(ctx, owner, "RB", "robots", "")
	must(err)
	p, err := s.ProjectByKey(ctx, owner, "RB")
	must(err)
	must(s.SetMember(ctx, p, resp, "member"))
	pm, err := s.ProjectByKey(ctx, resp, "RB")
	must(err)
	must(s.AddProjectStage(ctx, p, "Проверка", ""))
	st, err := s.Stages(ctx, p.ID)
	must(err)
	newSt, work, check, finish := st[0].ID, st[1].ID, st[2].ID, st[3].ID

	// only the owner and managers set robots; a robot needs its text, a known person, another stage of the project
	if err := s.AddRobot(ctx, resp, pm, work, "comment", RobotParams{Text: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member adds a robot: %v", err)
	}
	if err := s.AddRobot(ctx, owner, p, work, "comment", RobotParams{}); err == nil {
		t.Fatal("a comment robot without text")
	}
	if err := s.AddRobot(ctx, owner, p, work, "stage", RobotParams{Stage: work}); err == nil {
		t.Fatal("a stage robot to its own stage")
	}
	if err := s.AddRobot(ctx, owner, p, work, "auditor", RobotParams{To: "nobody"}); err == nil {
		t.Fatal("a robot for an unknown person")
	}

	must(s.AddRobot(ctx, owner, p, newSt, "notify", RobotParams{Text: "Новая: {title}", To: "creator"}))
	must(s.AddRobot(ctx, owner, p, work, "comment", RobotParams{Text: "Взята в работу, ответственный {responsible}"}))
	must(s.AddRobot(ctx, owner, p, work, "auditor", RobotParams{To: boss}))
	must(s.AddRobot(ctx, owner, p, work, "deadline", RobotParams{Days: 3}))
	must(s.AddRobot(ctx, owner, p, work, "checklist", RobotParams{Text: "Проверить {title}"}))
	must(s.AddRobot(ctx, owner, p, work, "task", RobotParams{Text: "Отчёт по {title}", To: "creator", Sub: true}))
	must(s.AddRobot(ctx, owner, p, work, "stage", RobotParams{Stage: check, Delay: 1, Unit: "hour"}))
	must(s.AddRobot(ctx, owner, p, check, "status", RobotParams{Status: ActionComplete}))
	must(s.AddRobot(ctx, owner, p, finish, "notify", RobotParams{Text: "Готово: {title}", To: "creator"}))
	robots, err := s.Robots(ctx, p.ID)
	must(err)
	if len(robots) != 9 {
		t.Fatalf("robots = %d", len(robots))
	}

	// a new task enters the first stage: its robots run
	key, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Сайт", ResponsibleID: resp})
	must(err)
	id, _ := s.TaskIDByKey(ctx, key)
	if n := run(time.Now()); n != 1 {
		t.Fatalf("new stage ran %d robots", n)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_robot' AND user_id = $1 AND body = 'Новая: Сайт'`, owner).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notifications = %d", notes)
	}

	// the responsible drags it to work: comment, auditor, deadline, checklist, a subtask; the stage robot waits
	must(s.MoveToStage(ctx, resp, id, work))
	if n := run(time.Now()); n != 5 {
		t.Fatalf("work stage ran %d robots", n)
	}
	c, err := s.GetCard(ctx, owner, id)
	must(err)
	if len(comments) != 1 || comments[0] != "Взята в работу, ответственный resp" {
		t.Fatalf("comments = %q", comments)
	}
	if len(c.Auditors) != 1 || c.Auditors[0].ID != boss {
		t.Fatalf("auditors = %+v", c.Auditors)
	}
	if c.Deadline == nil || c.Deadline.Before(time.Now().Add(48*time.Hour)) {
		t.Fatalf("deadline = %v", c.Deadline)
	}
	if len(c.Checklist) != 1 || c.Checklist[0].Title != "Проверить Сайт" {
		t.Fatalf("checklist = %+v", c.Checklist)
	}
	if len(c.Subtasks) != 1 || c.Subtasks[0].Title != "Отчёт по Сайт" || c.Subtasks[0].ResponsibleID != owner {
		t.Fatalf("subtasks = %+v", c.Subtasks)
	}
	if sid, _ := s.TaskByID(ctx, id); sid.StageID != work {
		t.Fatalf("the waiting robot ran early: %s", sid.StageID)
	}
	run(time.Now()) // the subtask entered the first stage: its notify robot

	// an hour later the task goes to «Проверка», whose robot completes it; the finish stage notifies the creator
	run(time.Now().Add(2 * time.Hour))
	run(time.Now().Add(2 * time.Hour))
	run(time.Now().Add(2 * time.Hour))
	tk, err := s.TaskByID(ctx, id)
	must(err)
	if tk.Status != StatusCompleted || tk.StageID != finish {
		t.Fatalf("after the robots: %s in %s", tk.Status, tk.StageID)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_robot' AND body = 'Готово: Сайт'`).Scan(&notes))
	if notes != 1 {
		t.Fatalf("finish notifications = %d", notes)
	}

	// a waiting robot does nothing if the task has left its stage
	key2, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Ушла", ResponsibleID: resp, StageID: work})
	must(err)
	id2, _ := s.TaskIDByKey(ctx, key2)
	run(time.Now())
	must(s.MoveToStage(ctx, resp, id2, newSt))
	run(time.Now().Add(2 * time.Hour))
	if tk, _ := s.TaskByID(ctx, id2); tk.StageID != newSt {
		t.Fatalf("the waiting robot moved a task that had left: %s", tk.StageID)
	}

	// a robot turned off does nothing; a ring of stage robots stops
	for _, r := range robots {
		must(s.SetRobotActive(ctx, p, r.ID, false))
	}
	_, err = s.CreateProject(ctx, owner, "RG", "ring", "")
	must(err)
	rp, _ := s.ProjectByKey(ctx, owner, "RG")
	rs, err := s.Stages(ctx, rp.ID)
	must(err)
	must(s.AddRobot(ctx, owner, rp, rs[0].ID, "stage", RobotParams{Stage: rs[1].ID}))
	must(s.AddRobot(ctx, owner, rp, rs[1].ID, "stage", RobotParams{Stage: rs[0].ID}))
	rkey, err := s.CreateTask(ctx, owner, rp, NewTask{Title: "Круг"})
	must(err)
	rid, _ := s.TaskIDByKey(ctx, rkey)
	for i := 0; i < 20 && run(time.Now()) > 0; i++ {
	}
	var moves int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'stage'`, rid).Scan(&moves))
	if moves == 0 || moves > maxRobotDepth+1 {
		t.Fatalf("ring moves = %d", moves)
	}
	before := len(comments)
	key3, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Тихо", ResponsibleID: resp, StageID: work})
	must(err)
	_ = key3
	if n := run(time.Now()); n != 0 || len(comments) != before {
		t.Fatalf("robots turned off ran: %d", n)
	}

	// a robot that fails leaves a line in the task's history
	must(s.SetRobotActive(ctx, p, robots[0].ID, true))
	_, err = pool.Exec(ctx, `UPDATE task_robots SET params = jsonb_set(params, '{to}', '"00000000-0000-0000-0000-000000000000"') WHERE id = $1`, robots[0].ID)
	must(err)
	key4, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Сбой", ResponsibleID: resp})
	must(err)
	id4, _ := s.TaskIDByKey(ctx, key4)
	run(time.Now())
	var fail string
	must(pool.QueryRow(ctx, `SELECT new_value FROM task_history WHERE task_id = $1 AND field = 'robot'`, id4).Scan(&fail))
	if len(fail) < 12 || fail[:12] != "fail:notify:" {
		t.Fatalf("failure = %q", fail)
	}

	// a robot is removed
	must(s.DeleteRobot(ctx, p, robots[1].ID))
	robots, _ = s.Robots(ctx, p.ID)
	if len(robots) != 8 {
		t.Fatalf("after delete: %d robots", len(robots))
	}
}
