package bi

import (
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

type Store struct {
	db  *pgxpool.Pool
	CRM *crm.Store
	TZ  string // the time zone the dates are grouped in (TZ of the container; "" — that of the database)
}

func NewStore(db *pgxpool.Pool, c *crm.Store) *Store {
	return &Store{db: db, CRM: c, TZ: os.Getenv("TZ")}
}

// Grains of a date field: the rows are grouped by its day, week, month, quarter or year.
var Grains = []string{"day", "week", "month", "quarter", "year"}

// Aggregates of a measure: count needs no field, count_distinct any one, the others a number.
var Aggregates = []string{"count", "count_distinct", "sum", "avg", "min", "max"}

// Filter ops: eq, ne, in (Values; "" — an empty value), contains; gt, gte, lt, lte (a number or a date); empty, set;
// yes, no (a flag); within — a date in the day, week, month, quarter or year of Value ("month:2026-10-01").
var filterOps = map[string]bool{"eq": true, "ne": true, "in": true, "contains": true, "gt": true, "gte": true, "lt": true,
	"lte": true, "empty": true, "set": true, "yes": true, "no": true, "within": true}

// Measure is an aggregate of a field.
type Measure struct {
	Agg   string `json:"agg"`
	Field string `json:"field,omitempty"`
}

// Filter is a condition on a field (its value as read, or the text of the key).
type Filter struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
}

// Query is what a widget asks of a data set: the dimensions to group by ("field" or "field:grain" for a date), the
// measures, the filters, and the period [From, To) on DateField (the set's own by default).
type Query struct {
	Dataset   string     `json:"dataset"`
	Dims      []string   `json:"dims,omitempty"`
	Measures  []Measure  `json:"measures,omitempty"`
	Filters   []Filter   `json:"filters,omitempty"`
	From      *time.Time `json:"from,omitempty"`
	To        *time.Time `json:"to,omitempty"`
	DateField string     `json:"date_field,omitempty"`
	Sort      int        `json:"sort,omitempty"` // a column from 1, negative — descending; 0 — by the date, else the first measure
	Limit     int        `json:"limit,omitempty"`

	raw    bool // the rows themselves, field by field, not grouped (an export)
	offset int
}

// Export is what an external BI reads of a data set: its rows with the fields (none — all; the row's id always
// comes first) over the period [From, To) on DateField and the filters; Offset and Limit page them.
type Export struct {
	Dataset   string
	Fields    []string
	Filters   []Filter
	From, To  *time.Time
	DateField string
	Limit     int
	Offset    int
}

// Column is a column of a result: a dimension (Field, Grain) or a measure (Agg of Field).
type Column struct {
	Field string `json:"field,omitempty"`
	Type  string `json:"type"`
	Grain string `json:"grain,omitempty"`
	Agg   string `json:"agg,omitempty"`
}

// Result is the table of a query: the dimensions' values as read (strings, a date as 2006-01-02), the measures'
// as numbers.
type Result struct {
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
	// Keys are what a row's dimensions are filtered by (a Filter "in", a date's "within"), one for each dimension
	Keys [][]string `json:"-"`
}

const (
	maxRows   = 5000
	MaxExport = 50000 // rows of an export at a time
)

// run is a query of a viewer: the reading conditions of the sections take their people from it.
type run struct {
	ctx    context.Context
	s      *Store
	viewer string
}

func (r *run) scope(entity, alias string) (string, error) {
	return r.s.CRM.ReadScope(r.ctx, r.viewer, entity, alias, 1)
}

// TaskScope is the tasks' visibility rule over the aliases t and p.
func (r *run) TaskScope(u, admin string) string { return tasks.VisibleSQL(u, admin) }

// dim is a dimension of a built query: its field and the grain of a date.
type dim struct {
	f     Field
	grain string
}

// built is the SQL of a query with its arguments ($1 — the viewer, $2 — the administrator mode), the dimensions and
// the columns of the result.
type built struct {
	sql  string
	args []any
	dims []dim
	res  Result
	raw  bool
}

// Check: the query names a data set, fields and functions it may (no rows are read).
func (q Query) Check() error { return q.check(DatasetOf(q.Dataset)) }

func (q Query) check(ds *Dataset) error {
	_, err := q.build(ds, func(*Dataset) (string, error) { return "true", nil }, nil)
	return err
}

// Check: the query names a data set (a file's too), fields and functions it may.
func (s *Store) Check(ctx context.Context, q Query) error {
	ds, err := s.Dataset(ctx, q.Dataset)
	if err != nil {
		return err
	}
	return q.check(ds)
}

// Run makes the query for the viewer (administrator mode — the tasks' admin of the context); tr reads the codes of
// the locale (stages, statuses, dictionary items).
func (s *Store) Run(ctx context.Context, viewer string, q Query, tr func(string) string) (Result, error) {
	ds, err := s.Dataset(ctx, q.Dataset)
	if err != nil {
		return Result{}, err
	}
	b, err := q.build(ds, func(ds *Dataset) (string, error) { return ds.scope(&run{ctx, s, viewer}) },
		[]any{viewer, tasks.IsAdmin(ctx)})
	if err != nil {
		return Result{}, err
	}
	return s.exec(ctx, b, len(q.Measures), tr)
}

// Rows reads an export for the viewer: the dates come with their time, the flags and numbers as they are.
func (s *Store) Rows(ctx context.Context, viewer string, e Export, tr func(string) string) (Result, error) {
	ds, err := s.Dataset(ctx, e.Dataset)
	if err != nil {
		return Result{}, err
	}
	if ds == nil {
		return Result{}, i18n.E("bi.err.dataset")
	}
	fields := []string{"id"}
	if len(e.Fields) == 0 {
		for _, f := range ds.Fields {
			fields = append(fields, f.Code)
		}
	}
	for _, f := range e.Fields {
		if !slices.Contains(fields, f) {
			fields = append(fields, f)
		}
	}
	return s.Run(ctx, viewer, Query{Dataset: e.Dataset, Dims: fields, Filters: e.Filters, From: e.From, To: e.To,
		DateField: e.DateField, Limit: e.Limit, raw: true, offset: max(e.Offset, 0)}, tr)
}

// build makes the SQL of the query; scope is the reading condition of the data set.
func (q Query) build(ds *Dataset, scope func(*Dataset) (string, error), args []any) (built, error) {
	var res Result
	fail := func(err error) (built, error) { return built{}, err }
	if ds == nil {
		return fail(i18n.E("bi.err.dataset"))
	}
	if len(q.Dims)+len(q.Measures) == 0 {
		return fail(i18n.E("bi.err.empty"))
	}
	field := func(code string) (Field, error) {
		f, ok := ds.Field(code)
		if code == "id" && q.raw {
			f, ok = Field{Code: "id", Type: String, Expr: ds.id + "::text"}, true
		}
		if !ok {
			return f, i18n.E("bi.err.field", code)
		}
		return f, nil
	}
	if args == nil {
		args = []any{"", false}
	}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var (
		cols, group []string
		dims        []dim
		pos         []int // the SQL position of each result column
	)
	for _, d := range q.Dims {
		code, grain, _ := strings.Cut(d, ":")
		f, err := field(code)
		if err != nil {
			return fail(err)
		}
		if !f.Dim() && !q.raw {
			return fail(i18n.E("bi.err.dim", code))
		}
		c := Column{Field: code, Type: f.Type}
		pos = append(pos, len(cols)+1)
		switch {
		case q.raw && f.Type == Date:
			cols = append(cols, "to_char("+f.Expr+", 'YYYY-MM-DD HH24:MI:SS')")
		case q.raw && f.Type == Number:
			cols = append(cols, "round(("+f.Expr+")::numeric, 2)::float8")
		case q.raw && f.Type == Bool:
			cols = append(cols, f.Expr)
		case f.Type == Date:
			if grain == "" {
				grain = "day"
			}
			if !slices.Contains(Grains, grain) {
				return fail(i18n.E("bi.err.grain"))
			}
			c.Grain = grain
			cols = append(cols, "to_char(date_trunc('"+grain+"', "+f.Expr+"), 'YYYY-MM-DD')")
		case f.CodeOf != "":
			cols = append(cols, "coalesce("+f.Expr+", '')", "coalesce("+f.CodeOf+", '')")
		case f.Type == Bool:
			cols = append(cols, "coalesce("+f.Expr+", false)")
		default:
			cols = append(cols, "coalesce("+f.Expr+", '')")
		}
		dims = append(dims, dim{f, grain})
		res.Columns = append(res.Columns, c)
	}
	group = make([]string, len(cols))
	for i := range cols {
		group[i] = strconv.Itoa(i + 1)
	}
	for _, m := range q.Measures {
		c := Column{Field: m.Field, Type: Number, Agg: m.Agg}
		var sql string
		switch m.Agg {
		case "count":
			sql = "count(*)::float8"
		case "count_distinct", "sum", "avg", "min", "max":
			f, err := field(m.Field)
			if err != nil {
				return fail(err)
			}
			if m.Agg == "count_distinct" {
				sql = "count(DISTINCT " + f.key() + ")::float8"
				break
			}
			if f.Type != Number {
				return fail(i18n.E("bi.err.measure", m.Field))
			}
			// rounded to cents: amounts converted to the base currency come with float noise
			sql = "round(coalesce(" + m.Agg + "(" + f.Expr + "), 0)::numeric, 2)::float8"
		default:
			return fail(i18n.E("bi.err.agg"))
		}
		pos = append(pos, len(cols)+1)
		cols = append(cols, sql)
		res.Columns = append(res.Columns, c)
	}

	sc, err := scope(ds)
	if err != nil {
		return fail(err)
	}
	where := []string{"(" + sc + ")", "($1::text IS NOT NULL OR $2::bool OR true)"} // every query names both
	df := ds.Period
	if q.DateField != "" {
		df = q.DateField
	}
	if (q.From != nil || q.To != nil) && df != "" { // a file without dates is read over all time
		f, err := field(df)
		if err != nil {
			return fail(err)
		}
		if f.Type != Date {
			return fail(i18n.E("bi.err.date", df))
		}
		if q.From != nil {
			where = append(where, f.Expr+" >= "+arg(*q.From))
		}
		if q.To != nil {
			where = append(where, f.Expr+" < "+arg(*q.To))
		}
	}
	for _, fl := range q.Filters {
		f, err := field(fl.Field)
		if err != nil {
			return fail(err)
		}
		if !filterOps[fl.Op] {
			return fail(i18n.E("bi.err.op"))
		}
		k := f.key()
		if f.Type != String && f.Type != Number && f.Type != Date {
			k = "(" + k + ")::text"
		}
		cast := "::text"
		switch f.Type {
		case Number:
			cast = "::float8"
		case Date:
			cast = "::timestamptz"
		}
		var c string
		switch fl.Op {
		case "eq", "ne", "gt", "gte", "lt", "lte":
			if f.Type == Date || f.Type == Number {
				if _, err := strconv.ParseFloat(fl.Value, 64); f.Type == Number && err != nil {
					return fail(i18n.E("bi.err.value", fl.Field))
				}
				if _, err := time.Parse("2006-01-02", fl.Value); f.Type == Date && err != nil {
					return fail(i18n.E("bi.err.value", fl.Field))
				}
			} else {
				cast = "::text"
			}
			c = k + map[string]string{"eq": " = ", "ne": " IS DISTINCT FROM ", "gt": " > ", "gte": " >= ", "lt": " < ", "lte": " <= "}[fl.Op] + arg(fl.Value) + cast
		case "in":
			if f.Type == Bool { // as grouped: no flag is «no»
				k = "coalesce(" + f.Expr + ", false)"
			}
			c = "coalesce(" + k + "::text, '') = ANY(" + arg(fl.Values) + "::text[])"
		case "within":
			a, b, ok := Within(fl.Value)
			if f.Type != Date || !ok {
				return fail(i18n.E("bi.err.value", fl.Field))
			}
			c = f.Expr + " >= " + arg(a) + "::timestamptz AND " + f.Expr + " < " + arg(b) + "::timestamptz"
		case "contains":
			c = k + "::text ILIKE '%' || " + arg(fl.Value) + " || '%'"
		case "empty":
			c = "coalesce(" + k + "::text, '') = ''"
		case "set":
			c = "coalesce(" + k + "::text, '') <> ''"
		case "yes", "no":
			if f.Type != Bool {
				return fail(i18n.E("bi.err.op"))
			}
			c = "(" + f.Expr + ") IS " + map[string]string{"yes": "TRUE", "no": "NOT TRUE"}[fl.Op]
		}
		where = append(where, c)
	}

	sql := "SELECT " + strings.Join(cols, ", ") + " FROM " + ds.from + " WHERE " + strings.Join(where, " AND ")
	if len(dims) > 0 && !q.raw {
		sql += " GROUP BY " + strings.Join(group, ", ")
	}
	switch {
	case q.Sort != 0:
		n := q.Sort
		if n < 0 {
			n = -n
		}
		if n > len(pos) {
			return fail(i18n.E("bi.err.sort"))
		}
		sql += " ORDER BY " + strconv.Itoa(pos[n-1])
		if q.Sort < 0 {
			sql += " DESC"
		}
	case q.raw:
		sql += " ORDER BY 1"
	case len(dims) > 0 && dims[0].f.Type == Date:
		sql += " ORDER BY 1"
	case len(dims) > 0 && len(q.Measures) > 0:
		sql += " ORDER BY " + strconv.Itoa(pos[len(dims)]) + " DESC, 1"
	case len(dims) > 0:
		sql += " ORDER BY 1"
	}
	limit, most := q.Limit, maxRows
	if q.raw {
		most = MaxExport
	}
	if limit <= 0 || limit > most {
		limit = most
	}
	sql += " LIMIT " + strconv.Itoa(limit)
	if q.offset > 0 {
		sql += " OFFSET " + strconv.Itoa(q.offset)
	}
	return built{sql, args, dims, res, q.raw}, nil
}

// exec reads the rows of a built query of n measures.
func (s *Store) exec(ctx context.Context, b built, n int, tr func(string) string) (Result, error) {
	res, dims := b.res, b.dims

	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if s.TZ != "" {
		if _, err := tx.Exec(ctx, `SELECT set_config('TimeZone', $1, true)`, s.TZ); err != nil {
			return res, err
		}
	}
	rows, err := tx.Query(ctx, b.sql, b.args...)
	if err != nil {
		return res, err
	}
	defer rows.Close()
	res.Rows = [][]any{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return res, err
		}
		row := make([]any, 0, len(res.Columns))
		var keys []string
		i := 0
		for _, d := range dims {
			var v string
			key := ""
			switch {
			case b.raw && (d.f.Type == Number || d.f.Type == Bool || d.f.Type == Date):
				row = append(row, vals[i])
				i++
				continue
			case d.f.Type == Bool:
				key = strconv.FormatBool(vals[i] == true)
				v = tr(map[bool]string{true: "bi.yes", false: "bi.no"}[vals[i] == true])
			case d.f.CodeOf != "":
				v, _ = vals[i].(string)
				key = v
				if code, _ := vals[i+1].(string); v == "" && code != "" {
					v, key = tr(d.f.Prefix+code), code
				}
				i++
			case d.f.Type == Date:
				v, _ = vals[i].(string)
				key = d.grain + ":" + v
			default:
				v, _ = vals[i].(string)
				key = v
				if d.f.Prefix != "" && v != "" {
					v = tr(d.f.Prefix + v)
				}
			}
			i++
			row = append(row, v)
			keys = append(keys, key)
		}
		if !b.raw {
			res.Keys = append(res.Keys, keys)
		}
		for range n {
			row = append(row, vals[i])
			i++
		}
		res.Rows = append(res.Rows, row)
	}
	return res, rows.Err()
}
