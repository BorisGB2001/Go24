package bi

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

var (
	ErrNotFound = i18n.E("err.not_found")
	ErrTitle    = i18n.E("bi.err.title")
	ErrKind     = i18n.E("bi.err.kind")
	ErrPeriod   = i18n.E("bi.err.period")
	ErrRights   = i18n.E("bi.err.rights")
	ErrBody     = i18n.E("bi.err.body")
)

// Kinds of a widget: a number, columns, stacked columns, a line, areas, a pie, a funnel, a table, a pivot table, a text.
var Kinds = []string{"kpi", "bar", "stack", "line", "area", "pie", "funnel", "table", "pivot", "text"}

// Widths of a widget, in columns of the grid of 12: a quarter, a third, a half, two thirds, the whole row.
var Widths = []int{3, 4, 6, 8, 12}

// Periods of a dashboard; range is from DateFrom to DateTo.
var Periods = []string{"all", "today", "week", "month", "quarter", "year", "last_7", "last_30", "prev_month",
	"prev_quarter", "prev_year", "range"}

// Sections a dashboard may be shown in, on their «BI-аналитика» tab: the CRM, the tasks, the company.
var Sections = []string{"crm", "tasks", "company"}

// Dashboard is a page of widgets with the period they all count over.
type Dashboard struct {
	ID, Title, Description string
	Code                   string // a ready dashboard: its title is the locale's bi.sys.CODE, it is only copied
	Period                 string
	DateFrom, DateTo       *time.Time
	CreatedBy, AuthorName  string
	UpdatedAt              time.Time
	Widgets                int
	Status                 string // draft, published
	GroupID, GroupTitle    string
	Access                 string   // the viewer's: view, edit
	Sections               []string // shown in these sections too
	Filters                []string // «Фильтры дашборда»: the fields picked on it besides the period
}

// Widget is a chart of a query on a dashboard; the query holds no period — the dashboard gives it.
type Widget struct {
	ID, DashboardID string
	Pos             int
	Kind, Title     string
	Width           int
	Query           Query
	NoPeriod        bool
	Compare         bool   // a number: its change against the period before, as long
	Body            string // a text: its paragraphs, «# » starts a heading
}

// MaxBody is the longest text of a text widget.
const MaxBody = 10000

// CanEdit: the viewer changes the dashboard's widgets and settings — its author, the administrators and those
// «Права доступа» let; nobody changes a ready one.
func (d Dashboard) CanEdit() bool { return d.Code == "" && d.Access == "edit" }

// CanManage: the author and the administrators publish a dashboard, put it in a group, give it rights, delete it.
func (d Dashboard) CanManage(v Viewer) bool { return d.Code == "" && (v.Admin || d.CreatedBy == v.ID) }

// Span is the period [from, to) at now in loc; nil ends are open (all time, or a range without one of its dates).
func Span(period string, dateFrom, dateTo *time.Time, now time.Time) (from, to *time.Time) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	span := func(a, b time.Time) (*time.Time, *time.Time) { return &a, &b }
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	quarter := time.Date(now.Year(), (now.Month()-1)/3*3+1, 1, 0, 0, 0, 0, now.Location())
	year := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
	switch period {
	case "today":
		return span(day, day.AddDate(0, 0, 1))
	case "week":
		wd := (int(day.Weekday()) + 6) % 7 // from Monday
		return span(day.AddDate(0, 0, -wd), day.AddDate(0, 0, 7-wd))
	case "month":
		return span(month, month.AddDate(0, 1, 0))
	case "quarter":
		return span(quarter, quarter.AddDate(0, 3, 0))
	case "year":
		return span(year, year.AddDate(1, 0, 0))
	case "last_7":
		return span(day.AddDate(0, 0, -6), day.AddDate(0, 0, 1))
	case "last_30":
		return span(day.AddDate(0, 0, -29), day.AddDate(0, 0, 1))
	case "prev_month":
		return span(month.AddDate(0, -1, 0), month)
	case "prev_quarter":
		return span(quarter.AddDate(0, -3, 0), quarter)
	case "prev_year":
		return span(year.AddDate(-1, 0, 0), year)
	case "range":
		if dateFrom != nil {
			a := time.Date(dateFrom.Year(), dateFrom.Month(), dateFrom.Day(), 0, 0, 0, 0, now.Location())
			from = &a
		}
		if dateTo != nil { // the last day is counted whole
			b := time.Date(dateTo.Year(), dateTo.Month(), dateTo.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
			to = &b
		}
	}
	return from, to
}

// Before is the period as long just before [from, to): the months before a span of whole months (a quarter — the
// quarter before), the days before one of whole days; false when the period has an open end.
func Before(from, to *time.Time) (a, b time.Time, ok bool) {
	if from == nil || to == nil || !to.After(*from) {
		return a, b, false
	}
	f, t := *from, *to
	if f.Day() == 1 && t.Day() == 1 && f.Hour() == 0 && t.Hour() == 0 && f.Minute() == 0 && t.Minute() == 0 {
		if n := (t.Year()-f.Year())*12 + int(t.Month()-f.Month()); n > 0 {
			return f.AddDate(0, -n, 0), f, true
		}
	}
	if days := int(math.Round(t.Sub(f).Hours() / 24)); days > 0 && f.AddDate(0, 0, days).Equal(t) {
		return f.AddDate(0, 0, -days), f, true
	}
	return f.Add(-t.Sub(f)), f, true
}

// dashboardSQL gives the dashboards the viewer ($1, $2 — an administrator) sees: the ready ones are everyone's to view,
// the author and the administrators change theirs, the others have what «Права доступа» give (none — every
// employee views it); a draft is seen only by those who may change it.
const dashboardSQL = `SELECT d.id, d.title, d.description, coalesce(d.code, ''), d.period, d.date_from, d.date_to,
		coalesce(d.created_by::text, ''), coalesce(u.name, ''), d.updated_at,
		(SELECT count(*) FROM bi_widgets w WHERE w.dashboard_id = d.id)::int,
		d.status, coalesce(d.group_id::text, ''), coalesce(g.title, ''), a.access, d.sections, d.filters
	FROM bi_dashboards d LEFT JOIN users u ON u.id = d.created_by LEFT JOIN bi_groups g ON g.id = d.group_id
	CROSS JOIN LATERAL (SELECT CASE WHEN d.code IS NOT NULL THEN 'view'
		WHEN $2::bool OR d.created_by = nullif($1, '')::uuid THEN 'edit'
		ELSE coalesce(bi_access(d.id, d.group_id, nullif($1, '')::uuid), 'view') END AS access) a
	WHERE a.access <> 'none' AND (d.status = 'published' OR a.access = 'edit')`

func scanDashboard(r pgx.Row) (Dashboard, error) {
	var d Dashboard
	err := r.Scan(&d.ID, &d.Title, &d.Description, &d.Code, &d.Period, &d.DateFrom, &d.DateTo, &d.CreatedBy, &d.AuthorName,
		&d.UpdatedAt, &d.Widgets, &d.Status, &d.GroupID, &d.GroupTitle, &d.Access, &d.Sections, &d.Filters)
	return d, err
}

// Dashboards the viewer sees: the ready ones in their order, then the others, the last changed first; q — those
// whose title has it (the ready ones are always given: their titles are the locale's); group — of that group only
// (the ready ones are in none).
func (s *Store) Dashboards(ctx context.Context, v Viewer, q, group string) ([]Dashboard, error) {
	rows, err := s.db.Query(ctx, dashboardSQL+` AND ($3 = '' OR d.code IS NOT NULL OR d.title ILIKE '%' || $3 || '%')
		AND ($5 = '' OR d.group_id::text = $5)
		ORDER BY d.code IS NULL, array_position($4::text[], d.code), d.updated_at DESC`,
		v.ID, v.Admin, strings.TrimSpace(q), SystemCodes(), group)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Dashboard, error) { return scanDashboard(r) })
}

// Pinned are the dashboards the viewer sees in a section: the ready ones first, then by title.
func (s *Store) Pinned(ctx context.Context, v Viewer, section string) ([]Dashboard, error) {
	rows, err := s.db.Query(ctx, dashboardSQL+` AND $3 = ANY(d.sections)
		ORDER BY d.code IS NULL, array_position($4::text[], d.code), lower(d.title)`, v.ID, v.Admin, section, SystemCodes())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Dashboard, error) { return scanDashboard(r) })
}

// Pin sets the sections a dashboard is shown in (of Sections; the others are dropped).
func (s *Store) Pin(ctx context.Context, id string, sections []string) error {
	list := []string{}
	for _, x := range Sections {
		if slices.Contains(sections, x) {
			list = append(list, x)
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE bi_dashboards SET sections = $2 WHERE id::text = $1`, id, list)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Dashboard is a dashboard the viewer sees.
func (s *Store) Dashboard(ctx context.Context, v Viewer, id string) (Dashboard, error) {
	d, err := scanDashboard(s.db.QueryRow(ctx, dashboardSQL+` AND d.id::text = $3`, v.ID, v.Admin, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}

// SaveDashboard adds a dashboard (no ID) by the user or changes one, its filters too.
func (s *Store) SaveDashboard(ctx context.Context, by string, d Dashboard) (string, error) {
	d.Title = strings.TrimSpace(d.Title)
	if d.Title == "" {
		return "", ErrTitle
	}
	if d.Period == "" {
		d.Period = "month"
	}
	if !slices.Contains(Periods, d.Period) {
		return "", ErrPeriod
	}
	if d.Period != "range" {
		d.DateFrom, d.DateTo = nil, nil
	} else if d.DateFrom != nil && d.DateTo != nil && d.DateTo.Before(*d.DateFrom) {
		return "", ErrPeriod
	}
	filters := []string{}
	for _, f := range d.Filters {
		if f != "" && !slices.Contains(filters, f) {
			filters = append(filters, f)
		}
	}
	if d.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO bi_dashboards (title, description, period, date_from, date_to, created_by, filters)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, d.Title, d.Description, d.Period, d.DateFrom, d.DateTo, by,
			filters).Scan(&d.ID)
		return d.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE bi_dashboards SET title = $2, description = $3, period = $4, date_from = $5,
		date_to = $6, filters = $7, updated_at = now() WHERE id = $1 AND code IS NULL`, d.ID, d.Title, d.Description,
		d.Period, d.DateFrom, d.DateTo, filters)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return d.ID, err
}

// CopyDashboard makes a copy of a dashboard with its widgets, the user its author, a draft in no group and with no
// rights of its own; name gives the titles of the
// ready dashboard's widgets (locale keys) in the copy.
func (s *Store) CopyDashboard(ctx context.Context, by, id, title string, name func(string) string) (string, error) {
	var nid string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `INSERT INTO bi_dashboards (title, description, period, date_from, date_to, created_by, filters)
			SELECT $2, description, period, date_from, date_to, $3, filters FROM bi_dashboards WHERE id::text = $1 RETURNING id`,
			id, title, by).Scan(&nid)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO bi_widgets (dashboard_id, pos, kind, title, width, query, no_period, compare, body)
			SELECT $2, pos, kind, title, width, query, no_period, compare, body FROM bi_widgets WHERE dashboard_id = $1`, id, nid)
		if err != nil || name == nil {
			return err
		}
		for _, d := range SystemDashboards {
			for _, w := range d.Widgets {
				if _, err := tx.Exec(ctx, `UPDATE bi_widgets SET title = $3 WHERE dashboard_id = $1 AND title = $2`,
					nid, w.Title, name(w.Title)); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return nid, err
}

func (s *Store) DeleteDashboard(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM bi_dashboards WHERE id::text = $1 AND code IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

const widgetSQL = `SELECT id, dashboard_id, pos, kind, title, width, query, no_period, compare, body FROM bi_widgets`

func scanWidget(r pgx.Row) (Widget, error) {
	var w Widget
	var q []byte
	if err := r.Scan(&w.ID, &w.DashboardID, &w.Pos, &w.Kind, &w.Title, &w.Width, &q, &w.NoPeriod, &w.Compare, &w.Body); err != nil {
		return w, err
	}
	return w, json.Unmarshal(q, &w.Query)
}

// Widgets of a dashboard in the order of its grid.
func (s *Store) Widgets(ctx context.Context, dashboard string) ([]Widget, error) {
	rows, err := s.db.Query(ctx, widgetSQL+` WHERE dashboard_id::text = $1 ORDER BY pos, created_at`, dashboard)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Widget, error) { return scanWidget(r) })
}

func (s *Store) Widget(ctx context.Context, id string) (Widget, error) {
	w, err := scanWidget(s.db.QueryRow(ctx, widgetSQL+` WHERE id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return w, err
}

// CheckWidget: the kind, the width and the query make a widget (the query is tried on no rows' worth of checking —
// its data set, fields and functions).
func (s *Store) CheckWidget(ctx context.Context, w Widget) error {
	if !slices.Contains(Kinds, w.Kind) {
		return ErrKind
	}
	if !slices.Contains(Widths, w.Width) {
		return ErrKind
	}
	if w.Kind == "text" { // no query: the text of the widget
		if len([]rune(w.Body)) > MaxBody {
			return ErrBody
		}
		return nil
	}
	return s.Check(ctx, w.Query)
}

// SaveWidget adds a widget at the end of its dashboard (no ID) or changes one; the period is the dashboard's.
func (s *Store) SaveWidget(ctx context.Context, w Widget) (string, error) {
	w.Title = strings.TrimSpace(w.Title)
	w.Query.From, w.Query.To = nil, nil
	if err := s.CheckWidget(ctx, w); err != nil {
		return "", err
	}
	q, err := json.Marshal(w.Query)
	if err != nil {
		return "", err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if w.ID == "" {
			err := tx.QueryRow(ctx, `INSERT INTO bi_widgets (dashboard_id, pos, kind, title, width, query, no_period, compare, body)
				VALUES ($1, (SELECT coalesce(max(pos) + 1, 0) FROM bi_widgets WHERE dashboard_id = $1), $2, $3, $4, $5, $6, $7, $8)
				RETURNING id`, w.DashboardID, w.Kind, w.Title, w.Width, q, w.NoPeriod, w.Compare, w.Body).Scan(&w.ID)
			if err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `UPDATE bi_widgets SET kind = $2, title = $3, width = $4, query = $5, no_period = $6,
				compare = $7, body = $8 WHERE id = $1`, w.ID, w.Kind, w.Title, w.Width, q, w.NoPeriod, w.Compare, w.Body)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return ErrNotFound
			}
		}
		_, err := tx.Exec(ctx, `UPDATE bi_dashboards SET updated_at = now() WHERE id = $1`, w.DashboardID)
		return err
	})
	return w.ID, err
}

// CopyWidget puts a copy of a widget right after it.
func (s *Store) CopyWidget(ctx context.Context, id string) (string, error) {
	w, err := s.Widget(ctx, id)
	if err != nil {
		return "", err
	}
	var nid string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE bi_widgets SET pos = pos + 1 WHERE dashboard_id = $1 AND pos > $2`,
			w.DashboardID, w.Pos); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO bi_widgets (dashboard_id, pos, kind, title, width, query, no_period, compare, body)
			SELECT dashboard_id, pos + 1, kind, title, width, query, no_period, compare, body FROM bi_widgets WHERE id = $1 RETURNING id`,
			id).Scan(&nid)
	})
	return nid, err
}

func (s *Store) DeleteWidget(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM bi_widgets WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// OrderWidgets places the widgets of a dashboard in the order of ids; those not named keep theirs after them.
func (s *Store) OrderWidgets(ctx context.Context, dashboard string, ids []string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE bi_widgets w SET pos = coalesce(o.n, $3 + w.pos)
			FROM bi_widgets x LEFT JOIN unnest($2::text[]) WITH ORDINALITY AS o(id, n) ON o.id = x.id::text
			WHERE x.id = w.id AND w.dashboard_id::text = $1`, dashboard, ids, len(ids))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE bi_dashboards SET updated_at = now() WHERE id::text = $1`, dashboard)
		return err
	})
}

// MoveWidget moves a widget one place earlier (by < 0) or later in its dashboard.
func (s *Store) MoveWidget(ctx context.Context, id string, by int) error {
	w, err := s.Widget(ctx, id)
	if err != nil {
		return err
	}
	list, err := s.Widgets(ctx, w.DashboardID)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(list, func(x Widget) bool { return x.ID == id })
	j := i + 1
	if by < 0 {
		j = i - 1
	}
	if j < 0 || j >= len(list) {
		return nil
	}
	list[i], list[j] = list[j], list[i]
	ids := make([]string, len(list))
	for k, x := range list {
		ids[k] = x.ID
	}
	return s.OrderWidgets(ctx, w.DashboardID, ids)
}
