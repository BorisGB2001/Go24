package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSimpleMode: in «Простой режим» a new lead is a deal with a contact (and the company it names) at once; going
// simple converts the leads in work, leaves the closed ones; back to classic, leads stay leads.
func TestSimpleMode(t *testing.T) {
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

	if simple, err := s.Simple(ctx); err != nil || simple {
		t.Fatalf("classic by default: %v %v", simple, err)
	}
	inWork, err := s.SaveLead(ctx, boss, Lead{Title: "В работе", Phone: "+7 900 111-22-33", Responsible: me})
	must(err)
	lost, err := s.SaveLead(ctx, boss, Lead{Title: "Провален", Email: "lost@x.ru", Responsible: me})
	must(err)
	must(pool.QueryRow(ctx, `UPDATE crm_leads SET stage_id = (SELECT id FROM crm_stages WHERE entity = 'lead' AND semantic = 'failure' LIMIT 1)
		WHERE id = $1 RETURNING id`, lost).Scan(&lost))
	if l, _ := s.Lead(ctx, inWork); l.DealID != "" {
		t.Fatal("classic: a lead stays a lead")
	}

	done, failed, err := s.SetSimple(ctx, boss, true, true)
	must(err)
	if done != 1 || failed != 0 {
		t.Fatalf("going simple converts the lead in work only: %d %d", done, failed)
	}
	if l, _ := s.Lead(ctx, inWork); l.DealID == "" || l.ContactID == "" || l.Stage.Semantic != "success" {
		t.Fatalf("the lead in work converted: %+v", l)
	}
	if l, _ := s.Lead(ctx, lost); l.DealID != "" {
		t.Fatal("a closed lead is left")
	}

	id, err := s.SaveLead(ctx, boss, Lead{Title: "С сайта", Name: "Пётр", CompanyTitle: "ООО Лютик", Email: "petr@lutik.ru",
		Amount: 500, Responsible: me})
	must(err)
	l, err := s.Lead(ctx, id)
	must(err)
	if l.DealID == "" || l.ContactID == "" || l.CompanyID == "" {
		t.Fatalf("simple: a new lead is a deal with its contact and company: %+v", l)
	}
	d, err := s.Deal(ctx, l.DealID)
	must(err)
	if d.Title != "С сайта" || d.Amount != 500 || d.Contact.ID != l.ContactID || d.Company.ID != l.CompanyID {
		t.Fatalf("the deal of the lead: %+v", d)
	}
	c, err := s.Contact(ctx, l.ContactID)
	must(err)
	if c.Name != "Пётр" || c.Email != "petr@lutik.ru" {
		t.Fatalf("the contact of the lead: %+v", c)
	}

	_, _, err = s.SetSimple(ctx, boss, false, true)
	must(err)
	id, err = s.SaveLead(ctx, boss, Lead{Title: "Снова классика", Responsible: me})
	must(err)
	if l, _ := s.Lead(ctx, id); l.DealID != "" {
		t.Fatal("classic again: a lead stays a lead")
	}
}
