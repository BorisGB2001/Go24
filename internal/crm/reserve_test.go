package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestReserve: a deal's row is reserved on its warehouse (the whole quantity by default, no more than it), the
// available stock is less by it; a lead's row and a free line aren't reserved; an expired reserve and that of a
// closed deal don't count. «Реализация» of a deal takes what isn't shipped yet at the rows' prices; conducted, it
// takes the stock and frees the reserve; nothing left — an error; cancelled, the reserve is back.
func TestReserve(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	main, err := s.SaveWarehouse(ctx, Warehouse{Name: "Основной"})
	must(err)
	shop, err := s.SaveWarehouse(ctx, Warehouse{Name: "Магазин"})
	must(err)
	chair, err := s.SaveProduct(ctx, anna, Product{Name: "Стул", Price: 1200})
	must(err)
	in, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: chair, Quantity: 10}, {ProductID: chair, Quantity: 2, To: shop}}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, in))
	free := func(but string, wh string, want float64) {
		t.Helper()
		av, err := s.Available(ctx, []string{chair}, but)
		must(err)
		if av[chair][wh] != want {
			t.Fatalf("available on %s: %v, want %v", wh, av[chair][wh], want)
		}
	}

	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Офис", PipelineID: p.ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 3, Reserve: 4, WarehouseID: main}})
	errKey(err, "err.stock_reserve")
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 3, Discount: 50, Reserve: -1, WarehouseID: main},
		{ProductID: chair, Quantity: 1, Reserve: 1}, {Name: "Доставка", Price: 500, Reserve: 1, WarehouseID: main}})
	must(err)
	rows, err := s.ProductRows(ctx, "deal", deal)
	must(err)
	if rows[0].Reserve != 3 || rows[0].WarehouseName != "Основной" || rows[1].WarehouseID != main || rows[2].Reserve != 0 || rows[2].WarehouseID != "" {
		t.Fatalf("rows %+v", rows)
	}
	free("", main, 6)
	free(deal, main, 10) // its own reserve isn't counted
	free("", shop, 2)
	lines, err := s.Stock(ctx, "", "", false, true)
	must(err)
	if len(lines) != 1 || lines[0].Reserve != 4 || lines[0].Free() != 8 {
		t.Fatalf("stock %+v", lines)
	}
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Лид"})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "lead", lead, []ProductRow{{ProductID: chair, Quantity: 5, Reserve: 5, WarehouseID: main}})
	must(err)
	free("", main, 6)

	// an expired reserve, and a closed deal's, don't hold
	other, err := s.SaveDeal(ctx, anna, Deal{Title: "Склад", PipelineID: p.ID})
	must(err)
	past := time.Now().AddDate(0, 0, -1)
	_, err = s.SetProductRows(ctx, anna, "deal", other, []ProductRow{{ProductID: chair, Quantity: 2, Reserve: 2, WarehouseID: shop, ReserveUntil: &past}})
	must(err)
	free("", shop, 2)
	next := time.Now().AddDate(0, 0, 3)
	_, err = s.SetProductRows(ctx, anna, "deal", other, []ProductRow{{ProductID: chair, Quantity: 2, Reserve: 2, WarehouseID: shop, ReserveUntil: &next}})
	must(err)
	free("", shop, 0)
	_, err = s.MoveDeal(ctx, anna, other, p.Stages[len(p.Stages)-1].ID)
	must(err)
	free("", shop, 2)

	// «Реализация»
	sh, err := s.NewShipment(ctx, anna, deal)
	must(err)
	d, err := s.StockDoc(ctx, sh)
	must(err)
	if d.Kind != "shipment" || d.Deal.ID != deal || d.Deal.Name != "Офис" || d.Status != "draft" || len(d.Rows) != 2 ||
		d.Rows[0].Quantity != 3 || d.Rows[0].Price != 600 || d.Rows[0].From != main || d.Rows[1].Price != 1200 || d.Amount != 3000 {
		t.Fatalf("shipment %+v", d)
	}
	_, err = s.NewShipment(ctx, anna, deal) // the draft takes all
	errKey(err, "err.stock_shipped_all")
	if n, _ := s.Unshipped(ctx, deal); n != 4 {
		t.Fatalf("unshipped %v", n)
	}
	must(s.ConductStockDoc(ctx, anna, sh))
	free("", main, 6) // 10 − 4 shipped, the reserve is gone
	if on, _ := s.ProductStock(ctx, chair); on[main] != 6 {
		t.Fatalf("stock %v", on)
	}
	if n, _ := s.Unshipped(ctx, deal); n != 0 {
		t.Fatalf("unshipped %v", n)
	}
	if list, _ := s.StockDocs(ctx, StockFilter{DealID: deal}); len(list) != 1 || list[0].ID != sh {
		t.Fatalf("deal's shipments %+v", list)
	}
	must(s.CancelStockDoc(ctx, anna, sh))
	free("", main, 6) // 10 back, 4 reserved again
	if n, _ := s.Unshipped(ctx, deal); n != 4 {
		t.Fatalf("unshipped after cancel %v", n)
	}
	// more is sold: the next shipment takes only what is new
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 6, Reserve: -1, WarehouseID: main}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, sh))
	sh2, err := s.NewShipment(ctx, anna, deal)
	must(err)
	d, err = s.StockDoc(ctx, sh2)
	must(err)
	if len(d.Rows) != 1 || d.Rows[0].Quantity != 2 {
		t.Fatalf("second shipment %+v", d.Rows)
	}
	free("", main, 4) // 6 on it, 2 more reserved
}
