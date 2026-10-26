package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestRowsTotal(t *testing.T) {
	vat20 := 20.0
	rows := []ProductRow{
		{Price: 1200, Quantity: 2, Discount: 10, VATRate: &vat20, VATIncluded: true}, // 2160, VAT inside 360
		{Price: 1000, Quantity: 1, VATRate: &vat20},                                   // 1000 + 200 on top
		{Price: 99.99, Quantity: 3},                                                   // 299.97, no VAT
	}
	total, vat := RowsTotal(rows)
	if total != 3659.97 || vat != 560 {
		t.Fatalf("total %v, vat %v", total, vat)
	}
}

// TestProducts: the catalog; a card's rows (a catalog product without a name takes the product's data) count its
// amount, which the card's form then cannot change; a lead's rows go to its deal; rows go with their card.
func TestProducts(t *testing.T) {
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
	vat20 := 20.0

	if _, err := s.SaveProduct(ctx, anna, Product{Name: " "}); err == nil {
		t.Fatal("a product without a name")
	}
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "x", VATRate: &[]float64{120}[0]}); err == nil {
		t.Fatal("VAT 120%")
	}
	lic, err := s.SaveProduct(ctx, anna, Product{Name: "Лицензия", SKU: "L-1", Price: 1200, VATRate: &vat20, VATIncluded: true})
	must(err)
	old, err := s.SaveProduct(ctx, anna, Product{Name: "Старая модель", Price: 10})
	must(err)
	p, err := s.Product(ctx, old)
	must(err)
	p.Active = false
	_, err = s.SaveProduct(ctx, anna, p)
	must(err)
	if list, _ := s.Products(ctx, "", false); len(list) != 1 || list[0].ID != lic || list[0].Currency != "RUB" {
		t.Fatalf("for sale: %+v", list)
	}
	if list, _ := s.Products(ctx, "l-1", true); len(list) != 1 {
		t.Fatalf("by SKU: %+v", list)
	}

	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Amount: 5})
	must(err)
	if _, err := s.SetProductRows(ctx, anna, "lead", lead, []ProductRow{{Name: "x", Discount: 150}}); err == nil {
		t.Fatal("a discount of 150%")
	}
	total, err := s.SetProductRows(ctx, anna, "lead", lead, []ProductRow{{ProductID: lic, Quantity: 2}, {Name: "Внедрение", Price: 1000, VATRate: &vat20}})
	must(err)
	if total != 2400+1200 {
		t.Fatalf("total %v", total)
	}
	rows, err := s.ProductRows(ctx, "lead", lead)
	must(err)
	if len(rows) != 2 || rows[0].Name != "Лицензия" || rows[0].Price != 1200 || rows[1].Quantity != 1 || rows[1].VATIncluded {
		t.Fatalf("rows: %+v", rows)
	}
	l, err := s.Lead(ctx, lead)
	must(err)
	if l.Amount != 3600 {
		t.Fatalf("lead amount %v", l.Amount)
	}
	l.Amount = 1 // the form cannot change an amount counted from the rows
	_, err = s.SaveLead(ctx, anna, l)
	must(err)
	if l, _ = s.Lead(ctx, lead); l.Amount != 3600 {
		t.Fatalf("lead amount after the form %v", l.Amount)
	}

	deal, err := s.ConvertLead(ctx, anna, lead)
	must(err)
	if rows, _ := s.ProductRows(ctx, "deal", deal); len(rows) != 2 || rows[0].ProductID != lic {
		t.Fatalf("deal rows: %+v", rows)
	}
	must(s.DeleteProduct(ctx, lic)) // the rows keep their name and price
	if rows, _ := s.ProductRows(ctx, "deal", deal); len(rows) != 2 || rows[0].ProductID != "" || rows[0].Name != "Лицензия" {
		t.Fatalf("deal rows after the product is gone: %+v", rows)
	}
	// no rows: the amount is the card's own again
	_, err = s.SetProductRows(ctx, anna, "deal", deal, nil)
	must(err)
	d, err := s.Deal(ctx, deal)
	must(err)
	d.Amount = 77
	_, err = s.SaveDeal(ctx, anna, d)
	must(err)
	if d, _ = s.Deal(ctx, deal); d.Amount != 77 {
		t.Fatalf("deal amount %v", d.Amount)
	}
	must(s.DeleteLead(ctx, anna, lead))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_product_rows`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d rows outlived their card", left)
	}
}
