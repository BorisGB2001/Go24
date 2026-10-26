package crm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestPhoneKey(t *testing.T) {
	for in, want := range map[string]string{"+7 (900) 123-45-67": "9001234567", "89001234567": "9001234567", "12-34": "", "": ""} {
		if got := phoneKey(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestDuplicates: cards with the same phone (any format), e-mail or name are found; two contacts merge with their
// deals and timeline; a CSV is read (any separator, a BOM) and imported, duplicates skipped, a contact's company made.
func TestDuplicates(t *testing.T) {
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
	ivan2, err := s.SaveContact(ctx, anna, Contact{Name: "Иван", Phone: "89001234567", Email: "ivan@x.ru", Position: "Директор"})
	must(err)
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Email: "IVAN@x.ru"})
	must(err)
	dups, err := s.Duplicates(ctx, "contact", ivan2, "Иван", "89001234567", "ivan@x.ru")
	must(err)
	if len(dups) != 2 || dups[0].ID != ivan || dups[0].By != "phone" || dups[1].ID != lead || dups[1].By != "email" {
		t.Fatalf("duplicates: %+v", dups)
	}
	pipes, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Contact: Ref{ID: ivan2}})
	must(err)
	must(s.Comment(ctx, anna, "contact", ivan2, "звонил"))

	if err := s.Merge(ctx, anna, "contact", ivan, ivan); err == nil {
		t.Fatal("merged a card into itself")
	}
	must(s.Merge(ctx, anna, "contact", ivan, ivan2))
	x, err := s.Contact(ctx, ivan)
	must(err)
	if x.Email != "ivan@x.ru" || x.Position != "Директор" || x.Phone != "+7 (900) 123-45-67" {
		t.Fatalf("merged contact: %+v", x)
	}
	if d, _ := s.Deal(ctx, deal); d.Contact.ID != ivan {
		t.Fatalf("the deal's contact: %+v", d.Contact)
	}
	tl, err := s.Timeline(ctx, "contact", ivan)
	must(err)
	if tl[0].Kind != "merged" || tl[0].Body != "Иван" || len(tl) < 3 {
		t.Fatalf("timeline: %+v", tl)
	}
	if _, err := s.Contact(ctx, ivan2); err == nil {
		t.Fatal("the merged contact is still there")
	}

	table, err := ReadCSV(strings.NewReader("\ufeffИмя,Телефон,E-mail,Компания,Должность\n" +
		"Мария,+7 911 000-00-01,maria@x.ru,ООО Ромашка,Бухгалтер\n" +
		"Иван дубль,8 900 123 45 67,,,\n" +
		",,,ООО Ромашка,\n" +
		"Олег,,,ооо ромашка,\n"))
	must(err)
	if len(table) != 5 || table[1][3] != "ООО Ромашка" {
		t.Fatalf("csv: %q", table)
	}
	res, err := s.Import(ctx, anna, "contact", table, true, nil)
	must(err)
	if res.Created != 2 || res.Skipped != 1 || len(res.Errors) != 1 || res.Errors[0].Row != 4 || res.Errors[0].Err.Key != "err.crm_name" {
		t.Fatalf("import: %+v", res)
	}
	var companies int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_companies WHERE title = 'ООО Ромашка'`).Scan(&companies))
	if companies != 1 {
		t.Fatalf("%d companies «ООО Ромашка»", companies)
	}
	if _, err := s.Import(ctx, anna, "lead", [][]string{{"x", "y"}, {"1", "2"}}, false, nil); err == nil {
		t.Fatal("imported without known columns")
	}
	semi, err := ReadCSV(strings.NewReader("Название;Сумма;Источник\nЗаявка с выставки;1 500,50;Выставка\n"))
	must(err)
	res, err = s.Import(ctx, anna, "lead", semi, false, nil)
	must(err)
	leads, _ := s.LeadList(ctx, "", Filter{Q: "выставки"})
	if res.Created != 1 || len(leads) != 1 || leads[0].Amount != 1500.5 || leads[0].Source != "Выставка" {
		t.Fatalf("leads: %+v %+v", res, leads)
	}
}
