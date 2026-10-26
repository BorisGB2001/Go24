package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestPlanStart: the periods of a plan start on their first month.
func TestPlanStart(t *testing.T) {
	day := time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)
	for period, want := range map[string]string{"month": "2026-11-01", "quarter": "2026-10-01", "half": "2026-07-01", "year": "2026-01-01"} {
		start, ok := PlanStart(period, day)
		if !ok || start.Format(time.DateOnly) != want {
			t.Errorf("%s: %s, want %s", period, start, want)
		}
	}
	if end := PlanEnd("quarter", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)); end.Format(time.DateOnly) != "2027-01-01" {
		t.Errorf("the quarter ends %s", end)
	}
	if _, ok := PlanStart("week", day); ok {
		t.Error("a week is not a period of a plan")
	}
	r := PlanRow{Target: 200, WonSum: 250, Metric: "sum"}
	if r.Percent() != 125 || r.Bar() != 100 || r.Left() != 0 {
		t.Errorf("exceeded: %d%% bar %d left %v", r.Percent(), r.Bar(), r.Left())
	}
	if r = (PlanRow{Target: 4, Won: 1, WonSum: 999, Metric: "count"}); r.Percent() != 25 || r.Left() != 3 {
		t.Errorf("by count: %d%% left %v", r.Percent(), r.Left())
	}
}

// TestSalesPlan: the plan of a pipeline for a quarter — the company's, a department's (with the subdepartment's
// employee) and the employees' targets against the deals won in the quarter by their responsible, in the base
// currency; the open deals to be closed in the quarter are «в работе»; the deals of another quarter or pipeline,
// lost or without the close date do not count; saving replaces the targets, the count metric counts the deals, and
// a plan without targets is removed.
func TestSalesPlan(t *testing.T) {
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
	var anna, petr, olga, sales, south string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'olga@t') RETURNING id`).Scan(&olga))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Юг', $1) RETURNING id`, sales).Scan(&south))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $4)`, anna, petr, sales, south)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO crm_currencies (code, name, rate) VALUES ('USD', 'USD', 90) ON CONFLICT (code) DO UPDATE SET rate = 90`)
	must(err)
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	stage := func(semantic string) string {
		for _, st := range p.Stages {
			if st.Semantic == semantic {
				return st.ID
			}
		}
		t.Fatal("no stage", semantic)
		return ""
	}
	q4 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	deal := func(who string, amount float64, currency, semantic string, closed time.Time, closeDate *time.Time, pipeline string) {
		t.Helper()
		id, err := s.SaveDeal(ctx, who, Deal{Title: "Сделка", PipelineID: pipeline, Amount: amount, Currency: currency,
			Responsible: Ref{ID: who}, CloseDate: closeDate})
		must(err)
		if semantic != "process" {
			_, err = pool.Exec(ctx, `UPDATE crm_deals SET stage_id = $2, closed_at = $3 WHERE id = $1`, id, stage(semantic), closed)
			must(err)
		}
	}
	nov := time.Date(2026, 11, 10, 12, 0, 0, 0, time.Local)
	dec := time.Date(2026, 12, 20, 0, 0, 0, 0, time.Local)
	deal(anna, 100000, "RUB", "success", nov, nil, p.ID)
	deal(anna, 1000, "USD", "success", nov, nil, p.ID)                                           // 90 000 in the base one
	deal(petr, 50000, "RUB", "success", nov, nil, p.ID)                                          // in «Юг», a part of «Продажи»
	deal(olga, 30000, "RUB", "success", nov, nil, p.ID)                                          // no department
	deal(olga, 70000, "RUB", "process", time.Time{}, &dec, p.ID)                                 // in work: to be closed in the quarter
	deal(anna, 999, "RUB", "failure", nov, nil, p.ID)                                            // lost
	deal(anna, 999, "RUB", "success", time.Date(2026, 9, 30, 0, 0, 0, 0, time.Local), nil, p.ID) // the previous quarter
	deal(anna, 999, "RUB", "process", time.Time{}, nil, p.ID)                                    // no close date
	np, err := s.AddPipeline(ctx, "Тендеры")
	must(err)
	deal(anna, 999, "RUB", "process", time.Time{}, &dec, np)

	plan, err := s.SalesPlan(ctx, p.ID, "quarter", nov)
	must(err)
	if plan.ID != "" || plan.Company.WonSum != 270000 || plan.Company.Won != 4 || plan.Company.InWorkSum != 70000 || len(plan.Departments) != 0 {
		t.Fatalf("no plan yet: %+v", plan)
	}
	if len(plan.Users) != 3 || plan.Users[0].Name != "Анна" || plan.Users[0].WonSum != 190000 || plan.Users[1].InWork != 1 {
		t.Fatalf("the employees with deals: %+v", plan.Users)
	}
	must(s.SaveSalesPlan(ctx, anna, p.ID, "quarter", dec, PlanTargets{Metric: "sum", Company: 500000,
		Departments: map[string]float64{sales: 300000, south: 0}, Users: map[string]float64{petr: 100000}}))
	plan, err = s.SalesPlan(ctx, p.ID, "quarter", q4)
	must(err)
	if plan.ID == "" || plan.Company.Target != 500000 || plan.Company.Percent() != 54 || plan.Company.Left() != 230000 || plan.UpdatedBy.Name != "Анна" {
		t.Fatalf("the company: %+v", plan)
	}
	if len(plan.Departments) != 1 || plan.Departments[0].Name != "Продажи" || plan.Departments[0].WonSum != 240000 || plan.Departments[0].Percent() != 80 {
		t.Fatalf("the department with the subdepartment: %+v", plan.Departments)
	}
	if plan.Users[0].Name != "Пётр" || plan.Users[0].Target != 100000 || plan.Users[0].Percent() != 50 || plan.Users[1].Target != 0 {
		t.Fatalf("the employees, with a target first: %+v", plan.Users)
	}
	if month, _ := s.SalesPlan(ctx, p.ID, "month", nov); month.ID != "" || month.Company.Won != 4 {
		t.Fatalf("the month has no plan: %+v", month)
	}

	must(s.SaveSalesPlan(ctx, petr, p.ID, "quarter", q4, PlanTargets{Metric: "count", Company: 8, Users: map[string]float64{olga: 2}}))
	plan, err = s.SalesPlan(ctx, p.ID, "quarter", q4)
	must(err)
	if plan.Metric != "count" || plan.Company.Percent() != 50 || plan.Company.Forecast() != 1 || len(plan.Departments) != 0 || plan.Users[0].Name != "Ольга" || plan.Users[0].Left() != 1 {
		t.Fatalf("by count: %+v", plan)
	}
	if err := s.SaveSalesPlan(ctx, anna, p.ID, "quarter", q4, PlanTargets{Metric: "sum", Company: -1}); err == nil {
		t.Fatal("a negative target is accepted")
	}
	if err := s.SaveSalesPlan(ctx, anna, p.ID, "week", q4, PlanTargets{Metric: "sum", Company: 1}); err == nil {
		t.Fatal("a week plan is accepted")
	}
	must(s.SaveSalesPlan(ctx, anna, p.ID, "quarter", q4, PlanTargets{Metric: "sum"}))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_sales_plans`).Scan(&n))
	if n != 0 {
		t.Fatal("a plan without targets stays")
	}
}
