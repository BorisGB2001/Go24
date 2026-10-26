package bi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/text/encoding/charmap"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestParseCSV: the separator and the encoding are guessed, the headers made unique, the numbers, dates and flags
// read as people write them, the types of the columns guessed.
func TestParseCSV(t *testing.T) {
	t.Parallel()
	cp, _ := charmap.Windows1251.NewEncoder().String("Город;Сумма\nТверь;1 234,50\n\nМосква;-7\n")
	for _, src := range []string{"\uFEFFГород;Сумма\nТверь;1 234,50\n\nМосква;-7\n", cp, "Город,Сумма\nТверь,\"1,234.50\"\nМосква,-7\n",
		"Город\tСумма\nТверь\t1234.5\nМосква\t-7"} {
		tb, err := ParseCSV([]byte(src))
		if err != nil || len(tb.Head) != 2 || tb.Head[0] != "Город" || len(tb.Rows) != 2 || tb.Rows[1][0] != "Москва" {
			t.Fatalf("%q: %+v %v", src, tb, err)
		}
		if n, ok := parseNum(tb.Rows[0][1]); !ok || n != 1234.5 {
			t.Errorf("%q: %v %v", tb.Rows[0][1], n, ok)
		}
	}
	tb, err := ParseCSV([]byte("a;;A\n1;2;3"))
	if err != nil || tb.Head[1] != "2" || tb.Head[2] != "A (2)" {
		t.Errorf("%+v %v", tb.Head, err)
	}
	if _, err := ParseCSV([]byte("\n\n")); err == nil {
		t.Error("an empty file")
	}
	for v, want := range map[string]float64{"1.234,5": 1234.5, "1,234,567": 1234567, "1.234.567": 1234567, "12": 12, "1e3": 1000} {
		if n, ok := parseNum(v); !ok || n != want {
			t.Errorf("%s: %v %v", v, n, ok)
		}
	}
	for _, v := range []string{"abc", "inf", "NaN", "0x10", "1,2.3.4", "12%", "12.03.2026", "1.23.456"} {
		if _, ok := parseNum(v); ok {
			t.Errorf("%s is a number", v)
		}
	}
	for v, want := range map[string]string{"2026-03-05": "2026-03-05 00:00:00", "05.03.2026 14:30": "2026-03-05 14:30:00",
		"2026-03-05T10:00:00+03:00": "2026-03-05T10:00:00+03:00", "5.3.2026": "2026-03-05 00:00:00"} {
		if d, ok := parseDate(v); !ok || d != want {
			t.Errorf("%s: %v %v", v, d, ok)
		}
	}
	if _, ok := parseDate("31.02.2026"); ok {
		t.Error("31.02 is a date")
	}
	for cells, want := range map[string]string{"да,нет,": Bool, "1,0,1": Number, "12,3.5,": Number, "007,8": String,
		"+79001234567": String, "2026-01-01,01.02.2026": Date, "a,1": String, ",": String} {
		if got := detect(splitComma(cells)); got != want {
			t.Errorf("%s: %s, want %s", cells, got, want)
		}
	}
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(out, cur)
}

// TestFiles: a set of one's own from a file — its columns and their types, the queries over it with the period on its
// first date, a type changed, the file loaded again (replaced and added, a new column), the export, the deletion.
func TestFiles(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool, crm.NewStore(pool))
	s.TZ = "Europe/Amsterdam"
	tr := func(k string) string { return "«" + k + "»" }

	tb, err := ParseCSV([]byte("Город;Дата;Сумма;Оплачен;Код\nТверь;05.03.2026;1 000,5;да;007\nМосква;2026-03-20;200;нет;008\nТверь;01.01.2025;50;да;\nТверь;вчера;x;да;009\n"))
	must(err)
	id, err := s.CreateFile(ctx, anna, " ", "Продажи.csv", tb)
	must(err)
	f, err := s.File(ctx, id)
	must(err)
	if f.Name != "Продажи" || f.Rows != 4 || f.AuthorName != "Анна" || len(f.Columns) != 5 || !f.Editable(Viewer{ID: anna}) || f.Editable(Viewer{ID: "x"}) {
		t.Fatalf("%+v", f)
	}
	types := ""
	for _, c := range f.Columns {
		types += c.Code + ":" + c.Type + " "
	}
	if types != "c1:string c2:string c3:string c4:bool c5:string " { // «вчера» and «x» spoil the date and the sum
		t.Errorf("types: %s", types)
	}
	must(s.SaveFile(ctx, id, "Продажи 2026", "", []FileColumn{{Code: "c2", Type: Date}, {Code: "c3", Name: "Выручка", Type: Number}}))
	if f, _ = s.File(ctx, id); f.Name != "Продажи 2026" || f.Columns[2].Name != "Выручка" || f.Columns[2].Header != "Сумма" || f.Columns[1].Type != Date {
		t.Fatalf("%+v", f)
	}
	bad, err := s.Unread(ctx, f)
	must(err)
	if bad["c2"] != 1 || bad["c3"] != 1 || len(bad) != 2 {
		t.Errorf("unread: %v", bad)
	}
	ds, err := s.Dataset(ctx, f.Code())
	must(err)
	if ds == nil || ds.Period != "c2" || ds.Tr(tr)("bi.f.c3") != "Выручка" || ds.Tr(tr)("bi.ds."+f.Code()) != "Продажи 2026" || ds.Tr(tr)("bi.agg.sum") != "«bi.agg.sum»" {
		t.Fatalf("%+v", ds)
	}

	// by city over March 2026 (the period on the first date), all time, a flag filter, a date dimension
	from, to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)
	res, err := s.Run(ctx, anna, Query{Dataset: f.Code(), Dims: []string{"c1"}, Measures: []Measure{{Agg: "sum", Field: "c3"}, {Agg: "count"}},
		From: &from, To: &to}, tr)
	must(err)
	if len(res.Rows) != 2 || res.Rows[0][0] != "Тверь" || res.Rows[0][1] != 1000.5 || res.Rows[1][1] != 200.0 {
		t.Errorf("march: %v", res.Rows)
	}
	res, err = s.Run(ctx, anna, Query{Dataset: f.Code(), Dims: []string{"c4", "c2:year"}, Measures: []Measure{{Agg: "count"}},
		Filters: []Filter{{Field: "c4", Op: "yes"}}, Sort: 2}, tr)
	must(err)
	if len(res.Rows) != 3 || res.Rows[0][0] != "«bi.yes»" || res.Rows[0][1] != "2025-01-01" || res.Rows[2][1] != "" {
		t.Errorf("flags: %v", res.Rows)
	}
	if err := s.Check(ctx, Query{Dataset: f.Code(), Measures: []Measure{{Agg: "sum", Field: "c1"}}}); err == nil {
		t.Error("a sum of text")
	}
	if err := s.CheckWidget(ctx, Widget{Kind: "bar", Width: 6, Query: Query{Dataset: f.Code(), Dims: []string{"c1"}, Measures: []Measure{{Agg: "count"}}}}); err != nil {
		t.Error(err)
	}

	// loaded again: rows added, then replaced by a file with the columns in another order and a new one
	tb, _ = ParseCSV([]byte("Город;Сумма\nКазань;10\n"))
	must(s.LoadFile(ctx, id, "ещё.csv", tb, true))
	tb, _ = ParseCSV([]byte("Выручка;Город;Менеджер\n5;Сочи;Пётр\n7;Сочи;Анна\n"))
	must(s.LoadFile(ctx, id, "новый.csv", tb, false))
	if f, _ = s.File(ctx, id); f.Rows != 2 || len(f.Columns) != 6 || f.Columns[5].Name != "Менеджер" || f.Columns[5].Code != "c6" || f.FileName != "новый.csv" {
		t.Fatalf("%+v", f)
	}
	res, err = s.Run(ctx, anna, Query{Dataset: f.Code(), Dims: []string{"c1"}, Measures: []Measure{{Agg: "sum", Field: "c3"}, {Agg: "count_distinct", Field: "c6"}},
		From: &from, To: &to}, tr) // no dates now: c2 is empty, so the period finds none
	must(err)
	if len(res.Rows) != 0 {
		t.Errorf("period: %v", res.Rows)
	}
	res, err = s.Run(ctx, anna, Query{Dataset: f.Code(), Dims: []string{"c1"}, Measures: []Measure{{Agg: "sum", Field: "c3"}, {Agg: "count_distinct", Field: "c6"}}}, tr)
	must(err)
	if len(res.Rows) != 1 || res.Rows[0][0] != "Сочи" || res.Rows[0][1] != 12.0 || res.Rows[0][2] != 2.0 {
		t.Errorf("replaced: %v", res.Rows)
	}
	exp, err := s.Rows(ctx, anna, Export{Dataset: f.Code(), Fields: []string{"c1", "c3"}}, tr)
	must(err)
	if len(exp.Rows) != 2 || exp.Rows[0][0] != "1" || exp.Rows[0][1] != "Сочи" || exp.Rows[0][2] != 5.0 {
		t.Errorf("export: %v", exp.Rows)
	}
	all, err := s.AllDatasets(ctx)
	must(err)
	if len(all) != len(Datasets)+1 || all[len(all)-1].Code != f.Code() {
		t.Errorf("all: %d", len(all))
	}
	if _, err := s.CreateKey(ctx, anna, "k", []string{f.Code(), "file_999"}); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.Keys(ctx)
	if len(keys) != 1 || len(keys[0].Datasets) != 1 || keys[0].Datasets[0] != f.Code() {
		t.Errorf("key: %+v", keys)
	}

	must(s.DeleteFile(ctx, id))
	if ds, err := s.Dataset(ctx, f.Code()); err != nil || ds != nil {
		t.Errorf("deleted: %v %v", ds, err)
	}
	if _, err := s.Run(ctx, anna, Query{Dataset: f.Code(), Measures: []Measure{{Agg: "count"}}}, tr); err == nil {
		t.Error("a deleted set")
	}
	if err := s.DeleteFile(ctx, id); err != ErrNotFound {
		t.Error(err)
	}
}
