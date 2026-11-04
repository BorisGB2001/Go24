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

// TestMultipleValues: the values of a multiple field are kept one a line, each checked, the empty and the repeated
// left out; people read them parted by commas.
func TestMultipleValues(t *testing.T) {
	f := Field{Key: "n", Label: "N", Kind: "number", Multiple: true}
	if v, err := cleanValue(f, " 1\n\n2,5\n1 "); err != nil || v != "1\n2,5" {
		t.Fatalf("clean: %q %v", v, err)
	}
	if _, err := cleanValue(f, "1\nx"); err == nil {
		t.Fatal("a bad value passed")
	}
	if got := f.Shown("1\n2"); got != "1, 2" {
		t.Fatalf("shown: %q", got)
	}
	if got := (Field{Kind: "string"}).List("a\nb"); len(got) != 1 {
		t.Fatalf("a single field's list: %q", got)
	}
	if !(Field{Kind: "select", Multiple: true, Default: "a\nb"}).Picked("b") {
		t.Fatal("not picked")
	}
	for _, kind := range []string{"text", "bool"} {
		if checkField(Field{Key: "x", Label: "X", Kind: kind, Multiple: true}) == nil {
			t.Fatalf("a multiple %s", kind)
		}
	}
}

// TestForeach: «Итератор» puts each value of a multiple field into another and does its steps till the «Перейти к
// шагу» back to it, then goes past it; an approval to a multiple user field goes to everyone of it.
func TestForeach(t *testing.T) {
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
	boss, ann, bob := newUser("boss"), newUser("ann"), newUser("bob")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	var ve *i18n.Error
	is := func(err error, key string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}

	fields := []Field{{Key: "people", Label: "Кто", Kind: "user", Multiple: true}, {Key: "one", Label: "Один", Kind: "user"},
		{Key: "who", Label: "Текущий", Kind: "user", Scope: "var"}, {Key: "n", Label: "N", Kind: "number", Default: "0", Scope: "var"},
		{Key: "names", Label: "Имена", Kind: "string", Scope: "var"}}
	for _, steps := range [][]Step{
		{{Kind: "foreach", Field: "one", Item: "who"}, {Kind: "goto", Goto: 1}},                                           // not multiple
		{{Kind: "foreach", Field: "people", Item: "n"}, {Kind: "goto", Goto: 1}},                                          // another kind
		{{Kind: "foreach", Field: "people", Item: "who"}, {Kind: "set", Field: "n", Value: "1"}},                          // no way back
		{{Kind: "set", Field: "n", Value: "1"}, {Kind: "foreach", Field: "people", Item: "who"}, {Kind: "goto", Goto: 1}}, // back past it
	} {
		_, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: fields, Steps: steps})
		if err == nil {
			t.Fatalf("saved: %+v", steps)
		}
	}
	_, err = s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: fields, Steps: []Step{{Kind: "foreach", Field: "one", Item: "who"}, {Kind: "goto", Goto: 1}}})
	is(err, "err.bp_foreach")

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Обход", Active: true, Fields: fields, Steps: []Step{
		{Kind: "foreach", Field: "people", Item: "who"},
		{Kind: "set", Field: "n", Value: "= {n} + 1"},
		{Kind: "set", Field: "names", Value: "{names}[{who}]"},
		{Kind: "goto", Goto: 1},
		{Kind: "review", To: "field:people", Text: "Итог: {people}"}}})
	must(err)
	id, err := s.Start(ctx, boss, tpl, "", map[string]string{"people": ann + "\n" + bob + "\n" + ann})
	must(err)
	x, err := s.Instance(ctx, boss, id)
	must(err)
	if x.Status != "running" || x.Values["n"] != "2" || x.Values["who"] != bob || x.Values["names"] != "["+ann+"]["+bob+"]" {
		t.Fatalf("the iterator: %s %+v", x.Status, x.Values)
	}
	for _, u := range []string{ann, bob} { // everyone of the multiple field is asked, the values shown by names
		a, _ := s.MyApprovals(ctx, u)
		if len(a) != 1 || a[0].Text != "Итог: ann, bob" {
			t.Fatalf("%s: %+v", u, a)
		}
		must(s.Decide(ctx, u, a[0].ID, "seen", ""))
	}
	if x, _ := s.Instance(ctx, boss, id); x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}
	// none — past the steps; a variable set by names, «;» parting them
	tpl, err = s.SaveTemplate(ctx, boss, Template{Name: "Пусто", Active: true, Fields: fields, Steps: []Step{
		{Kind: "foreach", Field: "people", Item: "who"}, {Kind: "set", Field: "n", Value: "= {n} + 1"}, {Kind: "goto", Goto: 1},
		{Kind: "set", Field: "people", Value: "ann; bob@t"}}})
	must(err)
	id, err = s.Start(ctx, boss, tpl, "", nil)
	must(err)
	if x, _ := s.Instance(ctx, boss, id); x.Status != "done" || x.Values["n"] != "0" || x.Values["people"] != ann+"\n"+bob {
		t.Fatalf("no values: %s %+v", x.Status, x.Values)
	}
}
