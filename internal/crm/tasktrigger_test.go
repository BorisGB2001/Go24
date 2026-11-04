package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestTaskTriggers: «Изменение статуса задачи» moves the deal and the lead linked to the task, the trigger with a
// status only when the task came to it; changes before the first run and other tasks' changes move nothing.
func TestTaskTriggers(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "SALE", "Продажи", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "SALE")
	must(err)
	s := NewStore(pool)
	s.Tasks = ts
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	lst, err := s.LeadStages(ctx)
	must(err)
	if err := s.AddTrigger(ctx, boss, st[1].ID, "task", TriggerParams{Status: "nonsense"}); err == nil {
		t.Fatalf("a trigger with an unknown status")
	}
	must(s.AddTrigger(ctx, boss, st[1].ID, "task", TriggerParams{}))
	must(s.AddTrigger(ctx, boss, st[3].ID, "task", TriggerParams{Status: "completed"}))
	must(s.AddTrigger(ctx, boss, lst[1].ID, "task", TriggerParams{Status: "in_progress"}))
	trs, _ := s.Triggers(ctx, []string{st[3].ID})
	if len(trs) != 1 || trs[0].Params.Status != "completed" {
		t.Fatalf("the trigger = %+v", trs)
	}
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Д", PipelineID: pipes[0].ID, Responsible: Ref{ID: boss}})
	must(err)
	lead, err := s.SaveLead(ctx, boss, Lead{Title: "Л", Responsible: Ref{ID: boss}})
	must(err)
	stageOf := func(entity, id string) string {
		t.Helper()
		if entity == "lead" {
			l, err := s.Lead(ctx, id)
			must(err)
			return l.Stage.ID
		}
		d, err := s.Deal(ctx, id)
		must(err)
		return d.Stage.ID
	}
	newTask := func(links ...string) string {
		t.Helper()
		key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Позвонить", ResponsibleID: boss})
		must(err)
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM tasks WHERE project_id = $1 AND number = $2`, p.ID, key[len("SALE-"):]).Scan(&id))
		for _, l := range links {
			must(ts.AddCRMLink(ctx, boss, id, l))
		}
		return id
	}
	run := func() int {
		t.Helper()
		// changes younger than 5 seconds wait for the next run
		_, err := pool.Exec(ctx, `UPDATE task_history SET created_at = created_at - interval '1 minute'`)
		must(err)
		n, err := s.RunTriggersDue(ctx, time.Now())
		must(err)
		return n
	}
	early := newTask("/crm/deals/" + deal)
	must(ts.ApplyAction(ctx, boss, early, tasks.ActionStart))
	if run(); stageOf("deal", deal) != st[0].ID {
		t.Fatalf("a change before the first run moved the deal")
	}
	task := newTask("/crm/deals/"+deal, "/crm/leads/"+lead)
	newTask() // a task of no card
	must(ts.ApplyAction(ctx, boss, task, tasks.ActionStart))
	if n := run(); n != 2 || stageOf("deal", deal) != st[1].ID || stageOf("lead", lead) != lst[1].ID {
		t.Fatalf("started: %d, deal %s, lead %s", n, stageOf("deal", deal), stageOf("lead", lead))
	}
	if run() != 0 {
		t.Fatalf("the same change twice")
	}
	must(ts.ApplyAction(ctx, boss, task, tasks.ActionComplete))
	run()
	if stageOf("deal", deal) != st[3].ID || stageOf("lead", lead) != lst[1].ID {
		t.Fatalf("completed: deal %s, lead %s", stageOf("deal", deal), stageOf("lead", lead))
	}
	tl, _ := s.Timeline(ctx, "deal", deal)
	found := false
	for _, e := range tl {
		found = found || (e.Kind == "robot" && e.Body == "trigger:task")
	}
	if !found {
		t.Fatalf("no trigger line in the history")
	}
}
