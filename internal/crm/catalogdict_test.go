package crm

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCatalogDicts: the built-in units (one default; a new default takes it over; the default can't be deleted; a
// unit without a code needs its name and symbol); properties of each kind check their values, unknown ones are
// dropped, a deleted property leaves the products; barcodes are a product's own, found by the catalog's search and
// by ProductByBarcode, and replaced with the product's form.
func TestCatalogDicts(t *testing.T) {
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

	units, err := s.Units(ctx)
	must(err)
	if len(units) != 10 || units[0].Code != "796" || !units[0].Default {
		t.Fatalf("built-in units %+v", units)
	}
	if _, err := s.SaveUnit(ctx, Unit{Name: "Рулон"}); err == nil {
		t.Fatal("a unit without a code and a symbol")
	}
	roll, err := s.SaveUnit(ctx, Unit{Name: "Рулон", Symbol: "рул.", Default: true})
	must(err)
	units, err = s.Units(ctx)
	must(err)
	defaults := 0
	for _, u := range units {
		if u.Default {
			defaults++
			if u.ID != roll {
				t.Fatalf("the default is %+v", u)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("%d defaults", defaults)
	}
	if err := s.DeleteUnit(ctx, roll); err == nil {
		t.Fatal("the default unit deleted")
	}
	if units[len(units)-1].ID != roll {
		t.Fatal("a new unit isn't last")
	}
	must(s.DeleteUnit(ctx, units[0].ID))

	if _, err := s.SaveProp(ctx, Prop{Name: "Цвет", Kind: PropList}); err == nil {
		t.Fatal("a list without values")
	}
	if _, err := s.SaveProp(ctx, Prop{Name: "x", Kind: "date"}); err == nil {
		t.Fatal("an unknown kind")
	}
	color, err := s.SaveProp(ctx, Prop{Name: "Цвет", Kind: PropList, Options: []string{" красный", "синий", "", "красный"}, InList: true})
	must(err)
	weight, err := s.SaveProp(ctx, Prop{Name: "Вес", Kind: PropNumber})
	must(err)
	gift, err := s.SaveProp(ctx, Prop{Name: "Подарочная упаковка", Kind: PropBool})
	must(err)
	note, err := s.SaveProp(ctx, Prop{Name: "Примечание", Kind: PropString})
	must(err)
	props, err := s.ProductProps(ctx)
	must(err)
	if len(props) != 4 || !slices.Equal(props[3].Options, []string{"красный", "синий"}) {
		t.Fatalf("props %+v", props)
	}

	if _, err := s.SaveProduct(ctx, anna, Product{Name: "Кружка", Props: map[string]string{color: "зелёный"}}); err == nil {
		t.Fatal("a value not of the list")
	}
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "Кружка", Props: map[string]string{weight: "тяжёлая"}}); err == nil {
		t.Fatal("a number that isn't")
	}
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "Кружка", Barcodes: []string{"46 00"}}); err == nil {
		t.Fatal("a barcode with a space")
	}
	mug, err := s.SaveProduct(ctx, anna, Product{Name: "Кружка", Unit: "шт.", Props: map[string]string{color: "синий", weight: "0,35",
		gift: "1", note: " ", "00000000-0000-0000-0000-000000000000": "x"}, Barcodes: []string{"4600000000017", " 4600000000024", "4600000000017"}})
	must(err)
	p, err := s.Product(ctx, mug)
	must(err)
	if len(p.Props) != 3 || p.Props[color] != "синий" || p.Props[weight] != "0.35" || p.Props[gift] != "1" ||
		!slices.Equal(p.Barcodes, []string{"4600000000017", "4600000000024"}) {
		t.Fatalf("saved %+v %v", p.Props, p.Barcodes)
	}
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "Тарелка", Barcodes: []string{"4600000000024"}}); err == nil {
		t.Fatal("another product's barcode")
	}
	if list, _ := s.Products(ctx, "", true); len(list) != 1 {
		t.Fatalf("the refused product was saved: %d", len(list))
	}
	plate, err := s.SaveProduct(ctx, anna, Product{Name: "Тарелка", Barcodes: []string{"4600000000031"}})
	must(err)
	if list, _ := s.Products(ctx, "4600000000024", false); len(list) != 1 || list[0].ID != mug {
		t.Fatalf("search by barcode %+v", list)
	}
	if x, err := s.ProductByBarcode(ctx, "4600000000031"); err != nil || x.ID != plate {
		t.Fatalf("by barcode %v %v", x.ID, err)
	}
	if _, err := s.ProductByBarcode(ctx, "1"); err != ErrNotFound {
		t.Fatalf("unknown barcode: %v", err)
	}
	p.Barcodes, p.Active = []string{"4600000000024"}, true
	_, err = s.SaveProduct(ctx, anna, p)
	must(err)
	if _, err := s.ProductByBarcode(ctx, "4600000000017"); err != ErrNotFound {
		t.Fatal("the removed barcode is still found")
	}

	must(s.DeleteProp(ctx, color))
	p, err = s.Product(ctx, mug)
	must(err)
	if _, ok := p.Props[color]; ok || len(p.Props) != 2 {
		t.Fatalf("after the property's deletion %+v", p.Props)
	}
	must(s.DeleteProduct(ctx, mug))
	if _, err := s.ProductByBarcode(ctx, "4600000000024"); err != ErrNotFound {
		t.Fatal("a deleted product's barcode")
	}
}
