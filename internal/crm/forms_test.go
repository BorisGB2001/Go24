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

// TestForms: a «CRM-форма» keeps the fields of its card in their order (unknown and repeated ones left out); the
// answers are checked — required, e-mail, the custom field's kind, the consent; a filling makes a lead (source
// «CRM-форма», the custom field, «Должность» in the comment) and moves it by the trigger, or a contact with its
// company and a deal — the same contact for the same phone; the responsible is told; the results keep the answers and
// the cards; a switched off form has no public page; deleting the form keeps the cards.
func TestForms(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'boris', 'boris@t') RETURNING id`).Scan(&boris))
	s := NewStore(pool)
	ts := tasks.NewStore(pool)
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["lead"], tasks.Field{Name: "Гостей", Kind: "number"}))
	must(ts.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Город", Kind: "string"}))
	var guests, city string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_lead_fields WHERE name = 'Гостей'`).Scan(&guests))
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_deal_fields WHERE name = 'Город'`).Scan(&city))
	tr := func(key string) string { return key }

	if _, err := s.SaveForm(ctx, anna, Form{Name: "Пусто", Scheme: "lead", Fields: []FormField{{Key: "password"}}}); err == nil {
		t.Fatal("a form without fields")
	}
	if _, err := s.SaveForm(ctx, anna, Form{Name: "Адрес", Scheme: "lead", Fields: []FormField{{Key: "name"}}, Redirect: "javascript:alert(1)"}); err == nil {
		t.Fatal("a bad address to go to")
	}
	id, err := s.SaveForm(ctx, anna, Form{Name: "Заявка", Scheme: "lead", Active: true, Responsible: Ref{ID: boris}, Consent: "Согласен",
		Fields: []FormField{{Key: "name", Required: true}, {Key: "email", Label: "Почта"}, {Key: "position"}, {Key: "f:" + guests},
			{Key: "f:" + city}, {Key: "name"}, {Key: "password"}}})
	must(err)
	f, err := s.Form(ctx, id)
	must(err)
	must(s.FormFields(ctx, &f))
	f.Labeled(tr)
	if len(f.Fields) != 4 || f.Fields[0].Label != "crm.form_field.name" || f.Fields[1].Label != "Почта" || f.Fields[3].Kind != "number" ||
		f.Fields[3].Label != "Гостей" || f.Token == "" {
		t.Fatalf("form = %+v", f)
	}
	if pub, err := s.FormByToken(ctx, f.Token); err != nil || pub.ID != id {
		t.Fatalf("by token: %v", err)
	}
	answer := func(m map[string]string, consent bool) ([]FormAnswer, error) {
		return f.Answer(func(k string) string { return m[k] }, consent)
	}
	var ie *i18n.Error
	if _, err := answer(map[string]string{"email": "a@b"}, true); !errors.As(err, &ie) || ie.Key != "err.crm_form_field_required" {
		t.Fatalf("required: %v", err)
	}
	if _, err := answer(map[string]string{"name": "Иван", "email": "не адрес"}, true); !errors.Is(err, ErrFormEmail) {
		t.Fatalf("e-mail: %v", err)
	}
	if _, err := answer(map[string]string{"name": "Иван", "f:" + guests: "много"}, true); err == nil {
		t.Fatal("a number field took a word")
	}
	if _, err := answer(map[string]string{"name": "Иван"}, false); !errors.Is(err, ErrFormConsent) {
		t.Fatalf("consent: %v", err)
	}

	// a lead, moved by the trigger «form»
	stages, err := s.LeadStages(ctx)
	must(err)
	must(s.AddTrigger(ctx, anna, stages[1].ID, "form"))
	ans, err := answer(map[string]string{"name": "Иван", "email": "ivan@client.ru", "position": "Директор", "f:" + guests: "12,5"}, true)
	must(err)
	res, err := s.SubmitForm(ctx, f, ans, FormFilling{Title: "Заявка — Иван", Comment: "со страницы", Source: "CRM-форма «Заявка»", IP: "10.0.0.1", Page: "https://site.ru/"})
	must(err)
	l, err := s.Lead(ctx, res.LeadID)
	must(err)
	if l.Title != "Заявка — Иван" || l.Name != "Иван" || l.Email != "ivan@client.ru" || l.Origin.Code != "WEBFORM" || l.Responsible.ID != boris ||
		l.Stage.ID != stages[1].ID || l.Comment != "crm.form_field.position: Директор\n\nсо страницы" {
		t.Fatalf("lead = %+v", l)
	}
	vals, err := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["lead"], res.LeadID)
	must(err)
	if len(vals) != 1 || vals[0].Value != "12.5" {
		t.Fatalf("custom values = %+v", vals)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_form' AND url = $2`, boris, "/crm/leads/"+res.LeadID).Scan(&notes))
	if notes != 1 {
		t.Fatal("the responsible is not told")
	}

	// a contact with its company and a deal; the same phone — the same contact
	pipes, err := s.Pipelines(ctx)
	must(err)
	did, err := s.SaveForm(ctx, anna, Form{Name: "Сделка", Scheme: "deal", Active: true, PipelineID: pipes[0].ID,
		Fields: []FormField{{Key: "name"}, {Key: "last_name"}, {Key: "phone"}, {Key: "company"}, {Key: "f:" + city}, {Key: "f:" + guests}}})
	must(err)
	df, err := s.Form(ctx, did)
	must(err)
	must(s.FormFields(ctx, &df))
	df.Labeled(tr)
	if len(df.Fields) != 5 {
		t.Fatalf("deal form fields = %+v", df.Fields)
	}
	submit := func(m map[string]string) FormResult {
		t.Helper()
		ans, err := df.Answer(func(k string) string { return m[k] }, false)
		must(err)
		r, err := s.SubmitForm(ctx, df, ans, FormFilling{Title: "Сделка с формы"})
		must(err)
		return r
	}
	r1 := submit(map[string]string{"name": "Пётр", "last_name": "Петров", "phone": "+7 900 111-22-33", "company": "Ромашка", "f:" + city: "Тверь"})
	d, err := s.Deal(ctx, r1.DealID)
	must(err)
	c, err := s.Contact(ctx, r1.ContactID)
	must(err)
	if c.Name != "Пётр Петров" || c.Company.Name != "Ромашка" || d.Contact.ID != c.ID || d.Company.ID != c.Company.ID ||
		d.Responsible.ID != anna || d.Origin.Code != "WEBFORM" || d.PipelineID != pipes[0].ID {
		t.Fatalf("deal = %+v, contact = %+v", d, c)
	}
	if v, _ := ts.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], r1.DealID); len(v) != 1 || v[0].Value != "Тверь" {
		t.Fatalf("deal values = %+v", v)
	}
	r2 := submit(map[string]string{"name": "Пётр", "phone": "89001112233"})
	if r2.ContactID != r1.ContactID || r2.DealID == r1.DealID {
		t.Fatalf("the second filling: %+v", r2)
	}

	// the results; switched off; deleted
	list, err := s.FormResults(ctx, did, 50)
	must(err)
	if len(list) != 2 || list[0].ID != r2.ID || list[1].ContactName != "Пётр Петров" || list[1].Answers[0].Label != "crm.form_field.name" {
		t.Fatalf("results = %+v", list)
	}
	if forms, _ := s.Forms(ctx); len(forms) != 2 || forms[0].Name != "Заявка" || forms[1].Results != 2 {
		t.Fatalf("forms = %+v", forms)
	}
	df.Active = false
	_, err = s.SaveForm(ctx, anna, df)
	must(err)
	if _, err := s.FormByToken(ctx, df.Token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a switched off form: %v", err)
	}
	must(s.DeleteForm(ctx, did))
	if _, err := s.Deal(ctx, r1.DealID); err != nil {
		t.Fatalf("the deal went with the form: %v", err)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_form_results WHERE form_id::text = $1`, did).Scan(&left))
	if left != 0 {
		t.Fatal("the results stay")
	}
}
