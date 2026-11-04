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

// TestCopyProject: «Копировать» a group — its settings, stages with robots, members and tasks go into the copy,
// the dates move with the start, the tasks start anew and no robot runs on them.
func TestCopyProject(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}
	owner, mod, member, gone := newUser("owner"), newUser("mod"), newUser("member"), newUser("gone")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, owner, "SRC", "Образец", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, "SRC")
	start := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	must(s.SaveProjectInfo(ctx, p, "Образец", "Описание", &start, nil))
	must(s.SetMember(ctx, p, mod, "manager"))
	must(s.SetMember(ctx, p, member, "member"))
	must(s.SetMember(ctx, p, gone, "member"))
	must(s.SetPrivacy(ctx, p, "open"))
	p, _ = s.ProjectByKey(ctx, owner, "SRC")
	stages, err := s.Stages(ctx, p.ID)
	must(err)
	must(s.AddRobot(ctx, owner, p, stages[0].ID, "stage", RobotParams{Stage: stages[1].ID}))

	deadline := time.Date(2026, 11, 5, 18, 0, 0, 0, time.UTC)
	parent, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Основная", ResponsibleID: gone, Deadline: &deadline,
		Checklist: []string{"Шаг 1", "Шаг 2"}})
	must(err)
	child, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Подзадача", ResponsibleID: member, ParentKey: parent})
	must(err)
	done, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Сделана", ResponsibleID: member})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE tasks SET status = 'completed' WHERE id = (SELECT t.id FROM tasks t JOIN projects p ON p.id = t.project_id WHERE p.key || '-' || t.number = $1)`, done)
	must(err)
	childID, _ := s.TaskIDByKey(ctx, child)
	must(s.AddLink(ctx, owner, childID, parent, "finish_start"))
	_, err = pool.Exec(ctx, `UPDATE task_checklist_items SET done = true`)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, gone)
	must(err)

	// only the owner and moderators copy
	pm, _ := s.ProjectByKey(ctx, member, "SRC")
	if _, err := s.CopyProject(ctx, member, pm, CopyOptions{Key: "CP", Name: "x"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member copies: %v", err)
	}
	if k, _ := s.CopyKey(ctx, "SRC"); k != "SRC2" {
		t.Fatalf("CopyKey = %q", k)
	}
	if _, err := s.CopyProject(ctx, mod, pm, CopyOptions{Key: "SRC", Name: "x"}); err == nil {
		t.Fatal("a taken key is accepted")
	}

	newStart := time.Date(2026, 12, 7, 0, 0, 0, 0, time.UTC) // 35 days later
	pmod, _ := s.ProjectByKey(ctx, mod, "SRC")
	key, err := s.CopyProject(ctx, mod, pmod, CopyOptions{Key: "cp", Name: "Копия", Start: &newStart, Members: true, Tasks: true})
	must(err)
	if key != "CP" {
		t.Fatalf("key = %q", key)
	}
	c, err := s.ProjectByKey(ctx, mod, "CP")
	must(err)
	if c.MyRole != "owner" || c.Privacy != "open" || c.Description != "Описание" || c.DateStart == nil || !c.DateStart.Equal(newStart) {
		t.Fatalf("copy = %+v", c)
	}
	var ownerRole string
	must(pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, c.ID, owner).Scan(&ownerRole))
	if ownerRole != "manager" || count(`SELECT count(*) FROM project_members WHERE project_id = $1`, c.ID) != 3 {
		t.Fatalf("members: owner %q, %d", ownerRole, count(`SELECT count(*) FROM project_members WHERE project_id = $1`, c.ID))
	}
	// the stages and the robot, which moves the task to the copy's own stage
	cst, err := s.Stages(ctx, c.ID)
	must(err)
	robots, err := s.Robots(ctx, c.ID)
	must(err)
	if len(cst) != len(stages) || len(robots) != 1 || robots[0].StageID != cst[0].ID || robots[0].Params.Stage != cst[1].ID {
		t.Fatalf("stages %d, robots %+v", len(cst), robots)
	}
	// the open tasks: numbers from 1, the subtask under its parent, the responsible who left — the one who copies
	if n := count(`SELECT count(*) FROM tasks WHERE project_id = $1`, c.ID); n != 2 {
		t.Fatalf("tasks = %d", n)
	}
	var resp, status string
	var dl time.Time
	must(pool.QueryRow(ctx, `SELECT responsible_id, status, deadline FROM tasks WHERE project_id = $1 AND number = 1`, c.ID).Scan(&resp, &status, &dl))
	if resp != mod || status != "pending" || !dl.Equal(deadline.AddDate(0, 0, 35)) {
		t.Fatalf("CP-1: %s %s %v", resp, status, dl)
	}
	if n := count(`SELECT count(*) FROM tasks c JOIN tasks pt ON pt.id = c.parent_id WHERE c.project_id = $1 AND pt.project_id = $1`, c.ID); n != 1 {
		t.Fatalf("subtasks = %d", n)
	}
	if n := count(`SELECT count(*) FROM task_checklist_items i JOIN tasks t ON t.id = i.task_id WHERE t.project_id = $1 AND NOT i.done`, c.ID); n != 2 {
		t.Fatalf("checklist = %d", n)
	}
	if n := count(`SELECT count(*) FROM task_links l JOIN tasks t ON t.id = l.from_task WHERE t.project_id = $1`, c.ID); n != 1 {
		t.Fatalf("links = %d", n)
	}
	if n := count(`SELECT count(*) FROM task_robot_jobs j JOIN tasks t ON t.id = j.task_id WHERE t.project_id = $1`, c.ID); n != 0 {
		t.Fatalf("robot jobs = %d", n)
	}
	if n := count(`SELECT task_seq FROM projects WHERE id = $1`, c.ID); n != 2 {
		t.Fatalf("task_seq = %d", n)
	}

	// settings only, completed tasks too in another copy
	key, err = s.CopyProject(ctx, mod, pmod, CopyOptions{Key: "CQ", Name: "Без задач"})
	must(err)
	cq, _ := s.ProjectByKey(ctx, mod, key)
	if count(`SELECT count(*) FROM tasks WHERE project_id = $1`, cq.ID) != 0 || count(`SELECT count(*) FROM project_members WHERE project_id = $1`, cq.ID) != 1 {
		t.Fatal("settings-only copy has tasks or members")
	}
	key, err = s.CopyProject(ctx, mod, pmod, CopyOptions{Key: "CR", Name: "Все", Tasks: true, Closed: true})
	must(err)
	cr, _ := s.ProjectByKey(ctx, mod, key)
	if n := count(`SELECT count(*) FROM tasks WHERE project_id = $1 AND status = 'pending'`, cr.ID); n != 3 {
		t.Fatalf("all tasks = %d", n)
	}
}
