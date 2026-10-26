package crm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestStock: warehouses (the first is the default; one default only); an arrival from a supplier puts products on
// a warehouse when conducted and sets their purchase price; a conducted document can't be changed or deleted; a
// transfer moves them, a write-off takes them — not more than there is, all rows or none; cancelling gives the
// stock back (not when what it brought is gone); a warehouse with stock or documents can't be deleted.
func TestStock(t *testing.T) {
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
	errKey := func(err error, key string) {
		t.Helper()
		if e, ok := err.(*i18n.Error); !ok || e.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'bob', 'bob@t') RETURNING id`).Scan(&bob))
	s := NewStore(pool)

	conducted := func(d StockDoc) error { // makes a document and conducts it
		id, err := s.SaveStockDoc(ctx, anna, d)
		if err != nil {
			return err
		}
		return s.ConductStockDoc(ctx, anna, id)
	}
	x, err := s.SaveProduct(ctx, anna, Product{Name: "x"})
	must(err)
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: x, Quantity: 1}}})
	errKey(err, "err.stock_no_warehouse")
	must(s.DeleteProduct(ctx, x))
	main, err := s.SaveWarehouse(ctx, Warehouse{Name: "Основной"})
	must(err)
	shop, err := s.SaveWarehouse(ctx, Warehouse{Name: "Магазин", Responsible: Ref{ID: bob}})
	must(err)
	whs, err := s.Warehouses(ctx)
	must(err)
	if len(whs) != 2 || whs[0].ID != main || !whs[0].Default || whs[1].Default || whs[1].Responsible.Name != "bob" {
		t.Fatalf("warehouses %+v", whs)
	}

	pen, err := s.SaveProduct(ctx, anna, Product{Name: "Ручка", Price: 50})
	must(err)
	pad, err := s.SaveProduct(ctx, anna, Product{Name: "Блокнот", Price: 120})
	must(err)
	supplier, err := s.SaveCompany(ctx, anna, Company{Title: "ООО Канцтовары", Responsible: Ref{ID: anna}})
	must(err)

	// an arrival: to the default warehouse when none is given
	arr, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "arrival", Supplier: Ref{ID: supplier}, IncomingNumber: "ТН-17",
		Rows: []StockRow{{ProductID: pen, Quantity: 100, Price: 20}, {ProductID: pad, Quantity: 10, Price: 70, To: shop}}})
	must(err)
	d, err := s.StockDoc(ctx, arr)
	must(err)
	if d.Number != 1 || d.Status != "draft" || d.Amount != 2700 || d.Supplier.Name != "ООО Канцтовары" || len(d.Rows) != 2 ||
		d.Rows[0].To != main || d.Rows[0].Name != "Ручка" || d.Rows[1].ToName != "Магазин" || d.Rows[0].From != "" {
		t.Fatalf("arrival %+v", d)
	}
	if on, _ := s.ProductStock(ctx, pen); len(on) != 0 {
		t.Fatalf("a draft moved the stock: %v", on)
	}
	must(s.ConductStockDoc(ctx, anna, arr))
	errKey(s.ConductStockDoc(ctx, anna, arr), "err.stock_conducted")
	if on, _ := s.ProductStock(ctx, pen); on[main] != 100 {
		t.Fatalf("pens %v", on)
	}
	if p, _ := s.Product(ctx, pad); p.PurchasePrice == nil || *p.PurchasePrice != 70 {
		t.Fatalf("purchase price %+v", p.PurchasePrice)
	}
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{ID: arr, Rows: []StockRow{{ProductID: pen, Quantity: 1}}})
	errKey(err, "err.stock_conducted")
	errKey(s.DeleteStockDoc(ctx, arr), "err.stock_conducted")
	d, _ = s.StockDoc(ctx, arr)
	if d.ConductedBy.ID != anna || d.ConductedAt == nil {
		t.Fatalf("conducted by %+v", d.ConductedBy)
	}

	// a transfer of 30 pens to the shop; the same warehouse twice is wrong
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{Kind: "moving", Rows: []StockRow{{ProductID: pen, Quantity: 1, From: main, To: main}}})
	errKey(err, "err.stock_same_warehouse")
	mov, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "moving", Rows: []StockRow{{ProductID: pen, Quantity: 30, To: shop}}})
	must(err)
	must(s.ConductStockDoc(ctx, bob, mov))
	if on, _ := s.ProductStock(ctx, pen); on[main] != 70 || on[shop] != 30 {
		t.Fatalf("after moving %v", on)
	}

	// a write-off of more than there is fails as a whole
	ded, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "deduct", Reason: "Брак",
		Rows: []StockRow{{ProductID: pen, Quantity: 5, From: shop}, {ProductID: pad, Quantity: 11, From: shop}}})
	must(err)
	err = s.ConductStockDoc(ctx, anna, ded)
	errKey(err, "err.stock_short")
	if !strings.Contains(err.Error(), "Блокнот") || !strings.Contains(err.Error(), "Магазин") {
		t.Fatalf("message %q", err)
	}
	if on, _ := s.ProductStock(ctx, pen); on[shop] != 30 {
		t.Fatalf("a failed write-off moved the stock: %v", on)
	}
	d, _ = s.StockDoc(ctx, ded)
	d.Rows[1].Quantity = 4
	_, err = s.SaveStockDoc(ctx, anna, d)
	must(err)
	must(s.ConductStockDoc(ctx, anna, ded))
	if on, _ := s.ProductStock(ctx, pad); on[shop] != 6 {
		t.Fatalf("pads %v", on)
	}
	if d, _ = s.StockDoc(ctx, ded); d.Reason != "Брак" || d.Rows[0].To != "" || d.Rows[0].From != shop || d.Rows[0].Price != 20 || d.Amount != 380 { // the purchase prices
		t.Fatalf("deduct %+v", d)
	}

	// cancelling the transfer brings the pens back; cancelling the arrival can't: the shop's pads were written off
	must(s.CancelStockDoc(ctx, anna, ded))
	errKey(s.CancelStockDoc(ctx, anna, ded), "err.stock_not_conducted")
	must(s.CancelStockDoc(ctx, anna, mov))
	if on, _ := s.ProductStock(ctx, pen); on[main] != 100 || on[shop] != 0 {
		t.Fatalf("after cancelling %v", on)
	}
	if d, _ = s.StockDoc(ctx, mov); d.Status != "cancelled" {
		t.Fatalf("status %s", d.Status)
	}
	mov2, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "moving", Rows: []StockRow{{ProductID: pad, Quantity: 10, From: shop, To: main}}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, mov2))
	must(conducted(StockDoc{Kind: "deduct", Rows: []StockRow{{ProductID: pad, Quantity: 10, From: main}}}))
	errKey(s.CancelStockDoc(ctx, anna, mov2), "err.stock_short")

	// the lists: by kind, by status, by the supplier's name and by a product
	for _, c := range []struct {
		f    StockFilter
		want int
	}{{StockFilter{}, 5}, {StockFilter{Kind: "moving"}, 2}, {StockFilter{Status: "cancelled"}, 2}, {StockFilter{Q: "канц"}, 1},
		{StockFilter{Q: "ТН-17"}, 1}, {StockFilter{ProductID: pen}, 3}} {
		if list, err := s.StockDocs(ctx, c.f); err != nil || len(list) != c.want {
			t.Fatalf("%+v: %d %v", c.f, len(list), err)
		}
	}
	lines, err := s.Stock(ctx, "", "", false, true)
	must(err)
	if len(lines) != 1 || lines[0].ID != pen || lines[0].Total != 100 {
		t.Fatalf("stock %+v", lines)
	}
	if totals, _ := s.StockTotals(ctx); totals[pen] != 100 || totals[pad] != 0 {
		t.Fatalf("totals %v", totals)
	}

	// a warehouse used by documents stays; a new default takes it from the old one; an unused one goes
	errKey(s.DeleteWarehouse(ctx, shop), "err.stock_warehouse_used")
	spare, err := s.SaveWarehouse(ctx, Warehouse{Name: "Запасной", Default: true})
	must(err)
	whs, _ = s.Warehouses(ctx)
	if whs[0].ID != spare || !whs[0].Default || whs[1].Default || whs[2].ID != main || whs[2].Value != 2000 {
		t.Fatalf("default %+v", whs)
	}
	errKey(s.DeleteWarehouse(ctx, spare), "err.stock_warehouse_default")
	_, err = s.SaveWarehouse(ctx, Warehouse{ID: main, Name: "Основной", Default: true, Active: true})
	must(err)
	must(s.DeleteWarehouse(ctx, spare))
	draft, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: pen, Quantity: 1, To: main}}})
	must(err)
	must(s.DeleteStockDoc(ctx, draft))
	errKey(s.ConductStockDoc(ctx, anna, draft), "err.not_found")

	// the supplier of an arrival is a contact or a company, one of them
	ivan, err := s.SaveContact(ctx, anna, Contact{Name: "Иван Поставщиков", Responsible: Ref{ID: anna}})
	must(err)
	byContact, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "arrival", Supplier: Ref{ID: ivan}, SupplierKind: "contact",
		Rows: []StockRow{{ProductID: pen, Quantity: 1, Price: 20}}})
	must(err)
	if d, _ = s.StockDoc(ctx, byContact); d.Supplier.ID != ivan || d.SupplierKind != "contact" || d.Supplier.Name != "Иван Поставщиков" {
		t.Fatalf("a contact supplier %+v", d)
	}
	if list, _ := s.StockDocs(ctx, StockFilter{Q: "поставщиков"}); len(list) != 1 || list[0].ID != byContact {
		t.Fatalf("found by the contact %+v", list)
	}
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{ID: byContact, Supplier: Ref{ID: supplier}, Rows: []StockRow{{ProductID: pen, Quantity: 1, Price: 20}}})
	must(err)
	if d, _ = s.StockDoc(ctx, byContact); d.Supplier.ID != supplier || d.SupplierKind != "company" {
		t.Fatalf("the company instead %+v", d)
	}
}
