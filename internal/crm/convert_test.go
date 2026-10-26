package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestConvertTo: the dialog offers a deal, a contact and a company, and finds the existing clients by phone and
// title; a lead converts into any of the three — a deal in the chosen pipeline, an existing contact, a new company
// with the lead's like-named own fields; the lead keeps all three and the timelines link them.
func TestConvertTo(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	ts := tasks.NewStore(pool)
	_, err = s.AddPipeline(ctx, "Тендеры")
	must(err)
	pipes, err := s.Pipelines(ctx)
	must(err)
	tenders := pipes[1]
	known, err := s.SaveContact(ctx, anna, Contact{Name: "Иван Петров", Phone: "+7 (900) 123-45-67", Responsible: Ref{ID: anna}})
	must(err)
	for _, set := range []tasks.FieldSet{tasks.CRMFieldSets["lead"], tasks.CRMFieldSets["company"], tasks.CRMFieldSets["deal"]} {
		must(ts.AddFieldTo(ctx, set, tasks.Field{Name: "Бюджет", Kind: "number"}))
	}
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["lead"], tasks.Field{Name: "Регион", Kind: "string"}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["company"], tasks.Field{Name: "регион", Kind: "string"}))

	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Name: "Иван", Phone: "89001234567", CompanyTitle: "Ромашка",
		Amount: 500, Responsible: Ref{ID: anna}})
	must(err)
	vals := map[string]string{}
	lf, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["lead"], lead)
	must(err)
	for _, f := range lf {
		vals[f.ID] = map[string]string{"Бюджет": "1000", "Регион": "Север"}[f.Name]
	}
	must(s.SetFields(ctx, anna, "lead", lead, vals))

	l, err := s.Lead(ctx, lead)
	must(err)
	def, err := s.ConvertDefaults(ctx, l)
	must(err)
	if !def.Deal || !def.Contact || !def.Company || def.ContactID != known || def.CompanyID != "" {
		t.Fatalf("defaults: %+v (known contact %s)", def, known)
	}
	var ve *i18n.Error
	if _, err := s.ConvertLeadTo(ctx, anna, lead, ConvertTo{}); !errors.As(err, &ve) || ve.Key != "err.crm_convert_nothing" {
		t.Fatalf("converting into nothing: %v", err)
	}

	// a contact only: the existing one; then a deal in «Тендеры» and a new company
	got, err := s.ConvertLeadTo(ctx, anna, lead, ConvertTo{Contact: true, ContactID: known})
	must(err)
	if got.ContactID != known || got.DealID != "" || got.CompanyID != "" {
		t.Fatalf("contact only: %+v", got)
	}
	l, err = s.Lead(ctx, lead)
	must(err)
	if l.ContactID != known || l.Stage.Semantic != "success" {
		t.Fatalf("lead after contact only: %+v", l)
	}
	def, err = s.ConvertDefaults(ctx, l)
	must(err)
	if def.ContactID != known {
		t.Fatalf("defaults after: %+v", def)
	}
	got, err = s.ConvertLeadTo(ctx, anna, lead, ConvertTo{Deal: true, PipelineID: tenders.ID, Contact: true, ContactID: known, Company: true})
	must(err)
	d, err := s.Deal(ctx, got.DealID)
	must(err)
	if d.PipelineID != tenders.ID || d.Contact.ID != known || d.Company.ID != got.CompanyID || d.Amount != 500 {
		t.Fatalf("deal: %+v", d)
	}
	c, err := s.Company(ctx, got.CompanyID)
	must(err)
	if c.Title != "Ромашка" {
		t.Fatalf("company: %+v", c)
	}
	cf, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["company"], c.ID)
	must(err)
	for _, f := range cf {
		if want := map[string]string{"Бюджет": "1000", "регион": "Север"}[f.Name]; f.Value != want {
			t.Fatalf("company field %s = %q, want %q", f.Name, f.Value, want)
		}
	}
	df, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], d.ID)
	must(err)
	if len(df) != 1 || df[0].Value != "1000" {
		t.Fatalf("deal fields: %+v", df)
	}
	// the deal is not made twice
	again, err := s.ConvertLeadTo(ctx, anna, lead, ConvertTo{Deal: true})
	must(err)
	if again.DealID != d.ID {
		t.Fatalf("a second deal: %+v", again)
	}
	tl, err := s.Timeline(ctx, "lead", lead)
	must(err)
	conv := map[string]int{}
	for _, e := range tl {
		if e.Kind == "converted" {
			conv[e.Body]++
		}
	}
	if conv["contact:"+known] != 1 || conv["deal:"+d.ID] != 1 || conv["company:"+c.ID] != 1 { // the contact chosen twice, told once
		t.Fatalf("lead timeline: %v", conv)
	}
	tl, err = s.Timeline(ctx, "company", c.ID)
	must(err)
	if !func() bool {
		for _, e := range tl {
			if e.Kind == "converted" && e.Body == "lead:"+lead {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("company timeline: %+v", tl)
	}
	// a merged contact takes the lead along
	other, err := s.SaveContact(ctx, anna, Contact{Name: "Иван П.", Responsible: Ref{ID: anna}})
	must(err)
	must(s.Merge(ctx, anna, "contact", other, known))
	if l, _ = s.Lead(ctx, lead); l.ContactID != other {
		t.Fatalf("after merge the lead's contact is %s, want %s", l.ContactID, other)
	}
}
