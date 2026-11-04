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

// TestGlobals: «Глобальные константы и переменные» of the designer — read by every template as {global.KEY},
// a user constant the person of a step, a variable changed by a step for all processes, a constant not changed.
func TestGlobals(t *testing.T) {
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
	for _, bad := range [][]Field{
		{{Key: "a b", Label: "A", Kind: "string", Scope: "const"}},
		{{Key: "a", Label: "A", Kind: "string", Scope: "param"}},
		{{Key: "a", Label: "A", Kind: "string", Scope: "var"}, {Key: "A", Label: "B", Kind: "string", Scope: "var"}},
		{{Key: "n", Label: "N", Kind: "number", Scope: "var", Default: "много"}},
	} {
		if err := s.SaveGlobals(ctx, boss, bad); !errors.As(err, &ve) {
			t.Fatalf("saved %+v: %v", bad, err)
		}
	}
	globals := []Field{{Key: "acc", Label: "Главный бухгалтер", Kind: "user", Default: boss, Scope: "const"},
		{Key: "limit", Label: "Лимит", Kind: "number", Default: "1000", Scope: "const"},
		{Key: "counter", Label: "Номер заявки", Kind: "number", Default: "0", Scope: "var"},
		{Key: "spare", Label: "Лишняя", Kind: "string", Scope: "var"}}
	must(s.SaveGlobals(ctx, boss, globals))

	form := []Field{{Key: "sum", Label: "Сумма", Kind: "number", Required: true}}
	for _, bad := range []Template{
		{Name: "Плохой", Fields: form, Steps: []Step{{Kind: "set", Field: GlobalPrefix + "limit", Value: "1"}}},   // a constant is not changed
		{Name: "Плохой", Fields: form, Steps: []Step{{Kind: "set", Field: GlobalPrefix + "nothing", Value: "1"}}}, // no such global
		{Name: "Плохой", Fields: []Field{{Key: GlobalPrefix + "x", Label: "X", Kind: "string"}}, Steps: []Step{{Kind: "notify", To: "starter", Text: "x"}}},
		{Name: "Плохой", Fields: form, Steps: []Step{{Kind: "approve", To: FieldPrefix + GlobalPrefix + "limit", Text: "x"}}}, // not a user
	} {
		if _, err := s.SaveTemplate(ctx, boss, bad); !errors.As(err, &ve) {
			t.Fatalf("saved: %+v %v", bad, err)
		}
	}
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Оплата", Active: true, Fields: form, Steps: []Step{
		{Kind: "set", Field: GlobalPrefix + "counter", Value: "= {global.counter} + 1"},
		{Kind: "if", Field: "sum", Op: "gt", Value: "{global.limit}", Goto: 4},
		{Kind: "approve", To: FieldPrefix + GlobalPrefix + "acc", Text: "№{global.counter}: {sum} сверх {global.limit}, бухгалтер {global.acc}"},
		{Kind: "notify", To: "starter", Text: "№{global.counter}"}}})
	must(err)

	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"sum": "5000", GlobalPrefix + "counter": "77"})
	must(err)
	mine, _ := s.MyApprovals(ctx, boss)
	if len(mine) != 1 || mine[0].InstanceID != id || mine[0].Text != "№1: 5000 сверх 1000, бухгалтер boss" {
		t.Fatalf("the accountant's approvals = %+v", mine)
	}
	if x, _ := s.Instance(ctx, ann, id); len(x.Values) != 1 || x.Values["sum"] != "5000" { // the globals are not the process's
		t.Fatalf("values %+v", x.Values)
	}
	_, err = s.Start(ctx, ann, tpl, "", map[string]string{"sum": "10"}) // under the limit: no approval, the counter goes on
	must(err)
	got, err := s.Globals(ctx)
	must(err)
	if len(got) != 4 || got[2].Key != "counter" || got[2].Default != "2" || got[0].Default != boss {
		t.Fatalf("globals %+v", got)
	}

	// a global a template uses stays; nor is a variable a step changes made a constant
	if err := s.SaveGlobals(ctx, boss, globals[1:]); !errors.As(err, &ve) || ve.Key != "err.bp_global_used" {
		t.Fatalf("removed a used global: %v", err)
	}
	asConst := append([]Field{}, globals...)
	asConst[2].Scope = "const"
	if err := s.SaveGlobals(ctx, boss, asConst); !errors.As(err, &ve) || ve.Key != "err.bp_global_used" {
		t.Fatalf("made a changed variable a constant: %v", err)
	}
	must(s.SaveGlobals(ctx, boss, globals[:3])) // the unused one goes
	if got, _ := s.Globals(ctx); len(got) != 3 || got[2].Default != "0" {
		t.Fatalf("globals %+v", got)
	}
}
