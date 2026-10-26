package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestProductVariants: a variation is named by its product and its values of the properties «для вариаций», takes
// the product's section, unit and VAT (and keeps them as the product changes), keeps its product, can't have
// variations of its own; the catalog lists the product with its variations (found by a variation's SKU or barcode),
// the pickers and the stock — the variations instead of the product; an inactive product hides its variations; a
// variation is reserved in a deal and conducted to a warehouse; the product goes with its variations.
func TestProductVariants(t *testing.T) {
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

	color, err := s.SaveProp(ctx, Prop{Name: "Цвет", Kind: PropList, Options: []string{"Красный", "Синий"}, Variant: true, Sort: 1})
	must(err)
	size, err := s.SaveProp(ctx, Prop{Name: "Размер", Kind: PropString, Variant: true, Sort: 2})
	must(err)
	cotton, err := s.SaveProp(ctx, Prop{Name: "Хлопок", Kind: PropBool, Sort: 3})
	must(err)
	props, err := s.ProductProps(ctx)
	must(err)
	if len(props) != 3 || !props[0].Variant || props[2].Variant {
		t.Fatalf("props %+v", props)
	}
	clothes, err := s.SaveSection(ctx, Section{Name: "Одежда"})
	must(err)
	vat := 20.0
	shirt, err := s.SaveProduct(ctx, anna, Product{Name: "Футболка", SKU: "TS", Price: 900, Unit: "шт", VATRate: &vat, VATIncluded: true,
		SectionID: clothes, Props: map[string]string{cotton: "1"}})
	must(err)
	red, err := s.SaveProduct(ctx, anna, Product{ParentID: shirt, SKU: "TS-R-M", Price: 950, Props: map[string]string{color: "Красный", size: "M"},
		Barcodes: []string{"4600000000017"}})
	must(err)
	blue, err := s.SaveProduct(ctx, anna, Product{ParentID: shirt, Name: "Футболка синяя", SKU: "TS-B", Price: 900, Props: map[string]string{color: "Синий"}})
	must(err)
	v, err := s.Product(ctx, red)
	must(err)
	if v.Name != "Футболка (Красный, M)" || v.ParentID != shirt || v.SectionID != clothes || v.Unit != "шт" || v.VATRate == nil || *v.VATRate != 20 ||
		!v.VATIncluded || v.Currency != "RUB" || v.Price != 950 {
		t.Fatalf("variation %+v", v)
	}
	if _, err := s.SaveProduct(ctx, anna, Product{ParentID: red, Name: "x"}); err == nil || err.(*i18n.Error).Key != "err.crm_variant_parent" {
		t.Fatalf("a variation of a variation: %v", err)
	}
	if _, err := s.SaveProduct(ctx, anna, Product{ParentID: "00000000-0000-0000-0000-000000000000", Name: "x"}); err != ErrNotFound {
		t.Fatalf("a variation of nothing: %v", err)
	}

	// the product's changes reach its variations; a variation keeps its product
	shoes, err := s.SaveSection(ctx, Section{Name: "Обувь"})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{ID: shirt, Name: "Футболка", Price: 900, Unit: "упак", SectionID: shoes, Active: true})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{ID: blue, ParentID: red, Name: "Футболка синяя", Price: 990, SectionID: clothes, Active: true})
	must(err)
	v, err = s.Product(ctx, blue)
	must(err)
	if v.ParentID != shirt || v.SectionID != shoes || v.Unit != "упак" || v.VATRate != nil || v.Price != 990 {
		t.Fatalf("variation after the product's change %+v", v)
	}

	plain, err := s.SaveProduct(ctx, anna, Product{Name: "Носки", Price: 100})
	must(err)
	names := func(list []Product) (out []string) {
		for _, p := range list {
			out = append(out, p.Name)
		}
		return out
	}
	sale, err := s.ProductsOf(ctx, "", "", false)
	must(err)
	if got := names(sale); len(got) != 3 || got[0] != "Носки" || got[1] != "Футболка (Красный, M)" || got[2] != "Футболка синяя" ||
		sale[1].SectionPath != "Обувь" {
		t.Fatalf("on sale %v %+v", got, sale)
	}
	cat, err := s.Catalog(ctx, "", "", "", false)
	must(err)
	if len(cat) != 2 || cat[1].ID != shirt || len(cat[1].Variants) != 2 || cat[1].Variants[0].ID != red || cat[1].Variants[0].SectionPath != "Обувь" {
		t.Fatalf("catalog %+v", cat)
	}
	for _, q := range []string{"TS-R", "4600000000017", "футб"} {
		cat, err = s.Catalog(ctx, q, "", "", false)
		must(err)
		if len(cat) != 1 || cat[0].ID != shirt || len(cat[0].Variants) != 2 {
			t.Fatalf("catalog by %q: %+v", q, cat)
		}
	}
	if p, err := s.ProductByBarcode(ctx, "4600000000017"); err != nil || p.ID != red {
		t.Fatalf("by barcode %+v %v", p, err)
	}
	vars, err := s.Variants(ctx, shirt)
	must(err)
	if len(vars) != 2 {
		t.Fatalf("variants %+v", vars)
	}

	// an inactive product: its variations aren't on sale; an inactive variation is hidden from the catalog too
	_, err = s.SaveProduct(ctx, anna, Product{ID: shirt, Name: "Футболка", Price: 900, SectionID: shoes})
	must(err)
	sale, err = s.ProductsOf(ctx, "", "", false)
	must(err)
	if got := names(sale); len(got) != 1 || got[0] != "Носки" {
		t.Fatalf("on sale with the product inactive %v", got)
	}
	_, err = s.SaveProduct(ctx, anna, Product{ID: shirt, Name: "Футболка", Price: 900, SectionID: shoes, Active: true})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{ID: blue, Name: "Футболка синяя", Price: 990})
	must(err)
	cat, err = s.Catalog(ctx, "", "", "", false)
	must(err)
	if len(cat[1].Variants) != 1 {
		t.Fatalf("an inactive variation in the catalog %+v", cat[1].Variants)
	}
	if cat, _ = s.Catalog(ctx, "", "", "", true); len(cat[1].Variants) != 2 {
		t.Fatalf("all: %+v", cat[1].Variants)
	}

	// the variation goes to a warehouse and a deal
	wh, err := s.SaveWarehouse(ctx, Warehouse{Name: "Основной"})
	must(err)
	arr, err := s.SaveStockDoc(ctx, anna, StockDoc{Kind: "arrival", Rows: []StockRow{{ProductID: red, Quantity: 5, Price: 400}}})
	must(err)
	must(s.ConductStockDoc(ctx, anna, arr))
	on, err := s.StockTotals(ctx)
	must(err)
	if on[red] != 5 || on[shirt] != 0 {
		t.Fatalf("stock %v", on)
	}
	stock, err := s.Stock(ctx, "Футболка", "", false, true)
	must(err)
	if len(stock) != 1 || stock[0].Product.ID != red || stock[0].On[wh] != 5 {
		t.Fatalf("stock lines %+v", stock)
	}
	pipes, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Мерч", PipelineID: pipes[0].ID})
	must(err)
	total, err := s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{ProductID: red, Quantity: 2, Reserve: -1, WarehouseID: wh}})
	must(err)
	rows, err := s.ProductRows(ctx, "deal", deal)
	must(err)
	if total != 1900 || len(rows) != 1 || rows[0].Name != "Футболка (Красный, M)" || rows[0].Reserve != 2 {
		t.Fatalf("deal rows %v %+v", total, rows)
	}

	// the product goes with its variations, the deal keeps its line
	must(s.DeleteProduct(ctx, shirt))
	if _, err := s.Product(ctx, red); err != ErrNotFound {
		t.Fatalf("the variation stayed: %v", err)
	}
	rows, err = s.ProductRows(ctx, "deal", deal)
	must(err)
	if len(rows) != 1 || rows[0].ProductID != "" || rows[0].Name != "Футболка (Красный, M)" {
		t.Fatalf("deal rows after delete %+v", rows)
	}
	_ = plain
}
