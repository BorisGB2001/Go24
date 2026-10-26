package crm

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestStockSettings: by default a row without a warehouse isn't reserved and a won deal ships nothing; with
// «Резервировать автоматически» it is reserved in full on the default warehouse till today plus the term (a reserve
// given, a service — not); with «Списывать при успешном закрытии» a won deal makes a «Реализация» of what isn't
// shipped and conducts it — not enough on the warehouse, it stays a draft; the deal's timeline tells either way.
func TestStockSettings(t *testing.T) {
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
	if set, err := s.StockSettings(ctx); err != nil || set != (StockSettings{}) {
		t.Fatalf("default settings %+v %v", set, err)
	}
	if e, ok := s.SetStockSettings(ctx, anna, StockSettings{ReserveDays: -1}).(*i18n.Error); !ok || e.Key != "err.stock_reserve_days" {
		t.Fatalf("negative term: %v", e)
	}
	main, err := s.SaveWarehouse(ctx, Warehouse{Name: "Основной"})
	must(err)
	chair, err := s.SaveProduct(ctx, anna, Product{Name: "Стул", Price: 1000})
	must(err)
	table, err := s.SaveProduct(ctx, anna, Product{Name: "Стол", Price: 5000})
	must(err)
	setup, err := s.SaveProduct(ctx, anna, Product{Name: "Сборка", Price: 500, Kind: KindService})
	must(err)
	in, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "adjustment", Rows: []StockRow{{ProductID: chair, Quantity: 10}, {ProductID: table, Quantity: 1}}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, in))
	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	var won string
	for _, st := range p.Stages {
		if st.Semantic == "success" {
			won = st.ID
		}
	}
	rows := func(deal string) []ProductRow {
		t.Helper()
		list, err := s.ProductRows(ctx, "deal", deal)
		must(err)
		return list
	}

	// by default: a row without a warehouse isn't reserved, a won deal ships nothing
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Офис", PipelineID: p.ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 2, Reserve: -1}})
	must(err)
	if r := rows(deal); r[0].Reserve != 0 || r[0].WarehouseID != "" {
		t.Fatalf("no auto reserve %+v", r)
	}
	_, err = s.MoveDeal(ctx, anna, deal, won)
	must(err)
	if list, _ := s.StockDocs(ctx, StockFilter{DealID: deal}); len(list) != 0 {
		t.Fatalf("shipped without the setting %+v", list)
	}

	// «Резервировать автоматически» with a term; a reserve given by hand stays as it is
	must(s.SetStockSettings(ctx, anna, StockSettings{AutoReserve: true, ReserveDays: 5, ShipOnWin: true}))
	deal, err = s.SaveDeal(ctx, anna, Deal{Title: "Кафе", PipelineID: p.ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 3, Reserve: -1}, {ProductID: table, Quantity: 2, Reserve: 0},
		{ProductID: setup, Quantity: 1, Reserve: -1}})
	must(err)
	r := rows(deal)
	want := time.Now().AddDate(0, 0, 5).Format(time.DateOnly)
	if r[0].Reserve != 3 || r[0].WarehouseID != main || r[0].ReserveUntil == nil || r[0].ReserveUntil.Format(time.DateOnly) != want ||
		r[1].Reserve != 0 || r[1].ReserveUntil != nil || r[2].Reserve != 0 || r[2].WarehouseID != "" {
		t.Fatalf("auto reserve %+v", r)
	}

	// won: not enough tables — the shipment stays a draft, the timeline tells
	_, err = s.MoveDeal(ctx, anna, deal, won)
	must(err)
	list, err := s.StockDocs(ctx, StockFilter{DealID: deal})
	must(err)
	if len(list) != 1 || list[0].Status != "draft" {
		t.Fatalf("draft shipment %+v", list)
	}
	events := func(deal, kind string) string {
		t.Helper()
		tl, err := s.Timeline(ctx, "deal", deal)
		must(err)
		for _, e := range tl {
			if e.Kind == kind {
				return e.Body
			}
		}
		return ""
	}
	if b := events(deal, "ship_draft"); b != list[0].ID+"|"+strconv.Itoa(list[0].Number) {
		t.Fatalf("ship_draft event %q", b)
	}

	// enough: conducted by itself, the stock goes down
	deal, err = s.SaveDeal(ctx, anna, Deal{Title: "Дом", PipelineID: p.ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: chair, Quantity: 4, Reserve: -1}, {ProductID: setup, Quantity: 1}})
	must(err)
	_, err = s.MoveDeal(ctx, anna, deal, won)
	must(err)
	list, err = s.StockDocs(ctx, StockFilter{DealID: deal})
	must(err)
	if len(list) != 1 || list[0].Status != "conducted" || events(deal, "ship_auto") == "" {
		t.Fatalf("conducted shipment %+v", list)
	}
	if on, _ := s.ProductStock(ctx, chair); on[main] != 6 {
		t.Fatalf("stock %v", on)
	}
	if n, _ := s.Unshipped(ctx, deal); n != 0 {
		t.Fatalf("unshipped %v", n)
	}
}
