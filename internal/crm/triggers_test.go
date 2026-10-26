package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTriggers: a deed done, a client's letter, a paid invoice and an open line's message move cards forward to the
// trigger's stage — never back; a converted lead's event moves its deal.
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages // new, preparation, prepayment, executing, final_invoice, won, lose
	must(s.AddTrigger(ctx, boss, st[1].ID, "mail"))
	must(s.AddTrigger(ctx, boss, st[1].ID, "mail")) // twice is once
	must(s.AddTrigger(ctx, boss, st[2].ID, "activity"))
	must(s.AddTrigger(ctx, boss, st[5].ID, "invoice_paid"))
	trs, _ := s.Triggers(ctx, []string{st[1].ID, st[2].ID, st[5].ID})
	if len(trs) != 3 {
		t.Fatalf("triggers = %+v", trs)
	}
	stageOf := func(id string) string {
		t.Helper()
		d, err := s.Deal(ctx, id)
		must(err)
		return d.Stage.ID
	}
	newDeal := func(title string) string {
		id, err := s.SaveDeal(ctx, boss, Deal{Title: title, PipelineID: pipes[0].ID, Amount: 100, Responsible: Ref{ID: boss}})
		must(err)
		return id
	}

	// a deed done: new → prepayment
	a := newDeal("А")
	act, err := s.AddActivity(ctx, boss, Activity{Entity: "deal", EntityID: a, Kind: "call", Subject: "Позвонить", Deadline: time.Now()})
	must(err)
	_, err = s.CompleteActivity(ctx, boss, act)
	must(err)
	if stageOf(a) != st[2].ID {
		t.Fatalf("a deed done did not move the deal")
	}

	// a client's letter: B at «new» goes to preparation, A (already further) stays
	b := newDeal("Б")
	_, err = s.RunTriggersDue(ctx, time.Now()) // the first run starts from now
	must(err)
	for _, id := range []string{a, b} {
		_, err = pool.Exec(ctx, `INSERT INTO crm_mail (entity, entity_id, from_addr, subject, sent_at, linked_by) VALUES ('deal', $1, 'c@client.test', 'Вопрос', now(), $2)`, id, boss)
		must(err)
	}
	n, err := s.RunTriggersDue(ctx, time.Now().Add(time.Minute))
	must(err)
	if n != 2 || stageOf(b) != st[1].ID || stageOf(a) != st[2].ID {
		t.Fatalf("mail triggers: %d, B at %s, A at %s", n, stageOf(b), stageOf(a))
	}
	if n, _ := s.RunTriggersDue(ctx, time.Now().Add(2*time.Minute)); n != 0 {
		t.Fatalf("the same letters fired again: %d", n)
	}

	// an invoice paid: the deal is won
	inv, err := s.MakeInvoice(ctx, boss, "invoice", a, 0)
	must(err)
	must(s.SetInvoiceStatus(ctx, boss, inv, "paid"))
	if d, _ := s.Deal(ctx, a); d.Stage.ID != st[5].ID || d.ClosedAt == nil {
		t.Fatalf("a paid invoice did not win the deal: %+v", d.Stage)
	}

	// leads: a message in the open line moves the lead; once converted, its deal
	leadStages, _ := s.LeadStages(ctx)
	must(s.AddTrigger(ctx, boss, leadStages[1].ID, "openline"))
	must(s.AddTrigger(ctx, boss, st[3].ID, "openline"))
	lead, err := s.SaveLead(ctx, boss, Lead{Title: "Сайт", Name: "Гость", Responsible: Ref{ID: boss}})
	must(err)
	must(s.Fire(ctx, "lead", lead, "openline"))
	if l, _ := s.Lead(ctx, lead); l.Stage.ID != leadStages[1].ID {
		t.Fatalf("an open line message did not move the lead")
	}
	deal, err := s.ConvertLead(ctx, boss, lead)
	must(err)
	must(s.Fire(ctx, "lead", lead, "openline"))
	if stageOf(deal) != st[3].ID {
		t.Fatalf("a converted lead's event did not move its deal")
	}
}
