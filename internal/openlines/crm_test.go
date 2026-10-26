package openlines

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestCRM: a line with «Создавать лид автоматически» makes a lead of a new dialogue (responsible — its operator, who
// follows the dialogue), the form fills the lead in; a visitor CRM knows by phone or e-mail is bound to their contact;
// the card's timeline has the dialogue and «Открыть чат».
func TestCRM(t *testing.T) {
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
	boss, a, b := newUser("boss"), newUser("a"), newUser("b")
	c := crm.NewStore(pool)
	s := NewStore(pool, c)
	s.Tr = func(key string, args ...any) string {
		out := key
		for _, x := range args {
			out += " " + x.(string)
		}
		return out
	}
	timeline := func(entity, id string) (n int) {
		t.Helper()
		list, err := c.Timeline(ctx, entity, id)
		must(err)
		for _, e := range list {
			if e.Kind == "openline" {
				n++
			}
		}
		return n
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a, b}, QueueMode: "strict", CRMAuto: true})
	must(err)

	// a new dialogue: the line's lead, given to the operator the queue chose
	sid, tok, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok, "Здравствуйте"))
	must(s.VisitorSay(ctx, wid, tok, "Есть доставка?"))
	x, _, err := s.Open(ctx, a, sid)
	must(err)
	if x.LeadID == "" {
		t.Fatal("no lead of the new dialogue")
	}
	l, err := c.Lead(ctx, x.LeadID)
	must(err)
	if l.Responsible.ID != x.OperatorID || l.Title != "ol.lead_title Линия #"+sid[:6] || l.Source != "ol.lead_source" {
		t.Fatalf("the line's lead: %+v (operator %s)", l, x.OperatorID)
	}
	if timeline("lead", l.ID) != 1 {
		t.Fatal("the lead's timeline has no dialogue")
	}
	if chat, _ := s.ChatOf(ctx, "lead", l.ID); chat != sid {
		t.Fatalf("«Открыть чат» of the lead: %q", chat)
	}
	// another operator answers: the lead follows the dialogue
	other := a
	if x.OperatorID == a {
		other = b
	}
	must(s.Reply(ctx, other, sid, "Да"))
	if l, _ = c.Lead(ctx, x.LeadID); l.Responsible.ID != other {
		t.Fatalf("the lead's responsible after the answer: %s, want %s", l.Responsible.ID, other)
	}
	must(s.Transfer(ctx, other, sid, x.OperatorID))
	if l, _ = c.Lead(ctx, x.LeadID); l.Responsible.ID != x.OperatorID {
		t.Fatal("the lead's responsible after the transfer")
	}
	// the form fills the lead in, no second lead
	must(s.Contact(ctx, wid, tok, "Иван", "+7 900 111-22-33", "ivan@shop.ru", "", "Сайт"))
	if l, _ = c.Lead(ctx, x.LeadID); l.Name != "Иван" || l.Phone != "+7 900 111-22-33" || l.Email != "ivan@shop.ru" {
		t.Fatalf("the lead filled in: %+v", l)
	}
	var leads int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads`).Scan(&leads))
	if leads != 1 {
		t.Fatalf("leads = %d", leads)
	}

	// a client CRM knows: the form binds the dialogue to their contact
	petr, err := c.SaveContact(ctx, boss, crm.Contact{Name: "Пётр", Phone: "8 (900) 555-44-33", Responsible: crm.Ref{ID: boss}})
	must(err)
	w, _ := s.Widget(ctx, wid)
	w.CRMAuto = false
	_, err = s.SaveWidget(ctx, boss, w)
	must(err)
	sid2, tok2, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok2, "Это Пётр"))
	if x, _, _ = s.Open(ctx, a, sid2); x.LeadID != "" || x.ContactID != "" {
		t.Fatalf("no lead without «Создавать лид автоматически»: %+v", x)
	}
	must(s.Contact(ctx, wid, tok2, "Пётр", "+79005554433", "", "", "Сайт"))
	if x, _, _ = s.Open(ctx, a, sid2); x.ContactID != petr || x.LeadID != "" {
		t.Fatalf("bound to the contact: %+v", x)
	}
	if timeline("contact", petr) != 1 {
		t.Fatal("the contact's timeline has no dialogue")
	}
	if card, err := s.MakeLead(ctx, a, sid2, "Сайт"); err != nil || card != "/crm/contacts/"+petr {
		t.Fatalf("«Создать лид» of a bound dialogue: %q %v", card, err)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads`).Scan(&leads))
	if leads != 1 {
		t.Fatalf("leads = %d", leads)
	}

	// the operator's «Создать лид»: the lead with this e-mail is found, not made again
	sid3, tok3, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok3, "Снова я"))
	must(pool.QueryRow(ctx, `UPDATE ol_sessions SET email = 'IVAN@shop.ru' WHERE id = $1 RETURNING id`, sid3).Scan(new(string)))
	if card, err := s.MakeLead(ctx, a, sid3, "Сайт"); err != nil || card != "/crm/leads/"+l.ID {
		t.Fatalf("«Создать лид» of a known e-mail: %q %v", card, err)
	}
	if timeline("lead", l.ID) != 2 {
		t.Fatal("the second dialogue in the lead's timeline")
	}
	// the operator answering a dialogue bound by hand does not take the lead
	must(s.Reply(ctx, b, sid3, "Здравствуйте"))
	if l2, _ := c.Lead(ctx, l.ID); l2.Responsible.ID != x.OperatorID && l2.Responsible.ID != l.Responsible.ID {
		t.Fatalf("a lead bound by hand changed hands: %+v", l2)
	}
}
