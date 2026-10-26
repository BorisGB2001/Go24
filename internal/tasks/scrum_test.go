package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestScrum: backlog → sprint → start → kanban → complete, against a real PostgreSQL.
func TestScrum(t *testing.T) {
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
	owner, dev, viewer := newUser("owner"), newUser("dev"), newUser("viewer")
	s := NewStore(pool)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	wantKey := func(err error, key string) {
		t.Helper()
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("err = %v, want %s", err, key)
		}
	}
	_, err = s.CreateProject(ctx, owner, "SC", "Scrum", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, "SC")
	if err := s.CreateSprint(ctx, p, "S1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("sprint in a group: %v", err)
	}
	must(s.SetProjectKind(ctx, p, "scrum"))
	must(s.SetMember(ctx, p, dev, "member"))
	must(s.SetMember(ctx, p, viewer, "viewer"))
	p, _ = s.ProjectByKey(ctx, owner, "SC")
	pd, _ := s.ProjectByKey(ctx, dev, "SC")
	pv, _ := s.ProjectByKey(ctx, viewer, "SC")
	if !p.Scrum() {
		t.Fatal("kind not saved")
	}

	two, three := 2.0, 3.0
	k1, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "A", StoryPoints: &two})
	must(err)
	k2, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "B", StoryPoints: &three})
	must(err)
	k3, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "C"})
	must(err)
	id := func(k string) string { x, _ := s.TaskIDByKey(ctx, k); return x }

	plan, err := s.ScrumPlanFor(ctx, dev, pd)
	must(err)
	if len(plan.Backlog) != 3 || plan.Points != 5 {
		t.Fatalf("backlog = %d tasks, %v SP", len(plan.Backlog), plan.Points)
	}
	if err := s.CreateSprint(ctx, pv, "S1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer creates a sprint: %v", err)
	}
	wantKey(s.CreateSprint(ctx, pd, " "), "err.sprint_name")
	must(s.CreateSprint(ctx, pd, "S1"))
	must(s.CreateSprint(ctx, pd, "S2"))
	plan, _ = s.ScrumPlanFor(ctx, dev, pd)
	s1, s2 := plan.Sprints[0], plan.Sprints[1]
	if s1.Name != "S1" || s2.DateStart == nil || !s2.DateStart.After(*s1.DateEnd) {
		t.Fatalf("sprints = %+v", plan.Sprints)
	}
	wantKey(s.StartSprint(ctx, p, s1.ID), "err.sprint_empty")
	must(s.MoveToSprint(ctx, dev, id(k1), s1.ID))
	must(s.MoveToSprint(ctx, dev, id(k2), s1.ID))
	if err := s.StartSprint(ctx, pd, s1.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member starts a sprint: %v", err)
	}
	must(s.StartSprint(ctx, p, s1.ID))
	must(s.MoveToSprint(ctx, dev, id(k3), s2.ID))
	wantKey(s.StartSprint(ctx, p, s2.ID), "err.sprint_active_exists")

	// the kanban shows the active sprint only
	cols, err := s.Board(ctx, dev, pd)
	must(err)
	n := 0
	for _, c := range cols {
		n += c.Total
	}
	if n != 2 {
		t.Fatalf("kanban has %d tasks, want 2 (active sprint)", n)
	}
	// «+» on the sprint kanban adds to the active sprint
	quick, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "D", StageID: cols[0].ID})
	must(err)
	if tk, _ := s.TaskByID(ctx, id(quick)); tk.SprintID != s1.ID {
		t.Fatalf("quick task sprint = %q", tk.SprintID)
	}

	// finish A by dragging to the finish stage; story points and epic
	must(s.MoveToStage(ctx, dev, id(k1), cols[len(cols)-1].ID))
	wantKey(s.SetStoryPoints(ctx, dev, id(k2), ptrF(-1)), "err.story_points")
	must(s.SetStoryPoints(ctx, dev, id(k2), ptrF(5)))
	must(s.CreateEpic(ctx, dev, pd, "Онлайн-оплата", "#ff5752", ""))
	ep, _ := s.Epics(ctx, p.ID)
	must(s.SetEpic(ctx, dev, id(k2), ep[0].ID))
	if ep, _ = s.Epics(ctx, p.ID); ep[0].Tasks != 1 || ep[0].Points != 5 || ep[0].Color != "FF5752" {
		t.Fatalf("epic = %+v", ep[0])
	}
	bd, err := s.Burndown(ctx, s1.ID)
	must(err)
	if len(bd) != 14 || bd[0].Ideal != 7 || bd[len(bd)-1].Ideal != 0 || bd[0].Remaining != 5 {
		t.Fatalf("burndown = %+v…", bd[0]) // 2+5 SP; A (2 SP) closed today
	}

	// complete: unfinished B and D move to S2; statistics frozen
	wantKey(s.CompleteSprint(ctx, p, s2.ID, ""), "err.sprint_not_active")
	must(s.CompleteSprint(ctx, p, s1.ID, s2.ID))
	done, err := s.CompletedSprints(ctx, p)
	must(err)
	if len(done) != 1 || done[0].Done != 2 || done[0].Points != 5 || done[0].DoneCount != 1 {
		t.Fatalf("completed = %+v", done) // points_start was 2+3 at start
	}
	plan, _ = s.ScrumPlanFor(ctx, dev, pd)
	if len(plan.Sprints) != 1 || len(plan.Sprints[0].Tasks) != 3 || len(plan.Backlog) != 0 {
		t.Fatalf("after completion: %d sprints, %d tasks in S2, backlog %d", len(plan.Sprints), len(plan.Sprints[0].Tasks), len(plan.Backlog))
	}
	if tk, _ := s.TaskByID(ctx, id(k2)); tk.StageID != "" {
		t.Fatal("moved task kept its kanban stage")
	}
	// back to the backlog; a planned sprint can be deleted, its tasks return to the backlog
	must(s.MoveToSprint(ctx, dev, id(k3), ""))
	must(s.DeleteSprint(ctx, pd, s2.ID))
	plan, _ = s.ScrumPlanFor(ctx, dev, pd)
	if len(plan.Backlog) != 3 {
		t.Fatalf("backlog after deleting S2 = %d", len(plan.Backlog))
	}
	wantKey(s.MoveToSprint(ctx, dev, id(k3), s1.ID), "err.sprint_completed")
}

func ptrF(f float64) *float64 { return &f }
