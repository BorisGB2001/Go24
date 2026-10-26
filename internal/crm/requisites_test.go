package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRequisites: a company's requisites are checked by their preset, the first is the default, another is made the
// default; the deal's buyer is the company's default one (else the contact's); a company with the same ИНН is a
// duplicate and is found by its ИНН; merging moves the requisites, deleting the default makes the next one default.
func TestRequisites(t *testing.T) {
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
	romashka, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	ivan, err := s.SaveContact(ctx, anna, Contact{Name: "Иван"})
	must(err)

	for _, bad := range []Requisite{
		{Preset: "org", Name: ""},
		{Preset: "org", Name: "ООО", INN: "123"},
		{Preset: "ip", Name: "ИП", INN: "7701234567"},
		{Preset: "ip", Name: "ИП", INN: "770123456789", KPP: "770101001"},
		{Preset: "org", Name: "ООО", BIK: "04452"},
		{Preset: "org", Name: "ООО", Account: "4070281"},
		{Preset: "nobody", Name: "ООО"},
	} {
		bad.Entity, bad.EntityID = "company", romashka
		if _, err := s.SaveRequisite(ctx, bad); err == nil {
			t.Fatalf("saved %+v", bad)
		}
	}
	if _, err := s.Buyer(ctx, romashka, ivan); err != nil {
		t.Fatal(err)
	}
	if b, _ := s.Buyer(ctx, romashka, ivan); b != nil {
		t.Fatalf("a buyer without requisites: %+v", b)
	}
	person, err := s.SaveRequisite(ctx, Requisite{Entity: "contact", EntityID: ivan, Preset: "person", Name: "Иванов Иван"})
	must(err)
	if b, _ := s.Buyer(ctx, romashka, ivan); b == nil || b.ID != person {
		t.Fatalf("the contact's requisite as the buyer: %+v", b)
	}
	first, err := s.SaveRequisite(ctx, Requisite{Entity: "company", EntityID: romashka, Preset: "org", Name: "ООО «Ромашка»",
		INN: "7701 234 567", KPP: "770101001", BIK: "044525225", Account: "40702810000000000001"})
	must(err)
	second, err := s.SaveRequisite(ctx, Requisite{Entity: "company", EntityID: romashka, Preset: "org", Name: "ООО «Ромашка-2»"})
	must(err)
	list, err := s.Requisites(ctx, "company", romashka)
	must(err)
	if len(list) != 2 || list[0].ID != first || !list[0].Default || list[1].Default || list[0].INN != "7701234567" {
		t.Fatalf("requisites: %+v", list)
	}
	if b, _ := s.Buyer(ctx, romashka, ivan); b == nil || b.ID != first {
		t.Fatalf("the company's requisite as the buyer: %+v", b)
	}
	_, err = s.SaveRequisite(ctx, Requisite{ID: second, Entity: "company", EntityID: romashka, Preset: "org", Name: "ООО «Ромашка-2»", Default: true})
	must(err)
	if list, _ = s.Requisites(ctx, "company", romashka); list[0].ID != second || list[1].Default {
		t.Fatalf("the default changed: %+v", list)
	}
	if _, err := s.SaveRequisite(ctx, Requisite{ID: second, Entity: "contact", EntityID: ivan, Preset: "person", Name: "X"}); err == nil {
		t.Fatal("changed a requisite as another card's")
	}

	twin, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка (филиал)"})
	must(err)
	_, err = s.SaveRequisite(ctx, Requisite{Entity: "company", EntityID: twin, Preset: "org", Name: "ООО «Ромашка»", INN: "7701234567"})
	must(err)
	dups, err := s.Duplicates(ctx, "company", twin, "Ромашка (филиал)", "", "")
	must(err)
	if len(dups) != 1 || dups[0].ID != romashka || dups[0].By != "inn" {
		t.Fatalf("duplicates by INN: %+v", dups)
	}
	found, err := s.Companies(ctx, anna, "77012345")
	must(err)
	if len(found) != 2 {
		t.Fatalf("found by INN: %+v", found)
	}

	must(s.Merge(ctx, anna, "company", romashka, twin))
	if list, _ = s.Requisites(ctx, "company", romashka); len(list) != 3 || list[0].ID != second || list[1].Default || list[2].Default {
		t.Fatalf("merged requisites: %+v", list)
	}
	must(s.DeleteRequisite(ctx, second))
	if list, _ = s.Requisites(ctx, "company", romashka); len(list) != 2 || list[0].ID != first || !list[0].Default {
		t.Fatalf("after deleting the default: %+v", list)
	}
	must(s.DeleteCompany(ctx, anna, romashka))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_requisites WHERE entity = 'company'`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d requisites of a deleted company", n)
	}
}
