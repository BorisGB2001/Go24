package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestStages: a new pipeline gets the stages of a new one; stages are added, renamed (a built-in one keeps its
// code), moved only among their kind, and deleted with their cards moved to another stage; the success stage, the
// last of a kind and a pipeline with deals stay.
func TestStages(t *testing.T) {
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
	pipeline := func(id string) Pipeline {
		t.Helper()
		pipes, err := s.Pipelines(ctx)
		must(err)
		for _, p := range pipes {
			if p.ID == id {
				return p
			}
		}
		t.Fatalf("no pipeline %s", id)
		return Pipeline{}
	}
	names := func(sts []Stage) (out []string) {
		for _, st := range sts {
			out = append(out, st.Code+st.Name)
		}
		return out
	}

	if _, err := s.AddPipeline(ctx, " "); err == nil {
		t.Fatal("a pipeline without a name")
	}
	opt, err := s.AddPipeline(ctx, "Опт")
	must(err)
	p := pipeline(opt)
	if p.Name != "Опт" || len(p.Stages) != 7 || p.Stages[5].Semantic != "success" || p.Stages[6].Semantic != "failure" {
		t.Fatalf("new pipeline: %+v", p)
	}
	must(s.RenamePipeline(ctx, opt, "Оптовые продажи"))
	if pipeline(opt).Name != "Оптовые продажи" {
		t.Fatal("rename")
	}

	// a working stage goes before the success one, a failure stage to the end
	must(s.AddStage(ctx, "deal", opt, "Переговоры", "#ff0000", ""))
	must(s.AddStage(ctx, "deal", opt, "Отказ клиента", "", "failure"))
	if err := s.AddStage(ctx, "deal", opt, "x", "red", ""); err == nil {
		t.Fatal("a bad color")
	}
	p = pipeline(opt)
	got := names(p.Stages)
	if got[5] != "Переговоры" || got[6] != "won" || got[8] != "Отказ клиента" || p.Stages[5].Color != "FF0000" {
		t.Fatalf("stages after adding: %v", got)
	}
	// moving stays among the stage's kind
	nego := p.Stages[5].ID
	must(s.ShiftStage(ctx, nego, 1)) // the next one is the success stage: nothing moves
	must(s.ShiftStage(ctx, nego, -1))
	if got = names(pipeline(opt).Stages); got[4] != "Переговоры" || got[5] != "final_invoice" {
		t.Fatalf("after shifting: %v", got)
	}
	// renaming a built-in stage keeps its code; the name wins
	won := p.Stages[6]
	must(s.UpdateStage(ctx, won.ID, "Оплачено", "00FF00"))
	if st := pipeline(opt).Stages[6]; st.Code != "won" || st.Name != "Оплачено" || stageText(st) != "Оплачено" {
		t.Fatalf("renamed: %+v", st)
	}

	// deleting: the cards go to another stage of the same pipeline (a deal is closed by a final one)
	id, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: opt, Stage: Stage{ID: nego}, Responsible: Ref{ID: anna}})
	must(err)
	if err := s.DeleteStage(ctx, anna, nego, ""); err == nil {
		t.Fatal("deleted a stage with cards and nowhere to move them")
	}
	cur := pipeline(opt)
	others, _ := s.Pipelines(ctx)
	if err := s.DeleteStage(ctx, anna, nego, others[0].Stages[0].ID); err == nil {
		t.Fatal("moved the cards to another pipeline")
	}
	failed := cur.Stages[len(cur.Stages)-1].ID
	must(s.DeleteStage(ctx, anna, nego, failed))
	d, err := s.Deal(ctx, id)
	must(err)
	if d.Stage.ID != failed || d.ClosedAt == nil {
		t.Fatalf("moved deal: %+v", d)
	}
	tl, err := s.Timeline(ctx, "deal", id)
	must(err)
	if len(tl) == 0 || tl[0].Kind != "stage" {
		t.Fatalf("timeline: %+v", tl)
	}
	if err := s.DeleteStage(ctx, anna, won.ID, ""); err == nil {
		t.Fatal("deleted the success stage")
	}
	must(s.DeleteStage(ctx, anna, cur.Stages[7].ID, "")) // «lose»: «Отказ клиента» is still a failure stage
	if err := s.DeleteStage(ctx, anna, failed, ""); err == nil {
		t.Fatal("deleted the last failure stage")
	}

	// leads: their own stages; the success one is reached by converting only
	must(s.AddStage(ctx, "lead", "", "Перезвонить", "", ""))
	lst, err := s.LeadStages(ctx)
	must(err)
	if got = names(lst); got[3] != "Перезвонить" || got[4] != "lead_converted" {
		t.Fatalf("lead stages: %v", got)
	}
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Responsible: Ref{ID: anna}})
	must(err)
	_, err = s.MoveLead(ctx, anna, lead, lst[3].ID)
	must(err)
	if err := s.DeleteStage(ctx, anna, lst[3].ID, lst[4].ID); err == nil {
		t.Fatal("leads moved to the success stage without converting")
	}
	must(s.DeleteStage(ctx, anna, lst[3].ID, lst[0].ID))
	if l, _ := s.Lead(ctx, lead); l.Stage.ID != lst[0].ID {
		t.Fatalf("moved lead: %+v", l.Stage)
	}

	// pipelines: one with deals stays, so does the last one
	if err := s.DeletePipeline(ctx, opt); err == nil {
		t.Fatal("deleted a pipeline with deals")
	}
	must(s.DeleteDeal(ctx, anna, id))
	must(s.DeletePipeline(ctx, opt))
	if err := s.DeletePipeline(ctx, others[0].ID); err == nil {
		t.Fatal("deleted the last pipeline")
	}
}
