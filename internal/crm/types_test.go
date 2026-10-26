package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTypes: a smart process gets its pipeline «Общая» with the stages of the smart process; its pipelines
// and cards stay apart from the deals (the deal pipelines, lead conversion), its cards move along its stages only; the last pipeline of a kind stays; a smart process with cards is not deleted, an empty one goes with its
// pipelines.
func TestTypes(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)

	if _, err := s.AddType(ctx, " ", ""); err == nil {
		t.Fatal("a smart process without a name")
	}
	typ, err := s.AddType(ctx, "Договоры", "#ffa900")
	must(err)
	pipes, err := s.PipelinesOf(ctx, typ)
	must(err)
	if len(pipes) != 1 || pipes[0].Code != "default" || pipes[0].TypeID != typ || len(pipes[0].Stages) != 5 ||
		pipes[0].Stages[0].Code != "sp_new" || pipes[0].Stages[3].Semantic != "success" || pipes[0].Stages[4].Semantic != "failure" {
		t.Fatalf("pipelines of the smart process: %+v", pipes)
	}
	deals, err := s.Pipelines(ctx)
	must(err)
	if len(deals) != 1 || deals[0].TypeID != "" {
		t.Fatalf("the deal pipelines take in the smart process's: %+v", deals)
	}
	if got, _ := s.PipelineType(ctx, pipes[0].ID); got != typ {
		t.Fatalf("pipeline type %q", got)
	}

	// a second pipeline of the smart process has its stages; the last one of the kind stays
	second, err := s.AddPipelineOf(ctx, typ, "Срочные")
	must(err)
	if p, _ := s.PipelinesOf(ctx, typ); len(p) != 2 || p[1].ID != second || p[1].Stages[0].Code != "sp_new" {
		t.Fatalf("second pipeline: %+v", p)
	}
	must(s.DeletePipeline(ctx, second))
	if err := s.DeletePipeline(ctx, pipes[0].ID); err == nil {
		t.Fatal("deleted the last pipeline of the smart process")
	}

	// its card: a deal of its pipeline, moving along its stages only
	st := pipes[0].Stages
	id, err := s.SaveDeal(ctx, anna, Deal{Title: "Договор 7", PipelineID: pipes[0].ID, Amount: 5000, Responsible: Ref{ID: anna}})
	must(err)
	d, err := s.Deal(ctx, id)
	must(err)
	if d.TypeID != typ || d.Stage.Code != "sp_new" {
		t.Fatalf("card: %+v", d)
	}
	if _, err := s.MoveDeal(ctx, anna, id, deals[0].Stages[1].ID); err == nil {
		t.Fatal("moved a card to a stage of a deal pipeline")
	}
	d, err = s.MoveDeal(ctx, anna, id, st[3].ID)
	must(err)
	if d.Stage.Code != "sp_success" {
		t.Fatalf("moved: %+v", d)
	}
	if ty, _ := s.Type(ctx, typ); ty.Items != 1 || ty.Name != "Договоры" || ty.Color != "FFA900" {
		t.Fatalf("type: %+v", ty)
	}

	// a lead converts into a deal, never into a card of a smart process
	must(pool.QueryRow(ctx, `UPDATE crm_pipelines SET sort = -1 WHERE id = $1 RETURNING id`, pipes[0].ID).Scan(new(string)))
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Responsible: Ref{ID: anna}})
	must(err)
	dealID, err := s.ConvertLead(ctx, anna, lead)
	must(err)
	if conv, _ := s.Deal(ctx, dealID); conv.TypeID != "" {
		t.Fatalf("converted into a smart process: %+v", conv)
	}

	// deleting: not with cards; empty — with its pipelines and stages
	if err := s.DeleteType(ctx, typ); err == nil {
		t.Fatal("deleted a smart process with cards")
	}
	must(s.UpdateType(ctx, typ, "Договоры поставки", "00C4FB"))
	must(s.DeleteDeal(ctx, anna, id))
	must(s.DeleteType(ctx, typ))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_pipelines WHERE id = $1`, pipes[0].ID).Scan(&left))
	if left != 0 {
		t.Fatal("the pipelines of a deleted smart process stay")
	}
	if list, _ := s.Types(ctx); len(list) != 0 {
		t.Fatalf("types left: %+v", list)
	}
}
