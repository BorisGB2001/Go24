package bi

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestRun: every field of every data set works as a dimension, a measure and the period; the deals by stage, by
// person and by month, filtered, within the CRM rights; the tasks within their visibility; the export of rows; the
// keys of external BI; the errors.
func TestRun(t *testing.T) {
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
	anna, petr := newUser("anna"), newUser("petr")
	c := crm.NewStore(pool)
	s := NewStore(pool, c)
	s.TZ = "Europe/Amsterdam"
	tr := func(k string) string { return "«" + k + "»" }

	// every field as a dimension, every number as a measure, every date as the period
	from, to := time.Now().AddDate(0, -1, 0), time.Now().AddDate(0, 0, 1)
	for _, ds := range Datasets {
		for _, f := range ds.Fields {
			q := Query{Dataset: ds.Code, Measures: []Measure{{Agg: "count"}}, From: &from, To: &to}
			if f.Dim() {
				q.Dims = []string{f.Code}
				q.Filters = []Filter{{Field: f.Code, Op: "set"}}
			} else {
				q.Measures = append(q.Measures, Measure{Agg: "sum", Field: f.Code}, Measure{Agg: "avg", Field: f.Code})
			}
			if f.Type == Date {
				q.DateField = f.Code
				q.Dims = []string{f.Code + ":month"}
			}
			if _, err := s.Run(ctx, anna, q, tr); err != nil {
				t.Fatalf("%s.%s: %v", ds.Code, f.Code, err)
			}
		}
	}

	pipes, err := c.Pipelines(ctx)
	must(err)
	p := pipes[0]
	won := p.Stages[len(p.Stages)-2].ID
	deal := func(title string, amount float64, resp, stage string) {
		id, err := c.SaveDeal(ctx, anna, crm.Deal{Title: title, PipelineID: p.ID, Amount: amount, Responsible: crm.Ref{ID: resp}})
		must(err)
		if stage != "" {
			_, err = c.MoveDeal(ctx, anna, id, stage)
			must(err)
		}
	}
	deal("a", 1000, anna, won)
	deal("b", 3000, anna, won)
	deal("c", 500, petr, "")

	// by stage: the built-in names come from the locale; the sum and the count; the stages as their pipeline goes
	// (the first one before «won»), by the count when asked
	res, err := s.Run(ctx, anna, Query{Dataset: "crm_deal", Dims: []string{"stage"},
		Measures: []Measure{{Agg: "count"}, {Agg: "sum", Field: "amount"}}}, tr)
	must(err)
	if len(res.Rows) != 2 || res.Rows[1][0] != "«crm.stage.won»" || res.Rows[1][1] != 2.0 || res.Rows[1][2] != 4000.0 {
		t.Fatalf("by stage: %v", res.Rows)
	}
	res, err = s.Run(ctx, anna, Query{Dataset: "crm_deal", Dims: []string{"stage"}, Measures: []Measure{{Agg: "count"}}, Sort: -2}, tr)
	must(err)
	if len(res.Rows) != 2 || res.Rows[0][0] != "«crm.stage.won»" {
		t.Fatalf("by stage, the most first: %v", res.Rows)
	}
	// by responsible, filtered to the won; and by month of creation
	res, err = s.Run(ctx, anna, Query{Dataset: "crm_deal", Dims: []string{"responsible"}, Measures: []Measure{{Agg: "avg", Field: "amount"}},
		Filters: []Filter{{Field: "won", Op: "yes"}}}, tr)
	must(err)
	if len(res.Rows) != 1 || res.Rows[0][0] != "anna" || res.Rows[0][1] != 2000.0 {
		t.Fatalf("won by responsible: %v", res.Rows)
	}
	res, err = s.Run(ctx, anna, Query{Dataset: "crm_deal", Dims: []string{"created_at:month"}, Measures: []Measure{{Agg: "count"}}}, tr)
	must(err)
	month := time.Now().In(must2(time.LoadLocation("Europe/Amsterdam"))).Format("2006-01")
	if len(res.Rows) != 1 || res.Rows[0][0].(string)[:7] != month || res.Rows[0][1] != 3.0 {
		t.Fatalf("by month: %v", res.Rows)
	}
	// a period before the deals: none
	old := time.Now().AddDate(-1, 0, 0)
	res, err = s.Run(ctx, anna, Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}}, To: &old}, tr)
	must(err)
	if res.Rows[0][0] != 0.0 {
		t.Fatalf("before the period: %v", res.Rows)
	}

	// a manager reading their own deals only counts those
	manager, err := c.SaveRole(ctx, crm.Role{Name: "Менеджер", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(c.SetMember(ctx, manager, petr, true))
	res, err = s.Run(ctx, petr, Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}, {Agg: "sum", Field: "amount"}}}, tr)
	must(err)
	if res.Rows[0][0] != 1.0 || res.Rows[0][1] != 500.0 {
		t.Fatalf("a manager's deals: %v", res.Rows)
	}

	// tasks: only the visible ones; the status from the locale
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "BI", "BI", "")
	must(err)
	pr, err := ts.ProjectByKey(ctx, anna, "BI")
	must(err)
	_, err = ts.CreateTask(ctx, anna, pr, tasks.NewTask{Title: "x", ResponsibleID: anna, Priority: 1})
	must(err)
	q := Query{Dataset: "task", Dims: []string{"status"}, Measures: []Measure{{Agg: "count"}}}
	res, err = s.Run(ctx, anna, q, tr)
	must(err)
	if len(res.Rows) != 1 || res.Rows[0][0] != "«status.pending»" {
		t.Fatalf("tasks: %v", res.Rows)
	}
	if res, _ = s.Run(ctx, petr, q, tr); len(res.Rows) != 0 {
		t.Fatalf("someone else's tasks: %v", res.Rows)
	}
	if res, _ = s.Run(tasks.WithAdmin(ctx), petr, q, tr); len(res.Rows) != 1 {
		t.Fatalf("admin mode: %v", res.Rows)
	}

	// an export: every data set, all its fields; the deals' rows with the id first, the numbers and flags as they
	// are, the dates with their time; the fields picked, a filter, paged; the rights still hold
	for _, ds := range Datasets {
		if _, err := s.Rows(ctx, anna, Export{Dataset: ds.Code}, tr); err != nil {
			t.Fatalf("export %s: %v", ds.Code, err)
		}
	}
	res, err = s.Rows(ctx, anna, Export{Dataset: "crm_deal"}, tr)
	must(err)
	if len(res.Rows) != 3 || res.Columns[0].Field != "id" || len(res.Columns) != len(DatasetOf("crm_deal").Fields)+1 {
		t.Fatalf("export: %v %v", res.Columns, res.Rows)
	}
	res, err = s.Rows(ctx, anna, Export{Dataset: "crm_deal", Fields: []string{"title", "amount", "won", "created_at", "closed_at"},
		Filters: []Filter{{Field: "amount", Op: "gte", Value: "1000"}}}, tr)
	must(err)
	if len(res.Rows) != 2 || len(res.Rows[0]) != 6 {
		t.Fatalf("export of fields: %v", res.Rows)
	}
	for _, row := range res.Rows {
		if _, ok := row[2].(float64); !ok || row[3] != true || len(row[4].(string)) != 19 || row[5] == nil {
			t.Fatalf("export values: %#v", row)
		}
	}
	page, err := s.Rows(ctx, anna, Export{Dataset: "crm_deal", Fields: []string{"title"}, Limit: 1, Offset: 1}, tr)
	must(err)
	if len(page.Rows) != 1 {
		t.Fatalf("export page: %v", page.Rows)
	}
	if res, _ = s.Rows(ctx, petr, Export{Dataset: "crm_deal"}, tr); len(res.Rows) != 1 {
		t.Fatalf("a manager's export: %v", res.Rows)
	}

	// keys: the token works once issued, limited to its sets; a wrong or revoked one does not
	token, err := s.CreateKey(ctx, anna, "Power BI", []string{"crm_deal", "nope"})
	must(err)
	k, err := s.Authenticate(ctx, token, "10.0.0.1")
	must(err)
	if k.CreatedBy != anna || !k.Reads("crm_deal") || k.Reads("task") {
		t.Fatalf("key: %+v", k)
	}
	if _, err := s.Authenticate(ctx, token[:len(token)-1]+"0", ""); err == nil && token[len(token)-1] != '0' {
		t.Fatal("a wrong secret passes")
	}
	if _, err := s.CreateKey(ctx, anna, " ", nil); err == nil {
		t.Fatal("a key without a name")
	}
	keys, err := s.Keys(ctx)
	must(err)
	if len(keys) != 1 || keys[0].Uses != 1 || keys[0].LastUsedIP != "10.0.0.1" || keys[0].AuthorName != "anna" {
		t.Fatalf("keys: %+v", keys)
	}
	must(s.RevokeKey(ctx, k.ID))
	if _, err := s.Authenticate(ctx, token, ""); err == nil {
		t.Fatal("a revoked key passes")
	}
	must(s.DeleteKey(ctx, k.ID))
	if keys, _ = s.Keys(ctx); len(keys) != 0 {
		t.Fatalf("deleted: %v", keys)
	}

	// the errors: an unknown set, field, a measure of a text
	for _, q := range []Query{{Dataset: "nope", Dims: []string{"x"}}, {Dataset: "task", Dims: []string{"nope"}},
		{Dataset: "task", Measures: []Measure{{Agg: "sum", Field: "title"}}}, {Dataset: "task"},
		{Dataset: "task", Dims: []string{"status"}, Filters: []Filter{{Field: "status", Op: "drop"}}}} {
		if _, err := s.Run(ctx, anna, q, tr); err == nil {
			t.Fatalf("no error: %+v", q)
		}
	}
}

func must2[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
