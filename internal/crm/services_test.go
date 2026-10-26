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

// TestServices: a service of the catalog is sold in a deal but never kept on a warehouse — its row has no
// warehouse or reserve, a shipment and «not shipped» leave it out, a warehouse document refuses it, the stock and
// its reports don't list it; it has no variations; a product becomes a service only without variations or
// warehouse documents, and its deals' reserves go then; the catalog shows the products or the services.
func TestServices(t *testing.T) {
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
	chair, err := s.SaveProduct(ctx, anna, Product{Name: "Стул", Price: 1200})
	must(err)
	setup, err := s.SaveProduct(ctx, anna, Product{Name: "Сборка", Price: 300, Kind: KindService, Active: true})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{Name: "Ремонт", Kind: "repair"})
	errKey(err, "err.crm_product_kind")
	_, err = s.SaveProduct(ctx, anna, Product{ParentID: setup, Name: "Сборка (быстро)"})
	errKey(err, "err.crm_service_variant")

	in, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: chair, Quantity: 10}}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, in))
	_, err = s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: setup, Quantity: 1}}})
	errKey(err, "err.stock_service")

	pipes, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Офис", PipelineID: pipes[0].ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 2, Reserve: -1, WarehouseID: main},
		{ProductID: setup, Quantity: 2, Reserve: -1, WarehouseID: main}})
	must(err)
	rows, err := s.ProductRows(ctx, "deal", deal)
	must(err)
	if rows[0].Service || rows[0].Reserve != 2 || !rows[1].Service || rows[1].WarehouseID != "" || rows[1].Reserve != 0 || rows[1].Name != "Сборка" {
		t.Fatalf("rows %+v", rows)
	}
	if left, err := s.Unshipped(ctx, deal); err != nil || left != 2 {
		t.Fatalf("unshipped %v %v", left, err)
	}
	ship, err := s.NewShipment(ctx, anna, deal)
	must(err)
	d, err := s.StockDoc(ctx, ship)
	must(err)
	if len(d.Rows) != 1 || d.Rows[0].ProductID != chair {
		t.Fatalf("shipment %+v", d.Rows)
	}

	stock, err := s.Stock(ctx, "", "", true, false)
	must(err)
	report, err := s.MovementReport(ctx, StockReportFilter{From: time.Now(), To: time.Now()})
	must(err)
	for _, x := range stock {
		if x.Product.ID == setup {
			t.Fatal("a service in the stock")
		}
	}
	for _, x := range report {
		if x.Product.ID == setup {
			t.Fatal("a service in the movements")
		}
	}

	only := func(kind string, want ...string) {
		t.Helper()
		cat, err := s.Catalog(ctx, "", "", kind, true)
		must(err)
		if len(cat) != len(want) {
			t.Fatalf("catalog %q: %+v", kind, cat)
		}
		for i, p := range cat {
			if p.Name != want[i] {
				t.Fatalf("catalog %q: %+v", kind, cat)
			}
		}
	}
	only("", "Сборка", "Стул")
	only(KindService, "Сборка")
	only(KindProduct, "Стул")

	// a product with warehouse documents stays a product; one without them becomes a service, its reserves gone
	p, err := s.Product(ctx, chair)
	must(err)
	p.Kind = KindService
	_, err = s.SaveProduct(ctx, anna, p)
	errKey(err, "err.crm_service_stock")
	table, err := s.SaveProduct(ctx, anna, Product{Name: "Стол", Price: 5000, Active: true})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: table, Quantity: 1, Reserve: -1, WarehouseID: main}})
	must(err)
	p, err = s.Product(ctx, table)
	must(err)
	p.Kind = KindService
	_, err = s.SaveProduct(ctx, anna, p)
	must(err)
	p.Kind = "" // not given: kept
	_, err = s.SaveProduct(ctx, anna, p)
	must(err)
	if p, _ = s.Product(ctx, table); !p.Service() {
		t.Fatalf("kind %q", p.Kind)
	}
	if rows, _ = s.ProductRows(ctx, "deal", deal); rows[0].Reserve != 0 || rows[0].WarehouseID != "" || !rows[0].Service {
		t.Fatalf("reserve of a service %+v", rows)
	}
}
