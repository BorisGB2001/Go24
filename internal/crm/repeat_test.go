package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRepeatLeads: a lead with the phone or e-mail of a known contact or company is «Повторный лид» linked to the
// client (a contact's company too), both timelines tell of it, the list filters by it, and converting it uses the
// client instead of making a new one.
func TestRepeatLeads(t *testing.T) {
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
	me := Ref{ID: boss}
	co, err := s.SaveCompany(ctx, boss, Company{Title: "ООО Ромашка", Email: "office@romashka.ru", Responsible: me})
	must(err)
	ivan, err := s.SaveContact(ctx, boss, Contact{Name: "Иван", Phone: "+7 (900) 111-22-33", Company: Ref{ID: co}, Responsible: me})
	must(err)

	first, err := s.SaveLead(ctx, boss, Lead{Title: "Новый клиент", Phone: "+7 900 999-88-77", Responsible: me})
	must(err)
	byPhone, err := s.SaveLead(ctx, boss, Lead{Title: "Снова Иван", Phone: "8 900 111 22 33", Responsible: me})
	must(err)
	byMail, err := s.SaveLead(ctx, boss, Lead{Title: "Письмо в офис", Email: "Office@Romashka.ru", Responsible: me})
	must(err)

	l, _ := s.Lead(ctx, first)
	if l.Repeat || l.ContactID != "" || l.Converted() {
		t.Fatalf("a lead of a new client: %+v", l)
	}
	l, _ = s.Lead(ctx, byPhone)
	if !l.Repeat || l.ContactID != ivan || l.CompanyID != co || l.Converted() {
		t.Fatalf("a repeat lead by the phone: %+v", l)
	}
	l, _ = s.Lead(ctx, byMail)
	if !l.Repeat || l.ContactID != "" || l.CompanyID != co {
		t.Fatalf("a repeat lead by the company's e-mail: %+v", l)
	}
	has := func(entity, id, body string) bool {
		t.Helper()
		evs, err := s.Timeline(ctx, entity, id)
		must(err)
		for _, e := range evs {
			if e.Kind == "repeat" && e.Body == body {
				return true
			}
		}
		return false
	}
	if !has("lead", byPhone, "contact:"+ivan) || !has("lead", byPhone, "company:"+co) || !has("contact", ivan, "lead:"+byPhone) ||
		!has("company", co, "lead:"+byMail) {
		t.Fatal("the repeat leads are not in the timelines")
	}

	for _, c := range []struct {
		repeat string
		want   int
	}{{"", 3}, {"1", 2}, {"0", 1}} {
		list, err := s.LeadList(ctx, boss, Filter{Repeat: c.repeat})
		must(err)
		if len(list) != c.want {
			t.Errorf("repeat=%q: %d leads", c.repeat, len(list))
		}
	}

	// converting a repeat lead: a deal of its client, no new contact or company
	deal, err := s.ConvertLead(ctx, boss, byPhone)
	must(err)
	d, _ := s.Deal(ctx, deal)
	if d.Contact.ID != ivan || d.Company.ID != co {
		t.Fatalf("the deal of a repeat lead: %+v", d)
	}
	var contacts, companies int
	must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM crm_contacts), (SELECT count(*) FROM crm_companies)`).Scan(&contacts, &companies))
	if contacts != 1 || companies != 1 {
		t.Fatalf("converting made clients: %d contacts, %d companies", contacts, companies)
	}
	if l, _ = s.Lead(ctx, byPhone); !l.Converted() {
		t.Fatalf("a converted repeat lead: %+v", l)
	}
}
