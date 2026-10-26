package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestInvoices: quotes and invoices are made from a deal with its products, numbered per kind; their own rows count
// their amount, not the deal's; statuses go to the deal's timeline; the seller's details are kept; all go with the deal.
func TestInvoices(t *testing.T) {
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
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 3}})
	must(err)

	if _, err := s.MakeInvoice(ctx, anna, "receipt", deal, 0); err == nil {
		t.Fatal("an unknown kind")
	}
	q, err := s.MakeInvoice(ctx, anna, "quote", deal, 0)
	must(err)
	i1, err := s.MakeInvoice(ctx, anna, "invoice", deal, 5)
	must(err)
	i2, err := s.MakeInvoice(ctx, anna, "invoice", deal, 5)
	must(err)
	v, err := s.Invoice(ctx, i2)
	must(err)
	if v.Number != 2 || v.Status != "new" || v.Amount != 3000 || v.DueDate == nil || v.CardTitle != "Поставка" {
		t.Fatalf("second invoice: %+v", v)
	}
	if qv, _ := s.Invoice(ctx, q); qv.Number != 1 || qv.Status != "draft" || qv.DueDate != nil {
		t.Fatalf("quote: %+v", qv)
	}
	rows, err := s.ProductRows(ctx, "invoice", i1)
	must(err)
	if len(rows) != 1 || rows[0].Quantity != 3 {
		t.Fatalf("invoice rows: %+v", rows)
	}
	// the invoice's own rows: its amount changes, the deal's stays
	_, err = s.SetProductRows(ctx, anna, "invoice", i1, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 1}})
	must(err)
	if v, _ := s.Invoice(ctx, i1); v.Amount != 1000 {
		t.Fatalf("invoice amount %v", v.Amount)
	}
	if d, _ := s.Deal(ctx, deal); d.Amount != 3000 {
		t.Fatalf("deal amount %v", d.Amount)
	}

	if err := s.SetInvoiceStatus(ctx, anna, i1, "accepted"); err == nil {
		t.Fatal("a quote's status on an invoice")
	}
	must(s.SetInvoiceStatus(ctx, anna, i1, "paid"))
	tl, err := s.Timeline(ctx, "deal", deal)
	must(err)
	if tl[0].Kind != "invoice" || tl[0].Body != "invoice:1:paid" {
		t.Fatalf("timeline: %+v", tl[0])
	}
	v.Title, v.Comment = "Счёт на лицензии", "Оплата в течение 5 дней"
	must(s.SaveInvoice(ctx, v))
	if list, _ := s.Invoices(ctx, anna, "", "invoice", "paid"); len(list) != 1 || list[0].ID != i1 {
		t.Fatalf("paid invoices: %+v", list)
	}
	if list, _ := s.Invoices(ctx, "", deal, "", ""); len(list) != 3 {
		t.Fatalf("the deal's documents: %d", len(list))
	}

	must(s.SetSeller(ctx, anna, "ООО Го24\nИНН 7700000000"))
	if seller, _ := s.Seller(ctx); seller != "ООО Го24\nИНН 7700000000" {
		t.Fatalf("seller %q", seller)
	}

	must(s.DeleteDeal(ctx, anna, deal))
	var left int
	must(pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM crm_invoices) + (SELECT count(*) FROM crm_product_rows)`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d documents or rows outlived their deal", left)
	}
}
