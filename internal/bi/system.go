package bi

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// systemVersion is the version of the ready dashboards: a greater one replaces their widgets in the database.
const systemVersion = 3

// SystemDashboard is a ready dashboard: its code (the title is the locale's bi.sys.CODE), period and widgets.
type SystemDashboard struct {
	Code     string
	Period   string
	Sections []string // where it is shown at first, see Sections
	Filters  []string // «Фильтры дашборда»
	Widgets  []Widget
}

func kpi(ds, title string, m Measure, f ...Filter) Widget {
	return Widget{Kind: "kpi", Width: 3, Title: title, Query: Query{Dataset: ds, Measures: []Measure{m}, Filters: f}}
}

func chart(kind string, width int, ds, title string, dims []string, m []Measure, f ...Filter) Widget {
	q := Query{Dataset: ds, Dims: dims, Measures: m, Filters: f}
	if kind == "bar" || kind == "table" {
		if len(dims) == 1 && len(dims[0]) == len(trimGrain(dims[0])) { // not a date: the biggest first, the top 10
			q.Limit = 10
		}
	}
	return Widget{Kind: kind, Width: width, Title: title, Query: q}
}

func trimGrain(d string) string {
	for i := range d {
		if d[i] == ':' {
			return d[:i]
		}
	}
	return d
}

var (
	count = Measure{Agg: "count"}
	sum   = func(f string) Measure { return Measure{Agg: "sum", Field: f} }
	avg   = func(f string) Measure { return Measure{Agg: "avg", Field: f} }
	yes   = func(f string) Filter { return Filter{Field: f, Op: "yes"} }
)

// SystemDashboards are the ready dashboards in the order of the list; the titles of their widgets are locale keys.
var SystemDashboards = []SystemDashboard{
	{Code: "sales", Period: "month", Sections: []string{"crm"}, Filters: []string{"responsible", "pipeline"}, Widgets: []Widget{
		kpi("crm_deal", "bi.sw.deals_new", count),
		kpi("crm_deal", "bi.sw.deals_won", count, yes("won")),
		kpi("crm_deal", "bi.sw.won_amount", sum("amount"), yes("won")),
		kpi("crm_deal", "bi.sw.avg_won", avg("amount"), yes("won")),
		chart("bar", 8, "crm_deal", "bi.sw.amount_by_month", []string{"created_at:month", "semantic"}, []Measure{sum("amount")}),
		chart("pie", 4, "crm_deal", "bi.sw.deals_by_stage", []string{"stage"}, []Measure{count}),
		chart("bar", 6, "crm_deal", "bi.sw.won_by_manager", []string{"responsible"}, []Measure{sum("amount")}, yes("won")),
		chart("table", 6, "crm_deal", "bi.sw.managers", []string{"responsible"}, []Measure{count, sum("amount"), avg("amount")}),
	}},
	{Code: "leads", Period: "month", Sections: []string{"crm"}, Filters: []string{"responsible", "source"}, Widgets: []Widget{
		kpi("crm_lead", "bi.sw.leads_new", count),
		kpi("crm_lead", "bi.sw.leads_converted", count, yes("converted")),
		kpi("crm_contact", "bi.sw.contacts_new", count),
		kpi("crm_company", "bi.sw.companies_new", count),
		chart("line", 8, "crm_lead", "bi.sw.leads_by_day", []string{"created_at:day"}, []Measure{count}),
		chart("pie", 4, "crm_lead", "bi.sw.leads_by_source", []string{"source"}, []Measure{count}),
		chart("bar", 6, "crm_lead", "bi.sw.leads_by_stage", []string{"stage"}, []Measure{count}),
		chart("table", 6, "crm_lead", "bi.sw.sources", []string{"source", "converted"}, []Measure{count, sum("amount")}),
	}},
	{Code: "tasks", Period: "month", Sections: []string{"tasks"}, Filters: []string{"responsible", "project"}, Widgets: []Widget{
		kpi("task", "bi.sw.tasks_new", count),
		kpi("task", "bi.sw.tasks_done", count, yes("completed")),
		kpi("task", "bi.sw.tasks_overdue", count, yes("overdue")),
		kpi("task", "bi.sw.tasks_spent", sum("spent_hours")),
		chart("bar", 8, "task", "bi.sw.tasks_by_week", []string{"created_at:week", "completed"}, []Measure{count}),
		chart("pie", 4, "task", "bi.sw.tasks_by_status", []string{"status"}, []Measure{count}),
		chart("table", 12, "task", "bi.sw.efficiency", []string{"responsible"}, []Measure{count, sum("estimate_hours"), sum("spent_hours")}),
	}},
	{Code: "timeman", Period: "month", Sections: []string{"company"}, Filters: []string{"employee"}, Widgets: []Widget{
		kpi("timeman", "bi.sw.hours", sum("hours")),
		kpi("timeman", "bi.sw.days", count),
		kpi("timeman", "bi.sw.avg_day", avg("hours")),
		kpi("timeman", "bi.sw.avg_start", avg("start_hour")),
		chart("line", 8, "timeman", "bi.sw.hours_by_day", []string{"day:day"}, []Measure{sum("hours")}),
		chart("pie", 4, "timeman", "bi.sw.hours_by_employee", []string{"employee"}, []Measure{sum("hours")}),
		chart("table", 12, "timeman", "bi.sw.employees", []string{"employee"}, []Measure{count, sum("hours"), avg("hours"), sum("pause_hours"), avg("start_hour")}),
	}},
}

// SystemCodes are the codes of the ready dashboards in their order.
func SystemCodes() []string {
	out := make([]string, len(SystemDashboards))
	for i, d := range SystemDashboards {
		out[i] = d.Code
	}
	return out
}

// EnsureSystem puts the ready dashboards into the database, or renews their widgets when go24 has a newer version.
func (s *Store) EnsureSystem(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, d := range SystemDashboards {
			var id string
			var ver int
			err := tx.QueryRow(ctx, `INSERT INTO bi_dashboards (title, period, code, status, sections)
				VALUES ($1, $2, $1, 'published', $3) ON CONFLICT (code) DO UPDATE SET code = EXCLUDED.code RETURNING id, version`,
				d.Code, d.Period, d.Sections).Scan(&id, &ver)
			if err != nil {
				return err
			}
			if ver >= systemVersion {
				continue
			}
			if _, err := tx.Exec(ctx, `DELETE FROM bi_widgets WHERE dashboard_id = $1`, id); err != nil {
				return err
			}
			for i, w := range d.Widgets {
				q, err := json.Marshal(w.Query)
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO bi_widgets (dashboard_id, pos, kind, title, width, query) VALUES ($1, $2, $3, $4, $5, $6)`,
					id, i, w.Kind, w.Title, w.Width, q); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE bi_dashboards SET version = $2, period = $3, filters = coalesce($4::text[], '{}') WHERE id = $1`,
				id, systemVersion, d.Period, d.Filters); err != nil {
				return err
			}
		}
		return nil
	})
}
