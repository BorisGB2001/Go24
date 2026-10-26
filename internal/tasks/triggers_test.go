package tasks

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTriggers checks the triggers of kanban stages against a real PostgreSQL (see TestStoreScenario).
func TestTriggers(t *testing.T) {
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
	owner, resp, other := newUser("owner"), newUser("resp"), newUser("other")
	s := NewStore(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	run := func(at time.Time) {
		t.Helper()
		for i := 0; i < 10; i++ {
			n, err := s.RunRobotsDue(ctx, at)
			must(err)
			if n == 0 {
				return
			}
		}
	}

	_, err = s.CreateProject(ctx, owner, "TG", "triggers", "")
	must(err)
	p, err := s.ProjectByKey(ctx, owner, "TG")
	must(err)
	must(s.SetMember(ctx, p, resp, "member"))
	pm, _ := s.ProjectByKey(ctx, resp, "TG")
	must(s.AddProjectStage(ctx, p, "Обсуждение", ""))
	must(s.AddProjectStage(ctx, p, "Просрочено", ""))
	st, err := s.Stages(ctx, p.ID)
	must(err)
	newSt, work, talk, late := st[0].ID, st[1].ID, st[2].ID, st[3].ID

	if err := s.AddTrigger(ctx, resp, pm, work, "comment", ""); err == nil {
		t.Fatal("a member sets a trigger")
	}
	if err := s.AddTrigger(ctx, owner, p, work, "status", StatusCompleted); err == nil {
		t.Fatal("a trigger for a finished status")
	}
	must(s.AddTrigger(ctx, owner, p, work, "status", StatusInProgress))
	must(s.AddTrigger(ctx, owner, p, talk, "comment", ""))
	must(s.AddTrigger(ctx, owner, p, newSt, "responsible", ""))
	must(s.AddTrigger(ctx, owner, p, late, "overdue", ""))
	must(s.AddTrigger(ctx, owner, p, work, "checklist", ""))
	must(s.AddRobot(ctx, owner, p, talk, "notify", RobotParams{Text: "Обсуждают {title}", To: "creator"}))

	stageOf := func(id string) string {
		t.Helper()
		tk, err := s.TaskByID(ctx, id)
		must(err)
		return tk.StageID
	}
	key, err := s.CreateTask(ctx, owner, p, NewTask{Title: "Сайт", ResponsibleID: resp})
	must(err)
	id, _ := s.TaskIDByKey(ctx, key)
	run(time.Now())

	// the responsible starts the task: it goes to «Выполняются»
	must(s.ApplyAction(ctx, resp, id, ActionStart))
	run(time.Now())
	if stageOf(id) != work {
		t.Fatalf("after start: %s", stageOf(id))
	}
	// a comment: «Обсуждение», whose robot runs
	_, err = s.PostComment(ctx, owner, id, "Как дела?")
	must(err)
	run(time.Now())
	if stageOf(id) != talk {
		t.Fatalf("after a comment: %s", stageOf(id))
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_robot' AND body = 'Обсуждают Сайт'`).Scan(&notes))
	if notes != 1 {
		t.Fatalf("robot after the trigger: %d", notes)
	}
	// the whole checklist done: back to work
	must(s.AddChecklistItem(ctx, owner, id, "Макет"))
	c, err := s.GetCard(ctx, owner, id)
	must(err)
	must(s.ChangeChecklistItem(ctx, resp, id, c.Checklist[0].ID, false))
	run(time.Now())
	if stageOf(id) != work {
		t.Fatalf("after the checklist: %s", stageOf(id))
	}
	// another responsible: the first stage
	must(s.SetResponsible(ctx, owner, id, other))
	run(time.Now())
	if stageOf(id) != newSt {
		t.Fatalf("after a new responsible: %s", stageOf(id))
	}
	// the deadline passes: «Просрочено», once; a new deadline lets it fire again
	past := time.Now().Add(-time.Hour)
	must(s.SetDeadline(ctx, owner, id, &past))
	run(time.Now())
	if stageOf(id) != late {
		t.Fatalf("overdue: %s", stageOf(id))
	}
	must(s.MoveToStage(ctx, owner, id, work))
	run(time.Now())
	if stageOf(id) != work {
		t.Fatal("overdue fired twice for one deadline")
	}
	past2 := time.Now().Add(-30 * time.Minute)
	must(s.SetDeadline(ctx, owner, id, &past2))
	run(time.Now())
	if stageOf(id) != late {
		t.Fatalf("overdue after a new deadline: %s", stageOf(id))
	}

	trs, err := s.Triggers(ctx, p.ID)
	must(err)
	if len(trs) != 5 {
		t.Fatalf("triggers = %d", len(trs))
	}
	must(s.DeleteTrigger(ctx, p, trs[1].ID))
	_, err = s.PostComment(ctx, owner, id, "Ещё")
	must(err)
	run(time.Now())
	if stageOf(id) != late {
		t.Fatal("a removed trigger moved the task")
	}
}
