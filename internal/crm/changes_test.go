package crm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMChanges: an edit of a deal, a lead, a contact and a company leaves one «changed» event in the card's timeline
// with each changed field, old and new as they read (a built-in item by its code, a date as date:); saving unchanged
// leaves nothing; another responsible from the list and the card's own fields are logged too.
func TestCRMChanges(t *testing.T) {
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
	changes := func(entity, id string) [][]Change {
		t.Helper()
		ev, err := s.Timeline(ctx, entity, id)
		must(err)
		var out [][]Change
		for _, e := range ev {
			if e.Kind == "changed" {
				out = append(out, Changes(e.Body))
			}
		}
		return out
	}

	pipes, err := s.Pipelines(ctx)
	must(err)
	co, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	id, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 100, Currency: "RUB"})
	must(err)
	if c := changes("deal", id); len(c) != 0 {
		t.Fatalf("created %+v", c)
	}
	d, err := s.Deal(ctx, id)
	must(err)
	_, err = s.SaveDeal(ctx, anna, d)
	must(err)
	if c := changes("deal", id); len(c) != 0 {
		t.Fatalf("unchanged %+v", c)
	}
	close := time.Date(2026, 11, 30, 0, 0, 0, 0, time.Local)
	d.Title, d.Amount, d.Company, d.CloseDate, d.DealType = "Поставка 2", 250, Ref{ID: co}, &close, Dict{Code: "SERVICE"}
	d.Responsible = Ref{ID: boris}
	_, err = s.SaveDeal(ctx, anna, d)
	must(err)
	c := changes("deal", id)
	want := []Change{{"title", "Поставка", "Поставка 2"}, {"amount", Amount(100), Amount(250)}, {"deal_type", "", "code:SERVICE"},
		{"company", "", "Ромашка"}, {"responsible", "Анна", "Борис"}, {"close_date", "", "date:2026-11-30"}}
	if len(c) != 1 || len(c[0]) != len(want) {
		t.Fatalf("deal %+v", c)
	}
	for i := range want {
		if c[0][i] != want[i] {
			t.Fatalf("deal change %d: %+v, want %+v", i, c[0][i], want[i])
		}
	}

	must(s.SetResponsible(ctx, boris, "deal", id, anna))
	if c := changes("deal", id); len(c) != 2 || len(c[0]) != 1 || c[0][0] != (Change{"responsible", "Борис", "Анна"}) {
		t.Fatalf("responsible %+v", c)
	}

	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Phone: "1"})
	must(err)
	l, err := s.Lead(ctx, lead)
	must(err)
	l.Phone, l.Comment = "2", strings.Repeat("я", 400)
	_, err = s.SaveLead(ctx, anna, l)
	must(err)
	if c := changes("lead", lead); len(c) != 1 || len(c[0]) != 2 || c[0][0] != (Change{"phone", "1", "2"}) ||
		len([]rune(c[0][1].New)) != changeClip+1 {
		t.Fatalf("lead %+v", c)
	}

	x, err := s.SaveContact(ctx, anna, Contact{Name: "Иван"})
	must(err)
	xc, err := s.Contact(ctx, x)
	must(err)
	xc.Position, xc.Company = "Директор", Ref{ID: co}
	_, err = s.SaveContact(ctx, anna, xc)
	must(err)
	if c := changes("contact", x); len(c) != 1 || len(c[0]) != 2 || c[0][1] != (Change{"company", "", "Ромашка"}) {
		t.Fatalf("contact %+v", c)
	}

	cc, err := s.Company(ctx, co)
	must(err)
	cc.Website = "romashka.ru"
	_, err = s.SaveCompany(ctx, anna, cc)
	must(err)
	if c := changes("company", co); len(c) != 1 || c[0][0] != (Change{"website", "", "romashka.ru"}) {
		t.Fatalf("company %+v", c)
	}

	ts := tasks.NewStore(pool)
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Договор", Kind: "string"}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Подписан", Kind: "date"}))
	fields, err := ts.FieldsOf(ctx, tasks.CRMFieldSets["deal"])
	must(err)
	values := map[string]string{}
	for _, f := range fields {
		values[f.ID] = map[string]string{"Договор": "Д-1", "Подписан": "2026-10-22"}[f.Name]
	}
	must(s.SetFields(ctx, anna, "deal", id, values))
	must(s.SetFields(ctx, anna, "deal", id, values))
	if c := changes("deal", id); len(c) != 3 || len(c[0]) != 2 || c[0][0] != (Change{"uf:Договор", "", "Д-1"}) ||
		c[0][1] != (Change{"uf:Подписан", "", "date:2026-10-22"}) {
		t.Fatalf("own fields %+v", c)
	}
}
