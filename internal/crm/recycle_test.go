package crm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestRecycle: a deleted deal goes to the bin and comes back with its contacts, observers, products and deeds, and the
// lead converted into it points at it again; a deleted company comes back with its requisites and its contacts; a
// contact whose company is gone comes back without one; a deal whose stage is gone cannot; the bin is the deleter's
// (all of it — an administrator's); deleting for good and the 30 days forget the card with its timeline.
func TestRecycle(t *testing.T) {
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
	admin := tasks.WithAdmin(ctx)
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)

	company, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	_, err = s.SaveRequisite(ctx, Requisite{Entity: "company", EntityID: company, Preset: "org", Name: "ООО Ромашка", INN: "7700000001"})
	must(err)
	ivan, err := s.SaveContact(ctx, anna, Contact{Name: "Иван", Company: Ref{ID: company}})
	must(err)
	petr, err := s.SaveContact(ctx, anna, Contact{Name: "Пётр"})
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Company: Ref{ID: company},
		Contacts: []Ref{{ID: ivan}, {ID: petr}}})
	must(err)
	must(s.SetObservers(ctx, anna, "deal", deal, []string{boris}))
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{Name: "Ромашки", Price: 100, Quantity: 3}})
	must(err)
	_, err = s.AddActivity(ctx, anna, Activity{Entity: "deal", EntityID: deal, Kind: "call", Subject: "Позвонить",
		Deadline: time.Now().Add(time.Hour), Responsible: Ref{ID: anna}})
	must(err)
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Name: "Иван"})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_leads SET deal_id = $1 WHERE id = $2`, deal, lead)
	must(err)

	must(s.DeleteDeal(ctx, anna, deal))
	if _, err := s.Deal(ctx, deal); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted deal is there: %v", err)
	}
	bin, err := s.Recycle(ctx, anna, "", "")
	must(err)
	if len(bin) != 1 || bin[0].Entity != "deal" || bin[0].Title != "Поставка" || bin[0].DeletedBy.ID != anna {
		t.Fatalf("bin = %+v", bin)
	}
	if other, _ := s.Recycle(ctx, boris, "", ""); len(other) != 0 {
		t.Fatalf("boris sees anna's bin: %+v", other)
	}
	if all, _ := s.Recycle(admin, boris, "", ""); len(all) != 1 {
		t.Fatalf("the administrator's bin = %+v", all)
	}
	if _, _, err := s.Restore(ctx, boris, bin[0].ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("boris restores anna's card: %v", err)
	}
	entity, id, err := s.Restore(ctx, anna, bin[0].ID)
	must(err)
	if entity != "deal" || id != deal {
		t.Fatalf("restored %s %s", entity, id)
	}
	d, err := s.Deal(ctx, deal)
	must(err)
	if d.Title != "Поставка" || len(d.Contacts) != 2 || d.Contact.ID != ivan || d.Company.ID != company || len(d.Observers) != 1 {
		t.Fatalf("restored deal = %+v", d)
	}
	if rows, _ := s.ProductRows(ctx, "deal", deal); len(rows) != 1 || rows[0].Quantity != 3 {
		t.Fatalf("products = %+v", rows)
	}
	if acts, _ := s.Activities(ctx, "deal", deal); len(acts) != 1 {
		t.Fatalf("deeds = %+v", acts)
	}
	var leadDeal string
	must(pool.QueryRow(ctx, `SELECT coalesce(deal_id::text, '') FROM crm_leads WHERE id = $1`, lead).Scan(&leadDeal))
	if leadDeal != deal {
		t.Fatalf("the lead's deal = %q", leadDeal)
	}
	tl, err := s.Timeline(ctx, "deal", deal)
	must(err)
	if len(tl) == 0 || tl[0].Kind != "restored" {
		t.Fatalf("timeline = %+v", tl)
	}
	if bin, _ := s.Recycle(ctx, anna, "", ""); len(bin) != 0 {
		t.Fatalf("the bin after restoring = %+v", bin)
	}

	// a company: its requisites and the links of its contact and deal come back
	must(s.DeleteCompany(ctx, anna, company))
	if c, _ := s.Contact(ctx, ivan); c.Company.ID != "" {
		t.Fatalf("the contact keeps the deleted company")
	}
	bin, _ = s.Recycle(ctx, anna, "company", "")
	if len(bin) != 1 {
		t.Fatalf("bin = %+v", bin)
	}
	_, _, err = s.Restore(ctx, anna, bin[0].ID)
	must(err)
	if c, _ := s.Contact(ctx, ivan); c.Company.ID != company {
		t.Fatalf("the contact's company = %q", c.Company.ID)
	}
	if d, _ := s.Deal(ctx, deal); d.Company.ID != company {
		t.Fatalf("the deal's company = %q", d.Company.ID)
	}
	if rq, _ := s.Requisites(ctx, "company", company); len(rq) != 1 || rq[0].INN != "7700000001" {
		t.Fatalf("requisites = %+v", rq)
	}

	// a contact whose company is deleted meanwhile comes back without it
	must(s.DeleteContact(ctx, anna, ivan))
	must(s.DeleteCompany(ctx, anna, company))
	bin, _ = s.Recycle(ctx, anna, "contact", "Ив")
	if len(bin) != 1 {
		t.Fatalf("bin = %+v", bin)
	}
	_, _, err = s.Restore(ctx, anna, bin[0].ID)
	must(err)
	c, err := s.Contact(ctx, ivan)
	must(err)
	if c.Company.ID != "" {
		t.Fatalf("the contact's company = %q", c.Company.ID)
	}
	if d, _ := s.Deal(ctx, deal); len(d.Contacts) != 2 {
		t.Fatalf("the deal's contacts after restoring = %+v", d.Contacts)
	}

	// a deal whose stage is gone cannot come back
	other, err := s.SaveDeal(ctx, anna, Deal{Title: "Вторая", PipelineID: pipes[0].ID})
	must(err)
	must(s.DeleteDeal(ctx, anna, other))
	bin, _ = s.Recycle(ctx, anna, "deal", "")
	_, err = pool.Exec(ctx, `UPDATE crm_recycle SET data = jsonb_set(data, '{tables,0,rows,0,stage_id}', to_jsonb(gen_random_uuid()::text)) WHERE id = $1`, bin[0].ID)
	must(err)
	if _, _, err := s.Restore(ctx, anna, bin[0].ID); !errors.Is(err, ErrRecycleGone) {
		t.Fatalf("a deal without its stage: %v", err)
	}

	// for good: by hand and after 30 days
	must(s.Comment(ctx, anna, "deal", deal, "заметка"))
	must(s.DeleteDeal(ctx, anna, deal))
	must(s.PurgeRecycleDue(ctx, time.Now()))
	if bin, _ := s.Recycle(ctx, anna, "", ""); len(bin) != 3 { // the company, «Вторая» and «Поставка»
		t.Fatalf("bin = %+v", bin)
	}
	must(s.PurgeRecycleDue(ctx, time.Now().AddDate(0, 0, RecycleDays+1)))
	if bin, _ := s.Recycle(ctx, anna, "", ""); len(bin) != 0 {
		t.Fatalf("the bin after 30 days = %+v", bin)
	}
	if tl, _ := s.Timeline(ctx, "deal", deal); len(tl) != 0 {
		t.Fatalf("the forgotten deal's timeline = %+v", tl)
	}
	lead2, err := s.SaveLead(ctx, anna, Lead{Title: "Ещё"})
	must(err)
	must(s.DeleteLead(ctx, anna, lead2))
	bin, _ = s.Recycle(ctx, anna, "", "")
	if err := s.PurgeRecycled(ctx, boris, bin[0].ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("boris deletes anna's card for good: %v", err)
	}
	must(s.EmptyRecycle(ctx, anna))
	if bin, _ := s.Recycle(admin, anna, "", ""); len(bin) != 0 {
		t.Fatalf("the bin after emptying = %+v", bin)
	}
}
