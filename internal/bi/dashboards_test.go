package bi

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

func TestSpan(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Amsterdam")
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, loc) // a Sunday
	d := func(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, loc) }
	for p, want := range map[string][2]time.Time{
		"today": {d(2026, 10, 25), d(2026, 10, 26)}, "week": {d(2026, 10, 19), d(2026, 10, 26)},
		"month": {d(2026, 10, 1), d(2026, 11, 1)}, "quarter": {d(2026, 10, 1), d(2027, 1, 1)}, "year": {d(2026, 1, 1), d(2027, 1, 1)},
		"last_7": {d(2026, 10, 19), d(2026, 10, 26)}, "last_30": {d(2026, 9, 26), d(2026, 10, 26)},
		"prev_month": {d(2026, 9, 1), d(2026, 10, 1)}, "prev_quarter": {d(2026, 7, 1), d(2026, 10, 1)}, "prev_year": {d(2025, 1, 1), d(2026, 1, 1)},
	} {
		from, to := Span(p, nil, nil, now)
		if from == nil || to == nil || !from.Equal(want[0]) || !to.Equal(want[1]) {
			t.Errorf("%s: %v – %v, want %v", p, from, to, want)
		}
	}
	if from, to := Span("all", nil, nil, now); from != nil || to != nil {
		t.Error("all time has ends")
	}
	a, b := d(2026, 3, 1), d(2026, 3, 31)
	if from, to := Span("range", &a, &b, now); !from.Equal(a) || !to.Equal(d(2026, 4, 1)) {
		t.Errorf("range %v – %v", from, to)
	}
	if from, to := Span("range", &a, nil, now); !from.Equal(a) || to != nil {
		t.Errorf("open range %v – %v", from, to)
	}
}

// TestDashboards: a dashboard with its widgets — saved, checked, ordered, copied, removed.
func TestDashboards(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	s := NewStore(pool, crm.NewStore(pool))

	if _, err := s.SaveDashboard(ctx, anna, Dashboard{Title: " "}); !errors.Is(err, ErrTitle) {
		t.Errorf("no title: %v", err)
	}
	if _, err := s.SaveDashboard(ctx, anna, Dashboard{Title: "x", Period: "decade"}); !errors.Is(err, ErrPeriod) {
		t.Errorf("bad period: %v", err)
	}
	id, err := s.SaveDashboard(ctx, anna, Dashboard{Title: " Продажи ", Period: "quarter"})
	must(err)
	adm := Viewer{Admin: true}
	d, err := s.Dashboard(ctx, Viewer{ID: anna}, id)
	must(err)
	if d.Title != "Продажи" || d.Period != "quarter" || d.AuthorName != "Анна" || d.Status != "draft" || !d.CanEdit() ||
		!d.CanManage(Viewer{ID: anna}) || d.CanManage(Viewer{ID: petr}) || !d.CanManage(Viewer{ID: petr, Admin: true}) {
		t.Errorf("dashboard %+v", d)
	}
	if _, err := s.Dashboard(ctx, Viewer{ID: petr}, id); !errors.Is(err, ErrNotFound) { // a draft
		t.Errorf("another's draft: %v", err)
	}
	a, b := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	_, err = s.SaveDashboard(ctx, anna, Dashboard{ID: id, Title: "Продажи", Period: "range", DateFrom: &a, DateTo: &b})
	must(err)
	if d, _ = s.Dashboard(ctx, adm, id); d.Period != "range" || d.DateFrom == nil || d.DateTo.Day() != 31 {
		t.Errorf("range %+v", d)
	}

	bad := []Widget{
		{DashboardID: id, Kind: "radar", Width: 6, Query: Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}}}},
		{DashboardID: id, Kind: "bar", Width: 5, Query: Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}}}},
		{DashboardID: id, Kind: "bar", Width: 6, Query: Query{Dataset: "crm_deal", Dims: []string{"amount"}, Measures: []Measure{{Agg: "count"}}}},
		{DashboardID: id, Kind: "bar", Width: 6, Query: Query{Dataset: "nope", Measures: []Measure{{Agg: "count"}}}},
	}
	for i, w := range bad {
		if _, err := s.SaveWidget(ctx, w); err == nil {
			t.Errorf("bad widget %d saved", i)
		}
	}
	from := time.Now()
	w1, err := s.SaveWidget(ctx, Widget{DashboardID: id, Kind: "kpi", Width: 3, Title: " Сделок ",
		Query: Query{Dataset: "crm_deal", Measures: []Measure{{Agg: "count"}}, From: &from}})
	must(err)
	w2, err := s.SaveWidget(ctx, Widget{DashboardID: id, Kind: "bar", Width: 6, NoPeriod: true,
		Query: Query{Dataset: "crm_deal", Dims: []string{"created_at:month", "stage"}, Measures: []Measure{{Agg: "sum", Field: "amount"}},
			Filters: []Filter{{Field: "won", Op: "yes"}}, Sort: -3, Limit: 10}})
	must(err)
	w3, err := s.SaveWidget(ctx, Widget{DashboardID: id, Kind: "table", Width: 12, Query: Query{Dataset: "task", Dims: []string{"status"}, Measures: []Measure{{Agg: "count"}}}})
	must(err)
	x, err := s.Widget(ctx, w2)
	must(err)
	if x.Pos != 1 || !x.NoPeriod || len(x.Query.Dims) != 2 || x.Query.Measures[0].Field != "amount" || x.Query.Filters[0].Op != "yes" || x.Query.Sort != -3 {
		t.Errorf("widget %+v", x)
	}
	if x, _ = s.Widget(ctx, w1); x.Title != "Сделок" || x.Query.From != nil {
		t.Errorf("the period is the dashboard's: %+v", x)
	}
	order := func() (ids []string) {
		list, err := s.Widgets(ctx, id)
		must(err)
		for _, w := range list {
			ids = append(ids, w.ID)
		}
		return
	}
	same := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("order %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("order %v, want %v", got, want)
			}
		}
	}
	same(order(), w1, w2, w3)
	must(s.MoveWidget(ctx, w3, -1))
	same(order(), w1, w3, w2)
	must(s.MoveWidget(ctx, w1, -1)) // the first stays first
	same(order(), w1, w3, w2)
	must(s.OrderWidgets(ctx, id, []string{w2, w1}))
	same(order(), w2, w1, w3)
	c, err := s.CopyWidget(ctx, w2)
	must(err)
	same(order(), w2, c, w1, w3)

	if d, _ = s.Dashboard(ctx, adm, id); d.Widgets != 4 {
		t.Errorf("widgets %d", d.Widgets)
	}
	cp, err := s.CopyDashboard(ctx, petr, id, "Копия", nil)
	must(err)
	if d, _ := s.Dashboard(ctx, adm, cp); d.Widgets != 4 || d.CreatedBy != petr || d.Period != "range" {
		t.Errorf("copy %+v", d)
	}
	if list, _ := s.Dashboards(ctx, adm, "коп", ""); len(list) != 1 || list[0].ID != cp {
		t.Errorf("search %+v", list)
	}
	must(s.DeleteWidget(ctx, c))
	if err := s.DeleteWidget(ctx, c); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
	must(s.DeleteDashboard(ctx, id))
	if _, err := s.Widget(ctx, w1); !errors.Is(err, ErrNotFound) {
		t.Errorf("widgets outlive their dashboard: %v", err)
	}
	if list, _ := s.Dashboards(ctx, adm, "", ""); len(list) != 1 {
		t.Errorf("left %d", len(list))
	}
}
