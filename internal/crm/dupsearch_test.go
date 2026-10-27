package crm

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestDuplicateGroups: «Поиск дубликатов» groups the cards by phone (any format), name and ИНН; «Не дубликаты» hides
// a pair, and a group left with one card is gone; merging a group leaves its main card only.
func TestDuplicateGroups(t *testing.T) {
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

	ivan, err := s.SaveContact(ctx, anna, Contact{Name: "Иван Петров", Phone: "+7 (900) 123-45-67"})
	must(err)
	ivan2, err := s.SaveContact(ctx, anna, Contact{Name: "Иван", Phone: "89001234567", Email: "ivan@x.ru"})
	must(err)
	ivan3, err := s.SaveContact(ctx, anna, Contact{Name: "иван ", Phone: "8 900 123 45 67"})
	must(err)
	_, err = s.SaveContact(ctx, anna, Contact{Name: "Пётр", Phone: "+7 911 000-00-00"})
	must(err)

	groups, total, err := s.DuplicateGroups(ctx, "", "contact", "phone")
	must(err)
	if total != 1 || len(groups[0].Cards) != 3 || groups[0].Cards[0].ID != ivan || groups[0].Cards[0].Title != "Иван Петров" {
		t.Fatalf("by phone: %d %+v", total, groups)
	}
	groups, _, err = s.DuplicateGroups(ctx, anna, "contact", "name")
	must(err)
	if len(groups) != 1 || len(groups[0].Cards) != 2 || groups[0].Cards[0].ID != ivan2 || groups[0].Cards[1].ID != ivan3 {
		t.Fatalf("by name: %+v", groups)
	}
	if _, _, err := s.DuplicateGroups(ctx, "", "lead", "inn"); err == nil {
		t.Fatal("leads have no ИНН")
	}

	// «Не дубликаты»: the pair is hidden, the third card keeps the group
	must(s.NotDuplicates(ctx, anna, "contact", []string{ivan3, ivan2}))
	must(s.NotDuplicates(ctx, anna, "contact", []string{ivan2, ivan3})) // again — nothing new
	groups, _, err = s.DuplicateGroups(ctx, "", "contact", "name")
	must(err)
	if len(groups) != 0 {
		t.Fatalf("name pair skipped: %+v", groups)
	}
	groups, _, err = s.DuplicateGroups(ctx, "", "contact", "phone")
	must(err)
	if len(groups) != 1 || len(groups[0].Cards) != 3 { // ivan joins both
		t.Fatalf("phone after skip: %+v", groups)
	}

	// companies by the ИНН of their requisites
	a, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	b, err := s.SaveCompany(ctx, anna, Company{Title: "ООО Ромашка"})
	must(err)
	for _, id := range []string{a, b} {
		_, err = s.SaveRequisite(ctx, Requisite{Entity: "company", EntityID: id, Preset: "org", Name: "ООО", INN: "7707083893"})
		must(err)
	}
	groups, _, err = s.DuplicateGroups(ctx, "", "company", "inn")
	must(err)
	if len(groups) != 1 || groups[0].Value != "7707083893" || len(groups[0].Cards) != 2 {
		t.Fatalf("by ИНН: %+v", groups)
	}
	must(s.Merge(ctx, anna, "company", a, b))
	groups, _, err = s.DuplicateGroups(ctx, "", "company", "inn")
	must(err)
	if len(groups) != 0 {
		t.Fatalf("after merge: %+v", groups)
	}
}
