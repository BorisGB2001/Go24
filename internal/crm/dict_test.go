package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCRMDicts: the lists have their built-in items in order; an item is added, renamed, moved and deleted (the cards
// that had it get none); cards keep their source and types, a built-in one may be given by its code, an item of
// another list is refused; a converted lead hands its source to the contact and the deal; the import finds items by
// name and keeps an unknown source of a lead as its text; the filter and the analytics go by the item.
func TestCRMDicts(t *testing.T) {
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

	dicts, err := s.Dicts(ctx)
	must(err)
	if len(dicts["source"]) != 13 || dicts["source"][0].Code != "CALL" || len(dicts["industry"]) != 11 || dicts["deal_type"][0].Code != "SALE" {
		t.Fatalf("built-in %+v", dicts)
	}
	tr := func(k string) string { return "«" + k + "»" }
	if dicts["source"][1].Text(tr) != "«crm.dict.EMAIL»" {
		t.Fatalf("text %q", dicts["source"][1].Text(tr))
	}
	if _, err := s.AddDictItem(ctx, "colour", "x"); err == nil {
		t.Fatal("unknown list")
	}
	if _, err := s.AddDictItem(ctx, "source", "  "); err == nil {
		t.Fatal("empty name")
	}
	fair, err := s.AddDictItem(ctx, "source", "Ярмарка")
	must(err)
	must(s.MoveDictItem(ctx, fair, true))
	call := dicts["source"][0].ID
	must(s.MoveDictItem(ctx, call, true)) // the first stays first
	must(s.RenameDictItem(ctx, call, "Входящий звонок"))
	dicts, err = s.Dicts(ctx)
	must(err)
	src := dicts["source"]
	if src[0].Name != "Входящий звонок" || src[0].Code != "CALL" || src[len(src)-2].ID != fair || src[len(src)-1].Code != "OTHER" {
		t.Fatalf("order %+v", src)
	}
	if src[0].Text(tr) != "Входящий звонок" {
		t.Fatal("own name")
	}

	// cards
	it := dicts["industry"][0].ID
	co, err := s.SaveCompany(ctx, anna, Company{Title: "ООО", Responsible: Ref{ID: anna}, CompanyType: Dict{Code: "PARTNER"}, Industry: Dict{ID: it}})
	must(err)
	c, err := s.Company(ctx, co)
	must(err)
	if c.CompanyType.Code != "PARTNER" || c.CompanyType.ID == "" || c.Industry.ID != it || c.Industry.Code != "IT" {
		t.Fatalf("company %+v %+v", c.CompanyType, c.Industry)
	}
	if _, err := s.SaveCompany(ctx, anna, Company{Title: "ООО", Responsible: Ref{ID: anna}, Industry: Dict{ID: fair}}); err == nil {
		t.Fatal("a source as industry")
	}
	if _, err := s.SaveContact(ctx, anna, Contact{Name: "Иван", Responsible: Ref{ID: anna}, ContactType: Dict{ID: "00000000-0000-0000-0000-000000000000"}}); err == nil {
		t.Fatal("unknown item")
	}
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Name: "Пётр", Responsible: Ref{ID: anna}, Origin: Dict{ID: fair}, Source: "стенд 5"})
	must(err)
	l, err := s.Lead(ctx, lead)
	must(err)
	if l.Origin.ID != fair || l.Origin.Name != "Ярмарка" || l.Source != "стенд 5" {
		t.Fatalf("lead %+v %q", l.Origin, l.Source)
	}
	deal, err := s.ConvertLead(ctx, anna, lead)
	must(err)
	d, err := s.Deal(ctx, deal)
	must(err)
	if d.Origin.ID != fair {
		t.Fatalf("deal origin %+v", d.Origin)
	}
	x, err := s.Contact(ctx, d.Contact.ID)
	must(err)
	if x.Origin.ID != fair {
		t.Fatalf("contact origin %+v", x.Origin)
	}
	d.DealType = Dict{Code: "SERVICE"}
	_, err = s.SaveDeal(ctx, anna, d)
	must(err)
	if d, err = s.Deal(ctx, deal); err != nil || d.DealType.Code != "SERVICE" || d.Origin.ID != fair {
		t.Fatalf("deal %+v %v", d.DealType, err)
	}
	_, err = s.SaveLead(ctx, anna, Lead{Title: "Звонок", Responsible: Ref{ID: anna}, Origin: Dict{Code: "CALL"}})
	must(err)

	// the filter and the analytics
	leads, err := s.LeadList(ctx, anna, Filter{Source: fair})
	must(err)
	if len(leads) != 1 || leads[0].ID != lead {
		t.Fatalf("filter %+v", leads)
	}
	res, err := s.Import(ctx, anna, "lead", [][]string{{"Название", "Источник"}, {"Альфа", "входящий звонок"}, {"Бета", "Газета"}, {"Гамма", "«crm.dict.WEB»"}}, false, tr)
	must(err)
	if res.Created != 3 {
		t.Fatalf("import %+v", res)
	}
	leads, err = s.LeadList(ctx, anna, Filter{Q: "Бета"})
	must(err)
	if len(leads) != 1 || leads[0].Origin.ID != "" || leads[0].Source != "Газета" {
		t.Fatalf("unknown source %+v", leads)
	}
	leads, err = s.LeadList(ctx, anna, Filter{Source: call})
	must(err)
	if len(leads) != 2 {
		t.Fatalf("by call %+v", leads)
	}
	leads, err = s.LeadList(ctx, anna, Filter{Q: "Гамма"})
	must(err)
	if len(leads) != 1 || leads[0].Origin.Code != "WEB" {
		t.Fatalf("by locale name %+v", leads)
	}

	// a deleted item leaves the cards without it
	dicts, err = s.Dicts(ctx)
	must(err)
	for _, it := range dicts["source"] {
		if it.ID == fair && it.Used != 3 {
			t.Fatalf("used %d", it.Used)
		}
	}
	must(s.DeleteDictItem(ctx, fair))
	if err := s.DeleteDictItem(ctx, fair); err != ErrNotFound {
		t.Fatalf("deleted twice: %v", err)
	}
	l, err = s.Lead(ctx, lead)
	must(err)
	if l.Origin.ID != "" || l.Source != "стенд 5" {
		t.Fatalf("after delete %+v", l.Origin)
	}
	if id, err := s.DictCode(ctx, "source", "CALL"); err != nil || id != call {
		t.Fatalf("code %q %v", id, err)
	}
}
