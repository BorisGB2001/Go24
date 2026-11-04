package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMCopy: «Копировать» makes a new card with the original's fields, own fields, client, contacts, observers,
// phones and product rows; the timeline stays with the original; a given responsible replaces the original's.
func TestCRMCopy(t *testing.T) {
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
	var anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)

	co, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка", Comms: []Comm{{"phone", "work", "+7 900 111-22-33"}, {"email", "work", "info@romashka.ru"}}})
	must(err)
	ct, err := s.SaveContact(ctx, anna, Contact{Name: "Иван", Company: Ref{ID: co}})
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Stage: pipes[0].Stages[1], Currency: "RUB",
		Company: Ref{ID: co}, Contacts: []Ref{{ID: ct}}, Responsible: Ref{ID: boris}, Observers: []Ref{{ID: anna}}, Comment: "срочно"})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{Name: "Стол", Price: 100, Quantity: 2}, {Name: "Стул", Price: 50, Quantity: 4}})
	must(err)
	ts := tasks.NewStore(pool)
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Договор", Kind: "string"}))
	fields, err := ts.FieldsOf(ctx, tasks.CRMFieldSets["deal"])
	must(err)
	must(s.SetFields(ctx, anna, "deal", deal, map[string]string{fields[0].ID: "Д-1"}))
	must(s.Comment(ctx, anna, "deal", deal, "звонил"))

	cp, err := s.Copy(ctx, anna, "deal", deal, "")
	must(err)
	if cp == deal {
		t.Fatal("the copy is the original")
	}
	d, err := s.Deal(ctx, cp)
	must(err)
	if d.Title != "Поставка" || d.Stage.ID != pipes[0].Stages[1].ID || d.Company.ID != co || len(d.Contacts) != 1 || d.Contacts[0].ID != ct ||
		d.Responsible.ID != boris || len(d.Observers) != 1 || d.Observers[0].ID != anna || d.Comment != "срочно" || d.Amount != 400 ||
		d.CreatedBy != anna {
		t.Fatalf("deal copy: %+v", d)
	}
	rows, err := s.ProductRows(ctx, "deal", cp)
	must(err)
	if len(rows) != 2 || rows[0].Name != "Стол" || rows[1].Quantity != 4 {
		t.Fatalf("rows: %+v", rows)
	}
	vals, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], cp)
	must(err)
	if len(vals) != 1 || vals[0].Value != "Д-1" {
		t.Fatalf("own fields: %+v", vals)
	}
	ev, err := s.Timeline(ctx, "deal", cp)
	must(err)
	for _, e := range ev {
		if e.Kind == "comment" || e.Kind == "changed" {
			t.Fatalf("the copy's timeline: %+v", ev)
		}
	}

	// a given responsible; a company keeps its phones and e-mails
	cc, err := s.Copy(ctx, anna, "company", co, anna)
	must(err)
	c, err := s.Company(ctx, cc)
	must(err)
	if c.Title != "Ромашка" || c.Responsible.ID != anna || len(c.Comms) != 2 || c.Phone == "" || c.Email != "info@romashka.ru" {
		t.Fatalf("company copy: %+v", c)
	}
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Name: "Пётр", Phone: "+7 900 222-33-44"})
	must(err)
	cl, err := s.Copy(ctx, boris, "lead", lead, boris)
	must(err)
	l, err := s.Lead(ctx, cl)
	must(err)
	if l.Title != "Заявка" || l.Name != "Пётр" || l.Phone == "" || l.Responsible.ID != boris || l.Converted() {
		t.Fatalf("lead copy: %+v", l)
	}
	cn, err := s.Copy(ctx, anna, "contact", ct, "")
	must(err)
	if c, err := s.Contact(ctx, cn); err != nil || c.Name != "Иван" || c.Company.ID != co {
		t.Fatalf("contact copy: %+v %v", c, err)
	}
	if _, err := s.Copy(ctx, anna, "quote", deal, ""); err != ErrNotFound {
		t.Fatalf("unknown kind: %v", err)
	}
}
