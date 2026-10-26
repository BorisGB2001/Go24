package crm

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestProductCSV: the catalog goes out as a table and comes back in — a row finds its product by SKU or name and
// changes only its columns, makes a new one with its section path and properties, a variation by its product's SKU;
// update false skips the found ones; a bad row is an error of its own, the others go in.
func TestProductCSV(t *testing.T) {
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
	color, err := s.SaveProp(ctx, Prop{Name: "Цвет", Kind: PropList, Options: []string{"белый", "чёрный"}, Variant: true})
	must(err)
	sec, err := s.SaveSection(ctx, Section{Name: "Оборудование"})
	must(err)
	vat20 := 20.0
	printer, err := s.SaveProduct(ctx, anna, Product{Name: "Принтер", SKU: "P-1", Price: 15000.5, VATRate: &vat20, VATIncluded: true,
		Active: true, SectionID: sec, Barcodes: []string{"4600001"}})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{ParentID: printer, SKU: "P-1-W", Price: 15500, Props: map[string]string{color: "белый"}, Active: true})
	must(err)

	names := func(key string) []string {
		return map[string][]string{"crm.pcsv.name": {"Название", "Name"}, "crm.pcsv.sku": {"Артикул", "SKU"}, "crm.pcsv.section": {"Раздел"},
			"crm.pcsv.parent": {"Товар вариации"}, "crm.pcsv.price": {"Цена", "Price"}, "crm.pcsv.vat": {"Ставка НДС"},
			"crm.pcsv.active": {"Активен"}, "crm.pcsv.stock": {"Остаток"}, "crm.pcsv.yes": {"Да", "Yes"}, "crm.pcsv.kind": {"Тип"},
			"crm.kind.product": {"Товар", "Product"}, "crm.kind.service": {"Услуга", "Service"}}[key]
	}
	tr := func(key string) string { return names(key)[0] }

	fields, props, rows, err := s.ExportProducts(ctx, "", "", false, tr)
	must(err)
	if slices.Contains(fields, "purchase_price") || len(props) != 1 || len(rows) != 2 {
		t.Fatalf("export: %v %v %v", fields, props, rows)
	}
	col := func(row []string, f string) string { return row[slices.Index(fields, f)] }
	if r := rows[0]; col(r, "name") != "Принтер" || col(r, "price") != "15000,5" || col(r, "section") != "Оборудование" ||
		col(r, "vat") != "20" || col(r, "vat_included") != "Да" || col(r, "barcodes") != "4600001" || col(r, "parent") != "" || col(r, "kind") != "Товар" {
		t.Fatalf("product row %v", r)
	}
	if r := rows[1]; col(r, "name") != "Принтер (белый)" || col(r, "parent") != "P-1" || r[len(fields)] != "белый" {
		t.Fatalf("variation row %v", r)
	}
	if f, _, _, _ := s.ExportProducts(ctx, "", "", true, tr); !slices.Contains(f, "purchase_price") {
		t.Fatal("no purchase price for an administrator")
	}

	table := [][]string{
		{"Артикул", "name", "Цена", "Раздел", "Товар вариации", "Цвет", "Остаток", "Непонятно"},
		{"P-1", "Принтер лазерный", "16 000,00", "", "", "", "99", "x"},     // found by SKU: renamed, new price, to the root
		{"S-1", "Сканер", "9000", "Оборудование / Сканеры", "", "", "", ""}, // new, with a new subsection
		{"P-1-B", "", "15600", "", "P-1", "чёрный", "", ""},                 // a new variation, named by its color
		{"", "Бумага", "abc", "", "", "", "", ""},                           // a bad price
		{"", "", "", "", "", "", "", ""},                                    // empty: skipped quietly
		{"X-1", "Картридж", "100", "", "нет такого", "", "", ""},            // no such product
	}
	res, err := s.ImportProducts(ctx, anna, table, true, names)
	must(err)
	if res.Created != 2 || res.Updated != 1 || len(res.Errors) != 2 || res.Errors[0].Row != 5 || res.Errors[1].Row != 7 ||
		res.Errors[1].Err.Key != "err.crm_pcsv_parent" {
		t.Fatalf("import: %+v", res)
	}
	p, err := s.Product(ctx, printer)
	must(err)
	if p.Name != "Принтер лазерный" || p.Price != 16000 || p.SectionID != "" || p.VATRate == nil || *p.VATRate != 20 ||
		!p.Active || len(p.Barcodes) != 1 {
		t.Fatalf("updated printer %+v", p) // the columns not in the table are kept
	}
	vars, err := s.Variants(ctx, printer)
	must(err)
	if len(vars) != 2 || !slices.ContainsFunc(vars, func(v Product) bool {
		return v.Name == "Принтер лазерный (чёрный)" && v.Props[color] == "чёрный"
	}) {
		t.Fatalf("variations %+v", vars)
	}
	scanners, err := s.ProductsOf(ctx, "S-1", "", true)
	must(err)
	if len(scanners) != 1 || scanners[0].SectionPath != "Оборудование / Сканеры" || !scanners[0].Active || scanners[0].Price != 9000 {
		t.Fatalf("scanner %+v", scanners)
	}

	// found by name (no SKU), and update false skips it; the same section path is not made twice
	res, err = s.ImportProducts(ctx, anna, [][]string{{"Название", "Раздел", "Активен"}, {"сканер", "оборудование / сканеры", "нет"},
		{"Сканер 2", "Оборудование/Сканеры", "Да"}}, false, names)
	must(err)
	if res.Created != 1 || res.Skipped != 1 {
		t.Fatalf("no update: %+v", res)
	}
	secs, err := s.Sections(ctx)
	must(err)
	if len(secs) != 2 {
		t.Fatalf("sections %+v", secs)
	}
	res, err = s.ImportProducts(ctx, anna, [][]string{{"Name", "Активен"}, {"Сканер", "нет"}}, true, names)
	must(err)
	if scanners, _ = s.ProductsOf(ctx, "S-1", "", true); res.Updated != 1 || scanners[0].Active {
		t.Fatalf("deactivate by name: %+v %+v", res, scanners)
	}
	// «Тип»: a service by its name in any language, a wrong one is the row's error, none — as it was
	res, err = s.ImportProducts(ctx, anna, [][]string{{"Название", "Тип"}, {"Монтаж", "service"}, {"Сканер", "Вещь"}, {"Сканер 2", ""}}, true, names)
	must(err)
	if res.Created != 1 || res.Updated != 1 || len(res.Errors) != 1 || res.Errors[0].Err.Key != "err.crm_product_kind" {
		t.Fatalf("kinds: %+v", res)
	}
	if svc, _ := s.Catalog(ctx, "", "", KindService, true); len(svc) != 1 || svc[0].Name != "Монтаж" {
		t.Fatalf("services %+v", svc)
	}
	if _, err := s.ImportProducts(ctx, anna, [][]string{{"Цена"}, {"1"}}, true, names); err == nil {
		t.Fatal("a table without a name, SKU or ID")
	}
}
