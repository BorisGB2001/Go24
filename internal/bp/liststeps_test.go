package bp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestListSteps: the steps «Списки» of any template — an element added (named, {created_list_element}), changed and
// read into the fields el_<…> (a condition and a text use them) and deleted by its link, as the starter and without the list's
// rights; a list step needs its list, a new element its name, the fields — the list's; a file of the template keeps
// the list by its name.
func TestListSteps(t *testing.T) {
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
	var ann, boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@t') RETURNING id`).Scan(&boss))
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	admin := tasks.WithAdmin(ctx)
	list, err := s.SaveList(admin, boss, List{Name: "Заявки", Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number", Required: true},
		{Key: "who", Label: "Получатель", Kind: "user"}, {Key: "paid", Label: "Оплачено", Kind: "bool"}}})
	must(err)
	must(s.SetListRights(admin, list, "write", []string{boss})) // ann may not change it herself

	var ve *i18n.Error
	for name, steps := range map[string][]Step{
		"no list":        {{Kind: "listadd", Sets: []Assign{{Field: "name", Value: "x"}}}},
		"no name":        {{Kind: "listadd", List: list, Sets: []Assign{{Field: "sum", Value: "1"}}}},
		"no such field":  {{Kind: "listadd", List: list, Sets: []Assign{{Field: "name", Value: "x"}, {Field: "color", Value: "1"}}}},
		"no sets":        {{Kind: "listupdate", List: list}},
		"read after if":  {{Kind: "if", Field: "el_sum", Op: "filled"}, {Kind: "listread", List: list}},
		"list elsewhere": {{Kind: "log", Text: "x", List: list}},
	} {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: steps}); !errors.As(err, &ve) {
			t.Fatalf("%s: saved (%v)", name, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Заявка", Active: true,
		Fields: []Field{{Key: "amount", Label: "Сумма", Kind: "number", Required: true}},
		Steps: []Step{{Kind: "listadd", List: list, Sets: []Assign{{Field: "name", Value: "Заявка {starter}"}, {Field: "Сумма", Value: "{amount}"},
			{Field: "who", Value: "starter"}}},
			{Kind: "listupdate", List: list, Sets: []Assign{{Field: "paid", Value: "Y"}}},
			{Kind: "listread", List: list, Value: "{created_list_element}"},
			{Kind: "if", Field: "el_paid", Op: "eq", Value: "Y", Goto: 6},
			{Kind: "log", Text: "{el_name}: {el_sum}, {el_who}, {el_paid}"},
			{Kind: "log", Text: "end"}}})
	must(err)
	got, err := s.Template(ctx, tpl)
	must(err)
	if f, ok := fieldOf(AllFields(got.Fields, got.Steps), "el_sum"); !ok || f.Kind != "number" || f.Label != "Заявки: Сумма" {
		t.Fatalf("the field el_sum: %+v", f)
	}
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"amount": "1500"})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	el := x.Values["created_list_element_id"]
	if x.Status != "done" || x.Values["created_list_element"] != "/bp/el/"+el || x.Values["el_name"] != "Заявка Анна" ||
		x.Values["el_sum"] != "1500" || x.Values["el_who"] != ann || x.Values["el_paid"] != "Y" {
		t.Fatalf("the process: %s %+v", x.Status, x.Values)
	}
	lines, err := s.Log(ctx, id)
	must(err)
	report := ""
	for _, l := range lines {
		if l.Kind == "report" && l.Text != "end" {
			report = l.Text
		}
	}
	if report != "Заявка Анна: 1500, Анна, Y" {
		t.Fatalf("the report: %q", report)
	}
	var authors []string
	rows, err := pool.Query(ctx, `SELECT author_id::text FROM list_log WHERE element_id = $1 ORDER BY id`, el)
	must(err)
	for rows.Next() {
		var a string
		must(rows.Scan(&a))
		authors = append(authors, a)
	}
	if rows.Err() != nil || strings.Join(authors, ",") != ann+","+ann {
		t.Fatalf("the element's history: %v", authors)
	}
	del, err := s.SaveTemplate(ctx, boss, Template{Name: "Удаление", Active: true, Fields: []Field{{Key: "el", Label: "Элемент", Kind: "string"}},
		Steps: []Step{{Kind: "listdelete", List: list, Value: "{el}"}}})
	must(err)
	_, err = s.Start(ctx, ann, del, "", map[string]string{"el": "/bp/el/" + el})
	must(err)
	if _, err := s.element(ctx, el); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the element is not deleted: %v", err)
	}

	// a step on an element that is not there stops the process
	bad, err := s.SaveTemplate(ctx, boss, Template{Name: "Чтение", Active: true, Steps: []Step{{Kind: "listread", List: list, Value: "{nothing}"}}})
	must(err)
	if id, err := s.Start(ctx, ann, bad, "", nil); err == nil {
		if x, _ := s.Instance(ctx, ann, id); x.Status == "done" {
			t.Fatal("a missing element read")
		}
	}

	// the file keeps the list by its name: another list of the name is taken on import
	f, err := s.Export(ctx, tpl)
	must(err)
	if f.Lists[list] != "Заявки" {
		t.Fatalf("the file's lists: %v", f.Lists)
	}
	f.Lists = map[string]string{"00000000-0000-0000-0000-000000000000": "заявки"}
	for i := range f.Steps {
		if f.Steps[i].List != "" {
			f.Steps[i].List = "00000000-0000-0000-0000-000000000000"
		}
	}
	data, err := json.Marshal(f)
	must(err)
	imported, _, err := s.Import(admin, boss, "", data)
	must(err)
	it, err := s.Template(ctx, imported)
	must(err)
	if it.Steps[0].List != list || it.Steps[2].List != list {
		t.Fatalf("the imported steps: %+v", it.Steps[:3])
	}
}
