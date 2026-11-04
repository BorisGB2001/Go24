package bp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCRMSmartSteps: «Создать» makes a smart process's element in the chosen stage, bound to the process's deal;
// «Изменить элемент CRM» sets the fields of the element by a link and of the process's own card; the file of the
// template names the smart process.
func TestCRMSmartSteps(t *testing.T) {
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
	cs := crm.NewStore(pool)
	cs.Tasks = ts
	s := NewStore(pool, ts, cs, company.NewStore(pool))

	typ, err := cs.AddType(ctx, "Договоры", "#3366cc")
	must(err)
	must(cs.SetTypeParents(ctx, typ, []string{""})) // bound to deals
	smart, err := cs.PipelinesOf(ctx, typ)
	must(err)
	pipes, err := cs.Pipelines(ctx)
	must(err)
	second := smart[0].Stages[1].ID

	var ve *i18n.Error
	bad := []Template{
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "create", Text: "x", Entity: "lead", Type: typ}}},                               // a lead of a smart process
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "create", Text: "x", Entity: "deal", Type: typ, Stage: pipes[0].Stages[0].ID}}}, // a stage of deals
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "create", Text: "x", Entity: "deal", Type: boss}}},                              // no such smart process
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmupdate"}}},                                                                  // nothing to set
		{Name: "Плохой", Steps: []Step{{Kind: "crmupdate", Sets: []Assign{{Field: "title", Value: "x"}}}}},                                    // no card
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmupdate", Sets: []Assign{{Field: "nonsense", Value: "x"}}}}},                 // no such field
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmread", Type: typ}}},                                                         // not of the step
	}
	for i, tp := range bad {
		if _, err := s.SaveTemplate(ctx, boss, tp); !errors.As(err, &ve) {
			t.Fatalf("bad template %d saved: %v", i, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Договор по сделке", Entity: "deal", Active: true,
		Steps: []Step{
			{Kind: "create", Text: "Договор", Entity: "deal", Type: typ, Stage: second, Sets: []Assign{{Field: "amount", Value: "500"}}},
			{Kind: "crmupdate", Entity: "deal", Value: "{created_card}", Sets: []Assign{{Field: "title", Value: "Договор подписан"}, {Field: "amount", Value: "700"}}},
			{Kind: "crmupdate", Sets: []Assign{{Field: "comment", Value: "договор готов"}}},
		}})
	must(err)
	deal, err := cs.SaveDeal(ctx, boss, crm.Deal{Title: "Окна", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	id, err := s.Start(ctx, boss, tpl, deal, nil)
	must(err)
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("status %s: %+v", x.Status, lines)
	}
	kids, err := cs.Children(ctx, boss, deal)
	must(err)
	if len(kids) != 1 || kids[0].TypeID != typ || kids[0].Stage.ID != second || kids[0].Title != "Договор подписан" || kids[0].Amount != 700 {
		t.Fatalf("the element: %+v", kids)
	}
	if x.Values["created_card"] != "/crm/deals/"+kids[0].ID {
		t.Fatalf("created_card %q", x.Values["created_card"])
	}
	if d, _ := cs.Deal(ctx, deal); d.Comment != "договор готов" || d.Title != "Окна" {
		t.Fatalf("the deal: %+v", d)
	}

	// without a stage — the first one of the smart process; a process not of a deal binds nothing
	plain, err := s.SaveTemplate(ctx, boss, Template{Name: "Просто элемент", Active: true,
		Steps: []Step{{Kind: "create", Text: "Без стадии", Entity: "deal", Type: typ}}})
	must(err)
	id, err = s.Start(ctx, boss, plain, "", nil)
	must(err)
	x, err = s.instance(ctx, id)
	must(err)
	var stage, pipe string
	must(pool.QueryRow(ctx, `SELECT stage_id::text, pipeline_id::text FROM crm_deals WHERE title = 'Без стадии'`).Scan(&stage, &pipe))
	if x.Status != "done" || stage != smart[0].Stages[0].ID || pipe != smart[0].ID {
		t.Fatalf("status %s, stage %s of %s", x.Status, stage, pipe)
	}

	// the file names the smart process; the import finds it by the name
	f, err := s.Export(ctx, tpl)
	must(err)
	if f.Types[typ] != "Договоры" {
		t.Fatalf("types %+v", f.Types)
	}
	f.Steps[0].Type, f.Types = "00000000-0000-0000-0000-000000000000", map[string]string{"00000000-0000-0000-0000-000000000000": "договоры"}
	f.Stages = map[string]string{second: f.Stages[second]}
	data, err := json.Marshal(f)
	must(err)
	got, _, err := s.Import(ctx, boss, "", data)
	must(err)
	imp, err := s.Template(ctx, got)
	must(err)
	if imp.Steps[0].Type != typ || imp.Steps[0].Stage != second {
		t.Fatalf("imported %+v", imp.Steps[0])
	}
	f.Types = map[string]string{f.Steps[0].Type: "Нет такого"}
	data, _ = json.Marshal(f)
	if _, _, err := s.Import(ctx, boss, "", data); !errors.As(err, &ve) {
		t.Fatalf("an unknown smart process imported: %v", err)
	}
}
