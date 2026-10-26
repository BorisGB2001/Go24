package bp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestScopes: the variables and the constants of the designer — not asked at the start, a variable changed by
// a step, a constant the same for every process and not changed.
func TestScopes(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, ann := newUser("boss"), newUser("ann")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))

	var ve *i18n.Error
	fields := []Field{{Key: "sum", Label: "Сумма", Kind: "number", Required: true},
		{Key: "status", Label: "Статус", Kind: "string", Default: "новая", Scope: "var", Required: true},
		{Key: "acc", Label: "Бухгалтер", Kind: "user", Default: boss, Scope: "const"},
		{Key: "limit", Label: "Лимит", Kind: "number", Default: "1000", Scope: "const"}}
	for _, bad := range []Template{
		{Name: "Плохой", Fields: []Field{{Key: "x", Label: "X", Kind: "string", Scope: "global"}}, Steps: []Step{{Kind: "notify", To: "starter", Text: "x"}}},
		{Name: "Плохой", Steps: []Step{{Kind: "request", To: "starter", Text: "x", Fields: []Field{{Key: "x", Label: "X", Kind: "string", Scope: "var"}}}}},
		{Name: "Плохой", Fields: fields, Steps: []Step{{Kind: "set", Field: "limit", Value: "1"}}}, // a constant is not changed
	} {
		if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
			t.Fatalf("saved: %+v %v", bad, err)
		}
	}
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Оплата", Active: true, Fields: fields, Steps: []Step{
		{Kind: "set", Field: "status", Value: "проверка {sum}"},
		{Kind: "approve", To: FieldPrefix + "acc", Text: "{sum} при лимите {limit}: {status}"}}})
	must(err)
	if tp, _ := s.Template(ctx, tpl); tp.Fields[1].Required || tp.Fields[0].Asked() == tp.Fields[1].Asked() {
		t.Fatalf("a variable is required or asked: %+v", tp.Fields)
	}
	// what is sent for a variable or a constant does not count
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"sum": "5000", "status": "одобрено", "limit": "1", "acc": ann})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Values["status"] != "проверка 5000" || x.Values["limit"] != "1000" || x.Values["acc"] != boss {
		t.Fatalf("values %+v", x.Values)
	}
	mine, _ := s.MyApprovals(ctx, boss)
	if len(mine) != 1 || mine[0].Text != "5000 при лимите 1000: проверка 5000" {
		t.Fatalf("the accountant's approvals = %+v", mine)
	}
}
