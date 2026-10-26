package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestStockReports: «Остатки» on a date go back over the documents conducted after it, valued at the average price
// of what came; «Движение товаров» counts what came and left over the period (a transfer only on one warehouse);
// a product's movements go document by document with what was left; drafts and cancelled documents don't count;
// «Продажи» sums the conducted shipments of the period in the base currency with the cost and the profit.
func TestStockReports(t *testing.T) {
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
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	doc := func(d StockDoc, conduct bool) string {
		t.Helper()
		id, err := s.SaveStockDoc(ctx, anna, d)
		must(err)
		if conduct {
			must(s.ConductStockDoc(ctx, anna, id))
		}
		return id
	}
	a, err := s.SaveWarehouse(ctx, Warehouse{Name: "A"})
	must(err)
	b, err := s.SaveWarehouse(ctx, Warehouse{Name: "B"})
	must(err)
	p, err := s.SaveProduct(ctx, anna, Product{Name: "p"})
	must(err)
	q, err := s.SaveProduct(ctx, anna, Product{Name: "q"})
	must(err)
	doc(StockDoc{Kind: "arrival", Date: day(1), Rows: []StockRow{{ProductID: p, Quantity: 10, Price: 100, To: a}}}, true)
	doc(StockDoc{Kind: "arrival", Date: day(5), Rows: []StockRow{{ProductID: p, Quantity: 10, Price: 130, To: a}}}, true)
	doc(StockDoc{Kind: "moving", Date: day(10), Rows: []StockRow{{ProductID: p, Quantity: 5, From: a, To: b}}}, true)
	deduct := doc(StockDoc{Kind: "deduct", Date: day(15), Rows: []StockRow{{ProductID: p, Quantity: 3, From: b}}}, true)
	doc(StockDoc{Kind: "adjustment", Date: day(2), Rows: []StockRow{{ProductID: p, Quantity: 99, Price: 1, To: a}}}, false)
	// q came at no price: valued at its purchase price
	doc(StockDoc{Kind: "adjustment", Date: day(3), Rows: []StockRow{{ProductID: q, Quantity: 2, To: b}}}, true)
	_, err = pool.Exec(ctx, `UPDATE crm_products SET purchase_price = 50 WHERE id::text = $1`, q)
	must(err)

	line := func(list []StockReportLine, id string) StockReportLine {
		for _, x := range list {
			if x.ID == id {
				return x
			}
		}
		return StockReportLine{}
	}
	list, err := s.StockReport(ctx, StockReportFilter{Date: day(3)})
	must(err)
	if x := line(list, p); len(list) != 2 || x.Quantity != 10 || x.Cost != 100 || x.Sum != 1000 || x.On[a] != 10 {
		t.Fatalf("on the 3rd %+v", list)
	}
	if x := line(list, q); x.Quantity != 2 || x.Cost != 50 || x.Sum != 100 {
		t.Fatalf("q %+v", x)
	}
	list, err = s.StockReport(ctx, StockReportFilter{Date: day(20)})
	must(err)
	if x := line(list, p); x.Quantity != 17 || x.Cost != 115 || x.Sum != 1955 || x.On[a] != 15 || x.On[b] != 2 {
		t.Fatalf("on the 20th %+v", x)
	}
	list, err = s.StockReport(ctx, StockReportFilter{Date: day(20), Warehouse: a})
	must(err)
	if len(list) != 1 || list[0].Quantity != 15 {
		t.Fatalf("on A %+v", list)
	}
	if list, _ = s.StockReport(ctx, StockReportFilter{Date: day(20), Q: "q"}); len(list) != 1 || list[0].ID != q {
		t.Fatalf("found %+v", list)
	}

	mv, err := s.MovementReport(ctx, StockReportFilter{From: day(4), To: day(12)})
	must(err)
	if len(mv) != 2 || mv[0].ID != p || mv[0].Opening != 10 || mv[0].In != 10 || mv[0].Out != 0 || mv[0].Closing != 20 || mv[0].Sum != 2300 {
		t.Fatalf("movement %+v", mv)
	}
	if mv[1].Opening != 2 || mv[1].In != 0 || mv[1].Closing != 2 {
		t.Fatalf("q unmoved %+v", mv[1])
	}
	mv, err = s.MovementReport(ctx, StockReportFilter{From: day(4), To: day(12), Warehouse: a})
	must(err)
	if len(mv) != 1 || mv[0].Opening != 10 || mv[0].In != 10 || mv[0].Out != 5 || mv[0].Closing != 15 {
		t.Fatalf("movement on A %+v", mv)
	}

	opening, moves, err := s.ProductMoves(ctx, StockReportFilter{ProductID: p, From: day(2), To: day(31)})
	must(err)
	want := []struct{ q, left float64 }{{10, 20}, {-5, 15}, {5, 20}, {-3, 17}}
	if opening != 10 || len(moves) != len(want) {
		t.Fatalf("moves %v %+v", opening, moves)
	}
	for i, w := range want {
		if moves[i].Quantity != w.q || moves[i].Balance != w.left {
			t.Fatalf("move %d %+v", i, moves[i])
		}
	}
	if moves[3].DocID != deduct || moves[3].Warehouse.Name != "B" || moves[3].Kind != "deduct" {
		t.Fatalf("the deduct %+v", moves[3])
	}

	// cancelled — as if it never was
	must(s.CancelStockDoc(ctx, anna, deduct))
	list, err = s.StockReport(ctx, StockReportFilter{Date: day(20), Warehouse: b})
	must(err)
	if x := line(list, p); x.Quantity != 5 {
		t.Fatalf("after cancelling %+v", list)
	}
	if _, moves, _ = s.ProductMoves(ctx, StockReportFilter{ProductID: p, From: day(1), To: day(31), Warehouse: b}); len(moves) != 1 || moves[0].Balance != 5 {
		t.Fatalf("on B %+v", moves)
	}

	// «Продажи»: the conducted shipments of the period, a dollar one by its rate
	must(s.SaveCurrency(ctx, Currency{Code: "USD", Rate: 90}))
	doc(StockDoc{Kind: "shipment", Date: day(16), Rows: []StockRow{{ProductID: p, Quantity: 4, Price: 200, From: a}}}, true)
	doc(StockDoc{Kind: "shipment", Date: day(18), Currency: "USD", Rows: []StockRow{{ProductID: p, Quantity: 1, Price: 3, From: a}}}, true)
	doc(StockDoc{Kind: "shipment", Date: day(19), Rows: []StockRow{{ProductID: p, Quantity: 1, Price: 999, From: a}}}, false)
	doc(StockDoc{Kind: "shipment", Date: day(25), Rows: []StockRow{{ProductID: q, Quantity: 1, Price: 80, From: b}}}, true)
	sales, err := s.SalesReport(ctx, StockReportFilter{From: day(1), To: day(20)})
	must(err)
	if len(sales) != 1 || sales[0].ID != p || sales[0].Quantity != 5 || sales[0].Revenue != 1070 || sales[0].Docs != 2 ||
		sales[0].Cost != 575 || sales[0].Profit != 495 || sales[0].Margin != 86.1 {
		t.Fatalf("sales %+v", sales)
	}
	if sales, _ = s.SalesReport(ctx, StockReportFilter{From: day(1), To: day(20), Warehouse: b}); len(sales) != 0 {
		t.Fatalf("sales on B %+v", sales)
	}
	sales, err = s.SalesReport(ctx, StockReportFilter{From: day(1), To: day(31)})
	must(err)
	if len(sales) != 2 || sales[0].ID != p || sales[1].ID != q || sales[1].Revenue != 80 || sales[1].Cost != 50 || sales[1].Margin != 60 {
		t.Fatalf("sales of the month %+v", sales)
	}
}
