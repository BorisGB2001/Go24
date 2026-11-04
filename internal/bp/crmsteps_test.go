package bp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCRMSteps: a process makes a lead, reads its fields (a step goes to its responsible, a condition checks them),
// converts it into a deal and a contact, reads the deal and deletes the contact — the starter without CRM rights.
func TestCRMSteps(t *testing.T) {
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
	boss, ann := newUser("boss"), newUser("Анна Петрова")
	ts := tasks.NewStore(pool)
	cs := crm.NewStore(pool)
	cs.Tasks = ts
	s := NewStore(pool, ts, cs, company.NewStore(pool))
	type message struct{ from, to, text string }
	var messages []message
	s.Chat = func(_ context.Context, from, to, text string) error {
		messages = append(messages, message{from, to, text})
		return nil
	}

	var ve *i18n.Error
	bad := []Template{
		{Name: "Плохой", Steps: []Step{{Kind: "crmread"}}},                                                // no card of the process
		{Name: "Плохой", Entity: "deal", Steps: []Step{{Kind: "crmconvert"}}},                             // no lead
		{Name: "Плохой", Entity: "lead", Steps: []Step{{Kind: "crmconvert", Into: []string{"fax"}}}},      // what to make
		{Name: "Плохой", Entity: "lead", Steps: []Step{{Kind: "crmdelete", Entity: "invoice"}}},           // no such card
		{Name: "Плохой", Entity: "lead", Steps: []Step{{Kind: "crmread", Into: []string{"deal"}}}},        // Into of a read
		{Name: "Плохой", Entity: "lead", Steps: []Step{{Kind: "crmconvert", Entity: "deal", Value: "1"}}}, // a kind of a conversion
	}
	for i, tp := range bad {
		if _, err := s.SaveTemplate(ctx, boss, tp); !errors.As(err, &ve) {
			t.Fatalf("bad template %d saved: %v", i, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Лид в сделку", Active: true,
		Steps: []Step{
			{Kind: "create", Entity: "lead", Text: "Заявка {starter}", To: ann, Sets: []Assign{{Field: "name", Value: "Иван Сидоров"},
				{Field: "phone", Value: "+7 900 111"}, {Field: "amount", Value: "700"}}},
			{Kind: "crmread", Entity: "lead"},
			{Kind: "if", Field: "crm_phone", Op: "filled", Goto: 7},
			{Kind: "chat", From: boss, To: FieldPrefix + "crm_responsible", Text: "{crm_title}: {crm_name}, {crm_amount}"},
			{Kind: "crmconvert", Value: "{crm_link}", Into: []string{"deal", "contact"}},
			{Kind: "crmread", Entity: "deal", Value: "{converted_deal}"},
			{Kind: "crmdelete", Entity: "contact", Value: "{converted_contact_id}"},
		}})
	must(err)
	tp, err := s.Template(ctx, tpl)
	must(err)
	if f, ok := (Instance{Fields: AllFields(tp.Fields, tp.Steps)}).Field("crm_responsible"); !ok || f.Kind != "user" || UserInfoLabel(f.Key) != "bp.crminfo.responsible" {
		t.Fatalf("the CRM fields: %+v", tp.Steps)
	}
	id, err := s.Start(ctx, ann, tpl, "", nil) // Anna has no rights to delete contacts: the step acts as a robot
	must(err)
	x, err := s.instance(ctx, id)
	must(err)
	if x.Status != "done" {
		lines, _ := s.Log(ctx, id)
		t.Fatalf("status %s: %+v", x.Status, lines)
	}
	if len(messages) != 1 || messages[0].to != ann || messages[0].text != "Заявка Анна Петрова: Иван Сидоров, 700" {
		t.Fatalf("messages: %+v", messages)
	}
	lead, ok := strings.CutPrefix(x.Values["created_card"], "/crm/deals/")
	if !ok || x.Values["converted_deal_id"] != lead || x.Values["crm_id"] != lead || x.Values["crm_title"] != "Заявка Анна Петрова" ||
		x.Values["crm_name"] != "" || x.Values["crm_contact"] != "/crm/contacts/"+x.Values["converted_contact_id"] || x.Values["converted_company"] != "" {
		t.Fatalf("values: %+v", x.Values)
	}
	d, err := cs.Deal(ctx, lead)
	must(err)
	if d.Responsible.ID != ann || d.Amount != 700 {
		t.Fatalf("the deal: %+v", d)
	}
	if _, err := cs.Contact(ctx, x.Values["converted_contact_id"]); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("the contact deleted: %v", err)
	}

	// a lead's own process converts it as the dialog offers; a card that is gone fails the step
	own, err := s.SaveTemplate(ctx, boss, Template{Name: "Конвертация", Entity: "lead", Active: true,
		Steps: []Step{{Kind: "crmconvert"}, {Kind: "crmdelete", Entity: "lead", Value: "{converted_deal_id}"}}})
	must(err)
	l, err := cs.SaveLead(ctx, boss, crm.Lead{Title: "Звонок", Name: "Пётр", Responsible: crm.Ref{ID: boss}})
	must(err)
	id, err = s.Start(ctx, boss, own, l, nil)
	must(err)
	x, err = s.instance(ctx, id)
	must(err)
	got, err := cs.Lead(ctx, l)
	must(err)
	if x.Status != "failed" || x.Step != 1 || got.DealID == "" || got.ContactID == "" || x.Values["converted_contact_id"] != got.ContactID {
		t.Fatalf("status %s at %d, the lead %+v, values %+v", x.Status, x.Step, got, x.Values)
	}
}
