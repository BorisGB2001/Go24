package crm

import (
	"context"
	"errors"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «План продаж» (CRM → Аналитика): a plan of a deal pipeline for a month, a quarter, a half-year or a
// year — by the sum (in the base currency) or the count of the deals won in the period. The company, departments
// (their employees with the subdepartments' ones) and employees have their targets; the fact is the won deals by
// their responsible, «в работе» — the open deals to be closed (close_date) in the period.

var planIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// PlanPeriods are the kinds of the plan's period.
var PlanPeriods = []string{"month", "quarter", "half", "year"}

// PlanStart is the first day of the period of the kind holding t; ok is false for an unknown kind.
func PlanStart(period string, t time.Time) (time.Time, bool) {
	m := int(t.Month()) - 1
	switch period {
	case "month":
	case "quarter":
		m -= m % 3
	case "half":
		m -= m % 6
	case "year":
		m = 0
	default:
		return time.Time{}, false
	}
	return time.Date(t.Year(), time.Month(m+1), 1, 0, 0, 0, 0, t.Location()), true
}

// planMonths is the period's length in months.
func planMonths(period string) int {
	return map[string]int{"month": 1, "quarter": 3, "half": 6, "year": 12}[period]
}

// PlanEnd is the day after the period starting at start.
func PlanEnd(period string, start time.Time) time.Time {
	return start.AddDate(0, planMonths(period), 0)
}

// PlanRow is a line of the plan: the company (no ID), a department or an employee.
type PlanRow struct {
	Ref
	Target    float64 // 0 — no target
	Won       int     // the deals won in the period
	WonSum    float64
	InWork    int // the open deals to be closed in the period
	InWorkSum float64
	Metric    string // the plan's: "sum" or "count"
}

// Fact is what counts against the target: the won sum or count.
func (r PlanRow) Fact() float64 {
	if r.Metric == "count" {
		return float64(r.Won)
	}
	return r.WonSum
}

// Forecast is the in-work part by the metric.
func (r PlanRow) Forecast() float64 {
	if r.Metric == "count" {
		return float64(r.InWork)
	}
	return r.InWorkSum
}

// Percent is the fact of the target, in percent (more than 100 when exceeded).
func (r PlanRow) Percent() int {
	if r.Target <= 0 {
		return 0
	}
	return int(math.Floor(r.Fact() * 100 / r.Target))
}

// Bar is Percent up to 100 (the bar's width).
func (r PlanRow) Bar() int { return min(r.Percent(), 100) }

// Left is what remains to the target.
func (r PlanRow) Left() float64 { return round2(max(r.Target-r.Fact(), 0)) }

// SalesPlan is the plan of a pipeline for a period with the facts; ID "" — no plan set yet.
type SalesPlan struct {
	ID, PipelineID, Period, Metric string
	Start, End                     time.Time
	Company                        PlanRow
	Departments, Users             []PlanRow // departments with targets; employees with targets or won deals
	Currency                       string
	UpdatedBy                      Ref
	UpdatedAt                      time.Time
}

// PlanTargets is what SaveSalesPlan sets: the metric and the targets by department and employee ID; a zero target is none.
type PlanTargets struct {
	Metric      string
	Company     float64
	Departments map[string]float64
	Users       map[string]float64
}

// planAgg selects the won and in-work counts and sums of the deals d (stage st) of the pipeline $1 in [$2, $3).
var planAgg = `count(*) FILTER (WHERE st.semantic = 'success' AND d.closed_at >= $2 AND d.closed_at < $3)::int AS won,
	coalesce(sum(` + inBase("d.amount", "d.currency") + `) FILTER (WHERE st.semantic = 'success' AND d.closed_at >= $2 AND d.closed_at < $3), 0)::float8 AS won_sum,
	count(*) FILTER (WHERE st.semantic = 'process' AND d.close_date >= $2::date AND d.close_date < $3::date)::int AS work,
	coalesce(sum(` + inBase("d.amount", "d.currency") + `) FILTER (WHERE st.semantic = 'process' AND d.close_date >= $2::date AND d.close_date < $3::date), 0)::float8 AS work_sum`

const planDeals = ` crm_deals d JOIN crm_stages st ON st.id = d.stage_id WHERE d.pipeline_id::text = $1
	AND ((st.semantic = 'success' AND d.closed_at >= $2 AND d.closed_at < $3) OR (st.semantic = 'process' AND d.close_date >= $2::date AND d.close_date < $3::date))`

// SalesPlan is the plan of the pipeline for the period of the kind holding day, with the facts.
func (s *Store) SalesPlan(ctx context.Context, pipelineID, period string, day time.Time) (SalesPlan, error) {
	start, ok := PlanStart(period, day)
	if !ok {
		return SalesPlan{}, i18n.E("err.bad_form")
	}
	p := SalesPlan{PipelineID: pipelineID, Period: period, Start: start, End: PlanEnd(period, start), Metric: "sum"}
	var err error
	if p.Currency, err = s.BaseCurrency(ctx); err != nil {
		return p, err
	}
	err = s.db.QueryRow(ctx, `SELECT p.id::text, p.metric, coalesce(p.updated_by::text, ''), coalesce(u.name, ''), p.updated_at
		FROM crm_sales_plans p LEFT JOIN users u ON u.id = p.updated_by WHERE p.pipeline_id::text = $1 AND p.period = $2 AND p.starts = $3::date`,
		pipelineID, period, start.Format(time.DateOnly)).Scan(&p.ID, &p.Metric, &p.UpdatedBy.ID, &p.UpdatedBy.Name, &p.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	end := p.End
	p.Company.Metric = p.Metric
	if err = s.db.QueryRow(ctx, `SELECT `+planAgg+` FROM`+planDeals, pipelineID, start, end).
		Scan(&p.Company.Won, &p.Company.WonSum, &p.Company.InWork, &p.Company.InWorkSum); err != nil {
		return p, err
	}
	if err = s.db.QueryRow(ctx, `SELECT coalesce((SELECT amount::float8 FROM crm_sales_targets WHERE plan_id::text = $1
		AND department_id IS NULL AND user_id IS NULL), 0)`, p.ID).Scan(&p.Company.Target); err != nil {
		return p, err
	}
	collect := func(rows pgx.Rows, err error) ([]PlanRow, error) {
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PlanRow, error) {
			x := PlanRow{Metric: p.Metric}
			err := r.Scan(&x.ID, &x.Name, &x.Target, &x.Won, &x.WonSum, &x.InWork, &x.InWorkSum)
			return x, err
		})
	}
	// the departments with targets: the deals of their employees and of the subdepartments' ones
	if p.Departments, err = collect(s.db.Query(ctx, `WITH RECURSIVE tree AS (
			SELECT t.department_id AS root, t.department_id AS id FROM crm_sales_targets t WHERE t.plan_id::text = $4 AND t.department_id IS NOT NULL
			UNION SELECT tree.root, dp.id FROM departments dp JOIN tree ON dp.parent_id = tree.id),
		m AS (SELECT DISTINCT tree.root, ud.user_id FROM tree JOIN user_departments ud ON ud.department_id = tree.id),
		agg AS (SELECT m.root, `+planAgg+` FROM m,`+planDeals+` AND d.responsible_id = m.user_id GROUP BY m.root)
		SELECT dp.id::text, dp.name, t.amount::float8, coalesce(agg.won, 0), coalesce(agg.won_sum, 0), coalesce(agg.work, 0), coalesce(agg.work_sum, 0)
		FROM crm_sales_targets t JOIN departments dp ON dp.id = t.department_id LEFT JOIN agg ON agg.root = dp.id
		WHERE t.plan_id::text = $4 ORDER BY dp.name`, pipelineID, start, end, p.ID)); err != nil {
		return p, err
	}
	// the employees with targets or with deals of the period
	p.Users, err = collect(s.db.Query(ctx, `WITH agg AS (SELECT d.responsible_id AS uid, `+planAgg+` FROM`+planDeals+` GROUP BY 1),
		tg AS (SELECT user_id AS uid, amount FROM crm_sales_targets WHERE plan_id::text = $4 AND user_id IS NOT NULL)
		SELECT u.id::text, u.name, coalesce(tg.amount, 0)::float8, coalesce(agg.won, 0), coalesce(agg.won_sum, 0), coalesce(agg.work, 0), coalesce(agg.work_sum, 0)
		FROM users u LEFT JOIN agg ON agg.uid = u.id LEFT JOIN tg ON tg.uid = u.id
		WHERE agg.uid IS NOT NULL OR tg.uid IS NOT NULL ORDER BY coalesce(tg.amount, 0) = 0, u.name`, pipelineID, start, end, p.ID))
	return p, err
}

// SaveSalesPlan sets the plan of the pipeline for the period of the kind holding day: its metric and targets, all of
// them replaced; a plan without targets is removed.
func (s *Store) SaveSalesPlan(ctx context.Context, by, pipelineID, period string, day time.Time, t PlanTargets) error {
	start, ok := PlanStart(period, day)
	if !ok || (t.Metric != "sum" && t.Metric != "count") {
		return i18n.E("err.bad_form")
	}
	targets := []float64{t.Company}
	for _, m := range []map[string]float64{t.Departments, t.Users} {
		for id, v := range m {
			if !planIDRe.MatchString(id) {
				return i18n.E("err.bad_form")
			}
			targets = append(targets, v)
		}
	}
	set := false
	for _, v := range targets {
		if v < 0 || v > 1e15 || math.IsNaN(v) {
			return i18n.E("err.crm_plan_amount")
		}
		set = set || v > 0
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	day0 := start.Format(time.DateOnly)
	if !set {
		if _, err = tx.Exec(ctx, `DELETE FROM crm_sales_plans WHERE pipeline_id::text = $1 AND period = $2 AND starts = $3::date`, pipelineID, period, day0); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var id string
	if err = tx.QueryRow(ctx, `INSERT INTO crm_sales_plans (pipeline_id, period, starts, metric, updated_by) VALUES ($1::text::uuid, $2, $3::date, $4, nullif($5, '')::uuid)
		ON CONFLICT (pipeline_id, period, starts) DO UPDATE SET metric = $4, updated_by = nullif($5, '')::uuid, updated_at = now() RETURNING id::text`,
		pipelineID, period, day0, t.Metric, by).Scan(&id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM crm_sales_targets WHERE plan_id = $1`, id); err != nil {
		return err
	}
	add := func(dept, user string, v float64) error {
		if v <= 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO crm_sales_targets (plan_id, department_id, user_id, amount) VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4)`,
			id, dept, user, round2(v))
		return err
	}
	if err = add("", "", t.Company); err != nil {
		return err
	}
	for d, v := range t.Departments {
		if err = add(d, "", v); err != nil {
			return err
		}
	}
	for u, v := range t.Users {
		if err = add("", u, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
