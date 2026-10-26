package bi

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

func TestWithin(t *testing.T) {
	for key, want := range map[string][2]string{
		"day:2026-10-26":     {"2026-10-26", "2026-10-27"},
		"week:2026-10-26":    {"2026-10-26", "2026-11-02"},
		"month:2026-12-01":   {"2026-12-01", "2027-01-01"},
		"quarter:2026-10-01": {"2026-10-01", "2027-01-01"},
		"year:2026-01-01":    {"2026-01-01", "2027-01-01"},
	} {
		if a, b, ok := Within(key); !ok || a != want[0] || b != want[1] {
			t.Errorf("%s: %s %s %v", key, a, b, ok)
		}
	}
	for _, bad := range []string{"", "month", "hour:2026-10-01", "month:2026-13-01"} {
		if _, _, ok := Within(bad); ok {
			t.Errorf("%q is a span", bad)
		}
	}
}

// TestFilters: the keys of a result filter its rows back (a stage by its code, a flag, an empty value, a month of a
// date); a dashboard's picks narrow the widgets whose sets have the field and skip the others; the values of a filter
// are those the viewer reads; the filters of a dashboard are kept, copied, and the ready ones have theirs.
func TestFilters(t *testing.T) {
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
	pipes, err := c.Pipelines(ctx)
	must(err)
	deal := func(who string, amount float64, made string) string {
		id, err := c.SaveDeal(ctx, who, crm.Deal{Title: "Сделка", PipelineID: pipes[0].ID, Amount: amount, Responsible: crm.Ref{ID: who}})
		must(err)
		_, err = pool.Exec(ctx, `UPDATE crm_deals SET created_at = $2 WHERE id = $1`, id, made)
		must(err)
		return id
	}
	deal(anna, 100, "2026-09-10 12:00+02")
	deal(anna, 200, "2026-10-10 12:00+02")
	won := deal(petr, 400, "2026-10-20 12:00+02")
	_, err = pool.Exec(ctx, `UPDATE crm_deals d SET stage_id = (SELECT id FROM crm_stages st WHERE st.pipeline_id = d.pipeline_id
		AND st.semantic = 'success' LIMIT 1) WHERE d.id = $1`, won)
	must(err)

	sum := []Measure{{Agg: "sum", Field: "amount"}}
	// a key read back filters the rows it was of
	res, err := s.Run(ctx, anna, Query{Dataset: "crm_deal", Dims: []string{"stage", "won", "company", "created_at:month"}, Measures: sum}, tr)
	must(err)
	if len(res.Keys) != len(res.Rows) || len(res.Keys) == 0 {
		t.Fatalf("keys: %v of %v", res.Keys, res.Rows)
	}
	for i, k := range res.Keys {
		if k[1] != map[any]string{"«bi.yes»": "true", "«bi.no»": "false"}[res.Rows[i][1]] || k[2] != "" || k[3][:6] != "month:" || res.Rows[i][0] != "«crm.stage."+k[0]+"»" {
			t.Fatalf("keys of %v: %v", res.Rows[i], k)
		}
	}
	open := slices.IndexFunc(res.Keys, func(k []string) bool { return k[1] == "false" })
	back, err := s.Run(ctx, anna, Query{Dataset: "crm_deal", Measures: sum, Filters: []Filter{
		{Field: "stage", Op: "in", Values: []string{res.Keys[open][0]}}, {Field: "won", Op: "in", Values: []string{"false"}},
		{Field: "company", Op: "in", Values: []string{""}}, {Field: "created_at", Op: "within", Value: "month:2026-10-01"}}}, tr)
	must(err)
	if back.Rows[0][0] != 200.0 {
		t.Fatalf("anna's October: %v", back.Rows)
	}
	if _, err := s.Run(ctx, anna, Query{Dataset: "crm_deal", Measures: sum, Filters: []Filter{{Field: "amount", Op: "within", Value: "month:2026-10-01"}}}, tr); err == nil {
		t.Fatal("within a number")
	}

	// a dashboard's picks: the deals by the responsible and the month; the tasks have no responsible's month
	picked := map[string][]string{"responsible": {"anna", "petr"}, "created_at": {"month:2026-09-01", "month:2026-10-01"},
		"pipeline": {}}
	q, skipped, err := s.Narrow(ctx, Query{Dataset: "crm_deal", Measures: sum, Filters: []Filter{{Field: "won", Op: "no"}}}, picked)
	must(err)
	if len(skipped) != 0 || len(q.Filters) != 3 || q.Filters[1].Op != "within" || q.Filters[1].Value != "month:2026-10-01" {
		t.Fatalf("narrowed: %+v %v", q.Filters, skipped)
	}
	if res, err := s.Run(ctx, petr, q, tr); err != nil || res.Rows[0][0] != 200.0 {
		t.Fatalf("not won in October: %v %v", res.Rows, err)
	}
	if _, skipped, _ := s.Narrow(ctx, Query{Dataset: "timeman", Measures: sum}, picked); !slices.Equal(skipped, []string{"created_at", "responsible"}) {
		t.Fatalf("skipped: %v", skipped)
	}
	own := make([]Filter, 1, 4)
	own[0] = Filter{Field: "won", Op: "no"}
	if _, _, err := s.Narrow(ctx, Query{Dataset: "crm_deal", Filters: own}, picked); err != nil || own[:2][1].Field != "" {
		t.Fatalf("the widget's own filters changed: %v", err)
	}

	// the values: the most frequent first; a stage as read, its key the code
	vals, err := s.Values(ctx, anna, "crm_deal", "responsible", tr)
	must(err)
	if len(vals) != 2 || vals[0] != (Value{"anna", "anna"}) || vals[1] != (Value{"petr", "petr"}) {
		t.Fatalf("anna's values: %v", vals)
	}
	vals, err = s.Values(ctx, anna, "crm_deal", "stage", tr)
	must(err)
	if len(vals) != 2 || vals[1] != (Value{"won", "«crm.stage.won»"}) {
		t.Fatalf("anna's values: %v", vals)
	}

	// the filters of a dashboard: saved without repeats, copied; the ready ones have theirs
	id, err := s.SaveDashboard(ctx, anna, Dashboard{Title: "Мой", Filters: []string{"responsible", "", "stage", "responsible"}})
	must(err)
	d, err := s.Dashboard(ctx, Viewer{ID: anna}, id)
	must(err)
	if !slices.Equal(d.Filters, []string{"responsible", "stage"}) {
		t.Fatalf("filters: %v", d.Filters)
	}
	cp, err := s.CopyDashboard(ctx, petr, id, "Копия", nil)
	must(err)
	if d, err := s.Dashboard(ctx, Viewer{ID: petr}, cp); err != nil || !slices.Equal(d.Filters, []string{"responsible", "stage"}) {
		t.Fatalf("copied: %v %v", d.Filters, err)
	}
	must(s.EnsureSystem(ctx))
	list, err := s.Dashboards(ctx, Viewer{ID: anna}, "", "")
	must(err)
	if i := slices.IndexFunc(list, func(d Dashboard) bool { return d.Code == "sales" }); i < 0 || !slices.Equal(list[i].Filters, []string{"responsible", "pipeline"}) {
		t.Fatalf("ready: %+v", list)
	}
}
