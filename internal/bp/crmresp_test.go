package bp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCRMResponsibleProduct: «Смена ответственного» gives a deal to the people in turn, the absent skipped; «Добавление
// товара» adds the catalog's rows to a deal and to a lead by a link — the price of the catalog or a formula, the
// quantity and the discount of the process's fields; the amount is counted from the rows.
func TestCRMResponsibleProduct(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, ann, bob, eve := newUser("boss"), newUser("ann"), newUser("bob"), newUser("eve")
	ts := tasks.NewStore(pool)
	cs := crm.NewStore(pool)
	cs.Tasks = ts
	co := company.NewStore(pool)
	s := NewStore(pool, ts, cs, co)

	vat := 20.0
	lic, err := cs.SaveProduct(ctx, boss, crm.Product{Name: "Лицензия", SKU: "L-1", Price: 1000, Unit: "шт", VATRate: &vat, VATIncluded: true})
	must(err)

	pipes, err := cs.Pipelines(ctx)
	must(err)

	var ve *i18n.Error
	bad := []Template{
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmresponsible"}}},                                    // no one
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmresponsible", Users: []string{ann}, Mode: "add"}}}, // the mode
		{Name: "Плохой", Steps: []Step{{Kind: "crmresponsible", Users: []string{ann}}}},                              // no card
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmproduct"}}},                                        // no product
		{Name: "Плохой", Entity: "contact", Steps: []Step{{Kind: "crmproduct", Product: lic}}},                       // a contact has no rows
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmread", Product: lic}}},                             // not of the step
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmproduct", Product: lic, Skip: true}}},              // not of the step
	}
	for i, tp := range bad {
		if _, err := s.SaveTemplate(ctx, boss, tp); !errors.As(err, &ve) {
			t.Fatalf("bad template %d saved: %v", i, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Распределение", Entity: "deal", Active: true,
		Fields: []Field{{Key: "n", Label: "Сколько", Kind: "number"}, {Key: "lead", Label: "Лид", Kind: "string"}},
		Steps: []Step{
			{Kind: "crmresponsible", Users: []string{ann, bob, eve}, Mode: "queue", Skip: true},
			{Kind: "crmproduct", Product: lic, Quantity: "{n}"},
			{Kind: "crmproduct", Product: lic, Price: "= {n} * 100", Discount: "10"},
			{Kind: "crmproduct", Entity: "lead", Value: "{lead}", Product: lic},
		}})
	must(err)
	lead, err := cs.SaveLead(ctx, boss, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: boss}})
	must(err)
	run := func() (crm.Deal, Instance) {
		t.Helper()
		deal, err := cs.SaveDeal(ctx, boss, crm.Deal{Title: "Окна", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
		must(err)
		id, err := s.Start(ctx, boss, tpl, deal, map[string]string{"n": "3", "lead": "/crm/leads/" + lead})
		must(err)
		x, err := s.instance(ctx, id)
		must(err)
		if x.Status != "done" {
			lines, _ := s.Log(ctx, id)
			t.Fatalf("status %s: %+v", x.Status, lines)
		}
		d, err := cs.Deal(ctx, deal)
		must(err)
		return d, x
	}
	d, _ := run()
	if d.Responsible.ID != ann {
		t.Fatalf("first in turn: %s", d.Responsible.ID)
	}
	rows, err := cs.ProductRows(ctx, "deal", d.ID)
	must(err)
	if len(rows) != 2 || rows[0].Name != "Лицензия" || rows[0].Price != 1000 || rows[0].Quantity != 3 || rows[0].Unit != "шт" ||
		rows[0].VATRate == nil || *rows[0].VATRate != 20 || rows[1].Price != 300 || rows[1].Quantity != 1 || rows[1].Discount != 10 {
		t.Fatalf("rows: %+v", rows)
	}
	if d.Amount != 3000+270 {
		t.Fatalf("amount %v", d.Amount)
	}
	must(co.AddAbsence(ctx, bob, bob, "vacation", time.Now(), time.Now(), "", true, "", nil))
	if d, _ = run(); d.Responsible.ID != eve {
		t.Fatalf("bob is away, then eve: %s", d.Responsible.ID)
	}
	if d, _ = run(); d.Responsible.ID != ann {
		t.Fatalf("round again: %s", d.Responsible.ID)
	}
	if rows, _ = cs.ProductRows(ctx, "lead", lead); len(rows) != 3 || rows[2].ProductID != lic {
		t.Fatalf("the lead's rows: %+v", rows)
	}

	// a product gone from the catalog fails the step
	must(cs.DeleteProduct(ctx, lic))
	deal, err := cs.SaveDeal(ctx, boss, crm.Deal{Title: "Двери", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	id, err := s.Start(ctx, boss, tpl, deal, map[string]string{"n": "1", "lead": lead})
	must(err)
	if x, _ := s.instance(ctx, id); x.Status != "failed" || x.Step != 1 {
		t.Fatalf("status %s at %d", x.Status, x.Step)
	}
}
