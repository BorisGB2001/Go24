package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestDealContacts: a deal has several contacts, the first is the main one; the deal is among the deals of each of
// them; a deal saved with its main contact only keeps that one; merging a contact moves its deals, deleting the main
// one makes the next the main one.
func TestDealContacts(t *testing.T) {
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
	pipes, err := s.Pipelines(ctx)
	must(err)
	contact := func(name string) string {
		id, err := s.SaveContact(ctx, anna, Contact{Name: name})
		must(err)
		return id
	}
	ivan, petr, olga, ivan2 := contact("Иван"), contact("Пётр"), contact("Ольга"), contact("Иван 2")
	names := func(id string) (main string, all []string) {
		t.Helper()
		d, err := s.Deal(ctx, id)
		must(err)
		for _, c := range d.Contacts {
			all = append(all, c.Name)
		}
		return d.Contact.Name, all
	}
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID,
		Contacts: []Ref{{ID: petr}, {ID: ""}, {ID: ivan}, {ID: petr}}})
	must(err)
	if m, all := names(deal); m != "Пётр" || len(all) != 2 || all[0] != "Пётр" || all[1] != "Иван" {
		t.Fatalf("main %q, contacts %v", m, all)
	}
	for _, c := range []string{petr, ivan} {
		if list, err := s.Deals(ctx, anna, "", "", "", c); err != nil || len(list) != 1 || list[0].ID != deal {
			t.Fatalf("deals of %s: %v, %v", c, list, err)
		}
	}
	if list, _ := s.Deals(ctx, anna, "", "", "", olga); len(list) != 0 {
		t.Fatalf("deals of Ольга: %v", list)
	}

	// reordered with one more: Ольга is the main one now
	_, err = s.SaveDeal(ctx, anna, Deal{ID: deal, Title: "Поставка", Contacts: []Ref{{ID: olga}, {ID: ivan}, {ID: petr}}})
	must(err)
	if m, all := names(deal); m != "Ольга" || len(all) != 3 || all[1] != "Иван" {
		t.Fatalf("reordered: main %q, contacts %v", m, all)
	}
	// the main contact alone (the REST API, converting a lead): the deal has just it
	other, err := s.SaveDeal(ctx, anna, Deal{Title: "Сервис", PipelineID: pipes[0].ID, Contact: Ref{ID: ivan2}})
	must(err)
	if m, all := names(other); m != "Иван 2" || len(all) != 1 {
		t.Fatalf("one contact: main %q, contacts %v", m, all)
	}

	// Иван 2 merged into Иван: both deals are Иван's
	must(s.Merge(ctx, anna, "contact", ivan, ivan2))
	if list, _ := s.Deals(ctx, anna, "", "", "", ivan); len(list) != 2 {
		t.Fatalf("deals of Иван after merging: %v", list)
	}
	if m, all := names(other); m != "Иван" || len(all) != 1 {
		t.Fatalf("merged: main %q, contacts %v", m, all)
	}
	// the main contact deleted: the next one is the main one
	must(s.DeleteContact(ctx, anna, olga))
	if m, all := names(deal); m != "Иван" || len(all) != 2 || all[1] != "Пётр" {
		t.Fatalf("after deleting: main %q, contacts %v", m, all)
	}
	// no contacts at all
	_, err = s.SaveDeal(ctx, anna, Deal{ID: deal, Title: "Поставка", Contacts: []Ref{}})
	must(err)
	if m, all := names(deal); m != "" || len(all) != 0 {
		t.Fatalf("cleared: main %q, contacts %v", m, all)
	}
}
