package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSections: the catalog's tree of sections in its order with paths; a section's products include its
// subsections'; a section can't go into its own subsection; deleting one moves its products and subsections to its
// parent; the picker lists the root's products first, then by sections; a product's picture and purchase price.
func TestSections(t *testing.T) {
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

	if _, err := s.SaveSection(ctx, Section{Name: " "}); err == nil {
		t.Fatal("a section without a name")
	}
	if _, err := s.SaveSection(ctx, Section{Name: "x", ParentID: "00000000-0000-0000-0000-000000000000"}); err != ErrNotFound {
		t.Fatalf("a missing parent: %v", err)
	}
	hw, err := s.SaveSection(ctx, Section{Name: "Оборудование"})
	must(err)
	soft, err := s.SaveSection(ctx, Section{Name: "Программы", Sort: -1})
	must(err)
	pr, err := s.SaveSection(ctx, Section{Name: "Принтеры", ParentID: hw})
	must(err)
	laser, err := s.SaveSection(ctx, Section{Name: "Лазерные", ParentID: pr})
	must(err)
	list, err := s.Sections(ctx)
	must(err)
	var got []string
	for _, x := range list {
		got = append(got, x.Path)
	}
	if len(got) != 4 || got[0] != "Программы" || got[1] != "Оборудование" || got[3] != "Оборудование / Принтеры / Лазерные" || list[3].Depth != 2 {
		t.Fatalf("tree %q", got)
	}
	if _, err := s.SaveSection(ctx, Section{ID: hw, Name: "Оборудование", ParentID: laser}); err == nil {
		t.Fatal("a section into its own subsection")
	}
	if _, err := s.SaveSection(ctx, Section{ID: hw, Name: "Оборудование", ParentID: hw}); err == nil {
		t.Fatal("a section into itself")
	}

	buy := 700.0
	p1, err := s.SaveProduct(ctx, anna, Product{Name: "Принтер HP", Price: 1000, SectionID: laser, PurchasePrice: &buy})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{Name: "Сканер", Price: 500, SectionID: hw})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{Name: "Антивирус", Price: 300, SectionID: soft})
	must(err)
	_, err = s.SaveProduct(ctx, anna, Product{Name: "Доставка", Price: 100})
	must(err)
	if _, err := s.SaveProduct(ctx, anna, Product{Name: "x", SectionID: "nope"}); err != ErrNotFound {
		t.Fatalf("a product in a missing section: %v", err)
	}
	in, err := s.ProductsOf(ctx, "", hw, false)
	must(err)
	if len(in) != 2 || in[0].Name != "Принтер HP" || in[0].SectionPath != "Оборудование / Принтеры / Лазерные" || *in[0].PurchasePrice != 700 {
		t.Fatalf("hardware %+v", in)
	}
	if in, _ := s.ProductsOf(ctx, "", "", false); len(in) != 4 {
		t.Fatalf("all %d", len(in))
	}
	pick, err := s.ProductsIn(ctx, "RUB")
	must(err)
	if pick[0].Name != "Доставка" || pick[1].SectionPath != "Оборудование" || pick[3].SectionPath != "Программы" {
		t.Fatalf("picker %v %v %v", pick[0].Name, pick[1].SectionPath, pick[3].SectionPath)
	}

	old, err := s.SetProductPhoto(ctx, p1, "go24/crm/products/1/a.png", "image/png")
	must(err)
	if old != "" {
		t.Fatalf("old photo %q", old)
	}
	if old, _ = s.SetProductPhoto(ctx, p1, "", ""); old != "go24/crm/products/1/a.png" {
		t.Fatalf("old photo %q", old)
	}
	if _, err := s.SetProductPhoto(ctx, "00000000-0000-0000-0000-000000000000", "k", "image/png"); err != ErrNotFound {
		t.Fatalf("photo of a missing product: %v", err)
	}

	must(s.DeleteSection(ctx, pr)) // «Лазерные» and its printer go up to «Оборудование»
	list, err = s.Sections(ctx)
	must(err)
	if len(list) != 3 || list[2].Path != "Оборудование / Лазерные" {
		t.Fatalf("after delete %+v", list)
	}
	p, err := s.Product(ctx, p1)
	must(err)
	if p.SectionID != laser {
		t.Fatalf("the printer's section %q", p.SectionID)
	}
	must(s.DeleteSection(ctx, laser))
	if p, _ = s.Product(ctx, p1); p.SectionID != hw {
		t.Fatalf("the printer's section %q, want %q", p.SectionID, hw)
	}
	if err := s.DeleteSection(ctx, laser); err != ErrNotFound {
		t.Fatalf("delete twice: %v", err)
	}
}
