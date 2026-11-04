package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTriggersMore: the client's call and missed call move the deals of the contact and the company, a condition
// keeps a card that does not meet it, «Webhook» moves by its code and back when allowed, «Изменение полей» moves
// only when its field changed.
func TestTriggersMore(t *testing.T) {
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
	stageOf := func(id string) string {
		t.Helper()
		d, err := s.Deal(ctx, id)
		must(err)
		return d.Stage.ID
	}
	contact, err := s.SaveContact(ctx, boss, Contact{Name: "Клиент", Phone: "+79990000001", Responsible: Ref{ID: boss}})
	must(err)
	company, err := s.SaveCompany(ctx, boss, Company{Title: "ООО Клиент", Responsible: Ref{ID: boss}})
	must(err)
	d1, err := s.SaveDeal(ctx, boss, Deal{Title: "Д1", PipelineID: pipes[0].ID, Amount: 100, Contacts: []Ref{{ID: contact}}, Responsible: Ref{ID: boss}})
	must(err)
	d2, err := s.SaveDeal(ctx, boss, Deal{Title: "Д2", PipelineID: pipes[0].ID, Company: Ref{ID: company}, Responsible: Ref{ID: boss}})
	must(err)

	must(s.AddTrigger(ctx, boss, st[1].ID, "call", TriggerParams{}))
	must(s.AddTrigger(ctx, boss, st[2].ID, "call_missed", TriggerParams{}))
	must(s.AddTrigger(ctx, boss, st[3].ID, "call", TriggerParams{If: &RobotCond{Field: "amount", Op: "gt", Value: "500"}}))
	must(s.FireCall(ctx, "contact", contact, false))
	must(s.FireCall(ctx, "company", company, true))
	if stageOf(d1) != st[1].ID || stageOf(d2) != st[2].ID {
		t.Fatalf("calls: D1 at %s, D2 at %s", stageOf(d1), stageOf(d2))
	}
	must(s.FireCall(ctx, "contact", contact, false)) // the amount is 100: the condition keeps it
	if stageOf(d1) != st[1].ID {
		t.Fatalf("a trigger moved a card not meeting its condition")
	}
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET amount = 1000 WHERE id = $1`, d1)
	must(err)
	must(s.FireCall(ctx, "contact", contact, false))
	if stageOf(d1) != st[3].ID {
		t.Fatalf("a card meeting the condition stayed")
	}

	// «Webhook»: by its code; back only when allowed
	must(s.AddTrigger(ctx, boss, st[1].ID, "webhook", TriggerParams{Code: "mine"}))
	trs, err := s.Triggers(ctx, []string{st[1].ID})
	must(err)
	code := ""
	for _, tr := range trs {
		if tr.Kind == "webhook" {
			code = tr.Params.Code
		}
	}
	if code == "" || code == "mine" {
		t.Fatalf("webhook code = %q", code)
	}
	if err := s.FireHook(ctx, "deal", d1, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown code: %v", err)
	}
	must(s.FireHook(ctx, "deal", d1, code))
	if stageOf(d1) != st[3].ID {
		t.Fatalf("a trigger behind moved the card back without «back»")
	}
	_, err = pool.Exec(ctx, `UPDATE crm_triggers SET params = params || '{"back": true}' WHERE kind = 'webhook'`)
	must(err)
	must(s.FireHook(ctx, "deal", d1, code))
	if stageOf(d1) != st[1].ID {
		t.Fatalf("«back» did not move the card back")
	}

	// «Изменение полей»
	if err := s.AddTrigger(ctx, boss, st[4].ID, "field", TriggerParams{}); err == nil {
		t.Fatalf("a fields trigger without fields")
	}
	if err := s.AddTrigger(ctx, boss, st[4].ID, "field", TriggerParams{Fields: []string{"bogus"}}); err == nil {
		t.Fatalf("a fields trigger with an unknown field")
	}
	must(s.AddTrigger(ctx, boss, st[4].ID, "field", TriggerParams{Fields: []string{"comment"}}))
	before, err := s.CardSnapshot(ctx, "deal", d1)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET title = 'Д1 новая' WHERE id = $1`, d1)
	must(err)
	must(s.FireChanged(ctx, "deal", d1, before))
	if stageOf(d1) != st[1].ID {
		t.Fatalf("another field's change fired «Изменение полей»")
	}
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET comment = 'Перезвонить' WHERE id = $1`, d1)
	must(err)
	must(s.FireChanged(ctx, "deal", d1, before))
	if stageOf(d1) != st[4].ID {
		t.Fatalf("the comment changed but the card stayed")
	}
}

// TestDocTriggers: the client's first opening of a document's public link moves the deal by «Клиент просмотрел
// документ» and notes it in the history (the next openings do not), a signed document moves by «Документ подписан»;
// an invoice's document moves the invoice's deal.
func TestDocTriggers(t *testing.T) {
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
	var boss, file string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by) VALUES ('d1', 'Договор.docx', 10, $1) RETURNING id`, boss).Scan(&file))
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	stageOf := func(id string) string {
		t.Helper()
		d, err := s.Deal(ctx, id)
		must(err)
		return d.Stage.ID
	}
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Д", PipelineID: pipes[0].ID, Responsible: Ref{ID: boss}})
	must(err)
	must(s.AddTrigger(ctx, boss, st[2].ID, "doc_viewed", TriggerParams{}))
	must(s.AddTrigger(ctx, boss, st[3].ID, "doc_signed", TriggerParams{}))
	docID, err := s.AddDocument(ctx, Document{TemplateName: "Договор", Entity: "deal", EntityID: deal, Number: "7", FileID: file, Values: map[string]string{}, AuthorID: boss})
	must(err)
	doc, err := s.Document(ctx, "deal", deal, docID)
	must(err)
	if doc.ViewedAt != nil {
		t.Fatalf("a new document is viewed")
	}
	first, err := s.DocumentViewed(ctx, doc)
	must(err)
	if !first || stageOf(deal) != st[2].ID {
		t.Fatalf("viewed: first %v, stage %s", first, stageOf(deal))
	}
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET stage_id = $2 WHERE id = $1`, deal, st[0].ID)
	must(err)
	if again, err := s.DocumentViewed(ctx, doc); err != nil || again || stageOf(deal) != st[0].ID {
		t.Fatalf("the second view: %v %v", again, err)
	}
	doc, _ = s.Document(ctx, "deal", deal, docID)
	if doc.ViewedAt == nil {
		t.Fatalf("no viewed_at")
	}
	must(s.DocumentSigned(ctx, "deal", deal, boss, "Договор № 7"))
	if stageOf(deal) != st[3].ID {
		t.Fatalf("signed: stage %s", stageOf(deal))
	}
	evs, err := s.Timeline(ctx, "deal", deal)
	must(err)
	kinds := map[string]string{}
	for _, e := range evs {
		kinds[e.Kind] = e.Body
	}
	if kinds["doc_viewed"] != "Договор № 7" || kinds["doc_signed"] != "Договор № 7" {
		t.Fatalf("timeline = %v", kinds)
	}

	deal2, err := s.SaveDeal(ctx, boss, Deal{Title: "Д2", PipelineID: pipes[0].ID, Responsible: Ref{ID: boss}})
	must(err)
	var inv string
	must(pool.QueryRow(ctx, `INSERT INTO crm_invoices (kind, number, deal_id, title, status, responsible_id, created_by)
		VALUES ('invoice', 1, $1, 'Счёт', 'new', $2, $2) RETURNING id`, deal2, boss).Scan(&inv))
	must(s.DocumentSigned(ctx, TemplateInvoice, inv, boss, "Счёт"))
	if stageOf(deal2) != st[3].ID {
		t.Fatalf("an invoice's document did not move its deal")
	}
}
