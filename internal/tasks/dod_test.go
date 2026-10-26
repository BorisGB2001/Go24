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

// TestScrumTeamDod: the scrum team runs sprints; a compulsory Definition of Done blocks completing a sprint task
// until its items are ticked, against a real PostgreSQL.
func TestScrumTeamDod(t *testing.T) {
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
	owner, master, dev, outsider := newUser("owner"), newUser("master"), newUser("dev"), newUser("outsider")
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
	_, err = s.CreateProject(ctx, owner, "DD", "Scrum", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, "DD")
	must(s.SetProjectKind(ctx, p, "scrum"))
	must(s.SetMember(ctx, p, master, "member"))
	must(s.SetMember(ctx, p, dev, "member"))
	p, _ = s.ProjectByKey(ctx, owner, "DD")

	// the team: the product owner is the project's owner by default; a non-member cannot be named
	team, err := s.ScrumTeamOf(ctx, p)
	must(err)
	if team.Owner == nil || team.Owner.ID != owner || team.Master != nil || len(team.Team) != 3 {
		t.Fatalf("team = %+v", team)
	}
	wantKey(s.SetScrumTeam(ctx, p, "", outsider), "err.scrum_not_member")
	pm, _ := s.ProjectByKey(ctx, master, "DD")
	if pm.CanLead() {
		t.Fatal("a member leads before being the scrum master")
	}
	if err := s.SetScrumTeam(ctx, pm, "", master); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member sets the team: %v", err)
	}
	must(s.SetScrumTeam(ctx, p, "", master))
	pm, _ = s.ProjectByKey(ctx, master, "DD")
	pd, _ := s.ProjectByKey(ctx, dev, "DD")
	if !pm.CanLead() || pd.CanLead() {
		t.Fatalf("lead: master %v, dev %v", pm.CanLead(), pd.CanLead())
	}

	// types: defaults once; the scrum master sets the DoD, a member cannot
	must(s.EnsureScrumTypes(ctx, p, "Задача", "Ошибка"))
	must(s.EnsureScrumTypes(ctx, p, "Задача", "Ошибка"))
	types, err := s.ScrumTypes(ctx, p.ID)
	must(err)
	if len(types) != 2 || types[0].Name != "Задача" {
		t.Fatalf("types = %+v", types)
	}
	if err := s.SaveScrumType(ctx, pd, types[0].ID, "Задача", true, []string{"Тесты"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member sets DoD: %v", err)
	}
	must(s.SaveScrumType(ctx, pm, types[0].ID, "Задача", true, []string{"Тесты", " ", "Ревью"}))
	types, _ = s.ScrumTypes(ctx, p.ID)
	if len(types[0].Items) != 2 || !types[0].Required || types[0].Items[1].Title != "Ревью" {
		t.Fatalf("type = %+v", types[0])
	}

	// a task of the backlog completes freely; in a sprint — only with the DoD ticked
	k1, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "A"})
	must(err)
	k2, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "B"})
	must(err)
	id := func(k string) string { x, _ := s.TaskIDByKey(ctx, k); return x }
	must(s.ApplyAction(ctx, dev, id(k2), ActionComplete))
	must(s.CreateSprint(ctx, pm, "S1"))
	plan, _ := s.ScrumPlanFor(ctx, dev, pd)
	sp := plan.Sprints[0].ID
	must(s.MoveToSprint(ctx, dev, id(k1), sp))
	must(s.StartSprint(ctx, pm, sp)) // the scrum master starts the sprint

	d, err := s.TaskDodOf(ctx, dev, id(k1))
	must(err)
	if d == nil || !d.InSprint || d.Type.ID != types[0].ID || !d.Blocks() || !d.CanEdit {
		t.Fatalf("dod = %+v", d)
	}
	wantKey(s.ApplyAction(ctx, dev, id(k1), ActionComplete), "err.dod_required")
	must(s.SetTaskDod(ctx, dev, id(k1), "", []string{d.Type.Items[0].ID}))
	wantKey(s.ApplyAction(ctx, dev, id(k1), ActionComplete), "err.dod_required")
	// renaming the checklist keeps the ticked item that stays
	must(s.SaveScrumType(ctx, pm, types[0].ID, "Задача", true, []string{"Ревью", "Тесты"}))
	must(s.SetTaskDod(ctx, dev, id(k1), "", []string{d.Type.Items[0].ID, d.Type.Items[1].ID})) // the form sends all the ticked ones
	d, _ = s.TaskDodOf(ctx, dev, id(k1))
	if !d.Done || d.Blocks() {
		t.Fatalf("dod after ticking = %+v", d)
	}
	must(s.ApplyAction(ctx, dev, id(k1), ActionComplete))

	// the other type has no DoD; a type change is in the history; the last type stays
	k3, err := s.CreateTask(ctx, dev, pd, NewTask{Title: "C"})
	must(err)
	must(s.MoveToSprint(ctx, dev, id(k3), sp))
	wantKey(s.ApplyAction(ctx, dev, id(k3), ActionComplete), "err.dod_required")
	must(s.SetTaskDod(ctx, dev, id(k3), types[1].ID, nil))
	must(s.ApplyAction(ctx, dev, id(k3), ActionComplete))
	var hist int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_history WHERE task_id = $1 AND field = 'scrum_type' AND new_value = 'Ошибка'`, id(k3)).Scan(&hist))
	if hist != 1 {
		t.Fatalf("type history = %d", hist)
	}
	must(s.DeleteScrumType(ctx, pm, types[1].ID))
	wantKey(s.DeleteScrumType(ctx, pm, types[0].ID), "err.scrum_type_last")
	d, _ = s.TaskDodOf(ctx, dev, id(k3)) // back to the default type
	if d.Type.ID != types[0].ID {
		t.Fatalf("type after delete = %+v", d.Type)
	}

	// the scrum master completes the sprint
	must(s.CompleteSprint(ctx, pm, sp, ""))
}
