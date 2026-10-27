// Package bi is the «BI Конструктор»: the data sets of the sections (deals, their goods, smart processes,
// leads, contacts, companies, CRM activities, invoices, quotes, tasks, working time, employees, calls, bookings), each a table of fields to group by and to measure, and the queries over them
// that the dashboards' widgets show. A viewer only ever counts the rows they may read in the section itself.
package bi

import "slices"

// Field types.
const (
	String = "string"
	Number = "number"
	Date   = "date"
	Bool   = "bool"
)

// Field is a column of a data set: Expr is its SQL over the set's aliases. A field with Code reads as Expr, or —
// when that is empty — as the locale's Prefix+Code (a built-in stage, a dictionary item); one with Prefix alone is
// an enumeration whose value is always read from the locale (a task's status).
type Field struct {
	Code   string `json:"code"`
	Name   string `json:"name,omitempty"` // a file's column names itself
	Type   string `json:"type"`
	Expr   string `json:"-"`
	CodeOf string `json:"-"`
	Prefix string `json:"-"`
	Order  string `json:"-"` // the SQL of its values' own order (the stages' in their pipeline), else by the value
}

// Dim: the field can group the rows (a measure needs a number).
func (f Field) Dim() bool { return f.Type != Number }

// key is the SQL the rows are grouped and filtered by.
func (f Field) key() string {
	if f.CodeOf != "" {
		return "coalesce(nullif(" + f.Expr + ", ''), " + f.CodeOf + ", '')"
	}
	return f.Expr
}

// Dataset is a data set: its FROM over the section's tables, the fields, the date the period applies to by default,
// and the reading condition of a viewer (the SQL with the placeholders $1 — the viewer, $2 — the administrator mode).
type Dataset struct {
	Code    string  `json:"code"`
	Name    string  `json:"name,omitempty"`    // a file's set names itself
	Section string  `json:"section,omitempty"` // the section of the portal the set is of: crm, tasks, company, telephony, booking
	Fields  []Field `json:"fields"`
	Period  string  `json:"period"` // the field of the period
	id      string  // the row's id, which an export gives first
	from    string
	scope   func(r *run) (string, error)
}

// Field is the set's field by its code.
func (d *Dataset) Field(code string) (Field, bool) {
	for _, f := range d.Fields {
		if f.Code == code {
			return f, true
		}
	}
	return Field{}, false
}

// inBase is an amount in the base currency, as the CRM counts its sums.
func inBase(amount, currency string) string {
	return `(` + amount + ` * coalesce((SELECT cr.rate FROM crm_currencies cr WHERE cr.code = ` + currency + `), 1))::float8`
}

// dict is a field of a dictionary item joined as j.
func dict(code, j string) Field {
	return Field{Code: code, Type: String, Expr: j + ".name", CodeOf: j + ".code", Prefix: "crm.dict."}
}

func dictJoin(j, col string) string { return ` LEFT JOIN crm_dict ` + j + ` ON ` + j + `.id = ` + col }

// crmScope is the CRM's reading condition of the viewer on a kind of card as alias.
func crmScope(entity, alias string) func(r *run) (string, error) {
	return func(r *run) (string, error) { return r.scope(entity, alias) }
}

// dealScope reads the deals (smart: the cards of smart processes, which are deals of their pipelines pl) the viewer
// may read as d.
func dealScope(smart bool) func(r *run) (string, error) {
	return func(r *run) (string, error) {
		sc, err := r.scope("deal", "d")
		return "pl.type_id IS " + map[bool]string{true: "NOT NULL", false: "NULL"}[smart] + " AND (" + sc + ")", err
	}
}

// staffScope: one's own rows (the employee in col) and those of the departments one heads, with their
// sub-departments; administrators — all.
func staffScope(col string) func(r *run) (string, error) {
	return func(r *run) (string, error) {
		return `$2::bool OR ` + col + `::text = $1 OR ` + col + ` IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (
				WITH RECURSIVE h AS (SELECT id FROM departments WHERE head_id::text = $1
					UNION SELECT d.id FROM departments d JOIN h ON d.parent_id = h.id) SELECT id FROM h))`, nil
	}
}

// everyone: the set is read by every employee (as the section shows it to all).
func everyone(*run) (string, error) { return "true", nil }

const dealFrom = `crm_deals d JOIN crm_stages st ON st.id = d.stage_id JOIN crm_pipelines pl ON pl.id = d.pipeline_id
	JOIN users u ON u.id = d.responsible_id LEFT JOIN crm_companies co ON co.id = d.company_id
	LEFT JOIN crm_contacts ct ON ct.id = d.contact_id`

// dealFields are the fields of a deal d (or a smart process's card) common to the sets of deals, of smart processes
// and of the goods of deals.
var dealFields = []Field{
	{Code: "pipeline", Type: String, Expr: "pl.name", CodeOf: "pl.code", Prefix: "crm.pipeline."},
	{Code: "stage", Type: String, Expr: "st.name", CodeOf: "st.code", Prefix: "crm.stage.", Order: "st.sort"},
	{Code: "semantic", Type: String, Expr: "st.semantic", Prefix: "bi.semantic."},
	{Code: "responsible", Type: String, Expr: "u.name"},
	{Code: "company", Type: String, Expr: "coalesce(co.title, '')"},
	{Code: "contact", Type: String, Expr: "coalesce(ct.name, '')"},
}

var dealDates = []Field{
	{Code: "created_at", Type: Date, Expr: "d.created_at"},
	{Code: "closed_at", Type: Date, Expr: "d.closed_at"},
	{Code: "close_date", Type: Date, Expr: "d.close_date::timestamptz"},
	{Code: "won", Type: Bool, Expr: "st.semantic = 'success'"},
	{Code: "closed", Type: Bool, Expr: "st.semantic <> 'process'"},
}

func fields(parts ...[]Field) []Field { return slices.Concat(parts...) }

// invoiceSet is the set of the invoices or of the quotes (kind) as i: those of the deals and leads the viewer may read.
func invoiceSet(code, kind string, own []Field) *Dataset {
	return &Dataset{Code: code, Section: "crm", id: "i.id", Period: "issue_date",
		from: `crm_invoices i JOIN users u ON u.id = i.responsible_id LEFT JOIN crm_deals d ON d.id = i.deal_id
			LEFT JOIN crm_leads l ON l.id = i.lead_id LEFT JOIN crm_companies co ON co.id = coalesce(d.company_id, l.company_id)`,
		scope: func(r *run) (string, error) {
			deals, err := r.scope("deal", "d")
			if err != nil {
				return "", err
			}
			leads, err := r.scope("lead", "l")
			return "i.kind = '" + kind + "' AND (i.deal_id IS NOT NULL AND (" + deals + ") OR i.lead_id IS NOT NULL AND (" + leads + "))", err
		},
		Fields: fields([]Field{
			{Code: "number", Type: String, Expr: "i.number::text"},
			{Code: "title", Type: String, Expr: "i.title"},
			{Code: "status", Type: String, Expr: "i.status", Prefix: "crm.inv."},
			{Code: "deal", Type: String, Expr: "coalesce(d.title, '')"},
			{Code: "lead", Type: String, Expr: "coalesce(l.title, '')"},
			{Code: "company", Type: String, Expr: "coalesce(co.title, nullif(l.company_title, ''), '')"},
			{Code: "responsible", Type: String, Expr: "u.name"},
			{Code: "currency", Type: String, Expr: "i.currency"},
			{Code: "issue_date", Type: Date, Expr: "i.issue_date::timestamptz"},
			{Code: "created_at", Type: Date, Expr: "i.created_at"},
			{Code: "amount", Type: Number, Expr: inBase("i.amount", "i.currency")},
		}, own)}
}

// paidSQL is what an invoice i has been paid, in its currency.
const paidSQL = `(SELECT coalesce(sum(p.amount), 0) FROM crm_invoice_payments p WHERE p.invoice_id = i.id)`

// SetSections are the sections of the data sets, in the order of the constructor.
var SetSections = []string{"crm", "tasks", "company", "telephony", "booking"}

// Datasets are the data sets, in the order of the constructor; their codes are those.
var Datasets = []*Dataset{
	{Code: "crm_deal", Section: "crm", id: "d.id", Period: "created_at",
		from:  dealFrom + dictJoin("src", "d.source_id") + dictJoin("dt", "d.deal_type_id"),
		scope: dealScope(false),
		Fields: fields([]Field{{Code: "title", Type: String, Expr: "d.title"}}, dealFields, []Field{
			dict("source", "src"),
			dict("deal_type", "dt"),
			{Code: "currency", Type: String, Expr: "d.currency"},
		}, dealDates, []Field{
			{Code: "amount", Type: Number, Expr: inBase("d.amount", "d.currency")},
			{Code: "cycle_days", Type: Number, Expr: "(extract(epoch FROM d.closed_at - d.created_at) / 86400)::float8"},
		})},
	{Code: "crm_deal_product_row", Section: "crm", id: "r.id", Period: "created_at",
		from: `crm_product_rows r JOIN ` + dealFrom + ` ON r.entity = 'deal' AND d.id = r.entity_id
			LEFT JOIN crm_products pr ON pr.id = r.product_id LEFT JOIN crm_products pp ON pp.id = pr.parent_id
			LEFT JOIN crm_product_sections sec ON sec.id = coalesce(pp.section_id, pr.section_id)`,
		scope: dealScope(false),
		Fields: fields([]Field{
			{Code: "product", Type: String, Expr: "r.name"},
			{Code: "section", Type: String, Expr: "coalesce(sec.name, '')"},
			{Code: "product_kind", Type: String, Expr: "coalesce(pp.kind, pr.kind, 'product')", Prefix: "crm.kind."},
			{Code: "deal", Type: String, Expr: "d.title"},
		}, dealFields, dealDates, []Field{
			{Code: "price", Type: Number, Expr: inBase("r.price", "d.currency")},
			{Code: "quantity", Type: Number, Expr: "r.quantity::float8"},
			{Code: "discount", Type: Number, Expr: "r.discount::float8"},
			{Code: "row_sum", Type: Number, Expr: inBase("(r.price * r.quantity * (1 - r.discount / 100))", "d.currency")},
		})},
	{Code: "crm_dynamic_items", Section: "crm", id: "d.id", Period: "created_at",
		from:  dealFrom + ` JOIN crm_types ty ON ty.id = pl.type_id`,
		scope: dealScope(true),
		Fields: fields([]Field{
			{Code: "smart_type", Type: String, Expr: "ty.name"},
			{Code: "title", Type: String, Expr: "d.title"},
		}, dealFields, []Field{{Code: "currency", Type: String, Expr: "d.currency"}}, dealDates, []Field{
			{Code: "amount", Type: Number, Expr: inBase("d.amount", "d.currency")},
		})},
	{Code: "crm_lead", Section: "crm", id: "l.id", Period: "created_at",
		from:  `crm_leads l JOIN crm_stages st ON st.id = l.stage_id JOIN users u ON u.id = l.responsible_id` + dictJoin("src", "l.source_id"),
		scope: crmScope("lead", "l"),
		Fields: []Field{
			{Code: "title", Type: String, Expr: "l.title"},
			{Code: "stage", Type: String, Expr: "st.name", CodeOf: "st.code", Prefix: "crm.stage.", Order: "st.sort"},
			{Code: "semantic", Type: String, Expr: "st.semantic", Prefix: "bi.semantic."},
			{Code: "responsible", Type: String, Expr: "u.name"},
			{Code: "source", Type: String, Expr: "coalesce(src.name, trim(l.source))", CodeOf: "src.code", Prefix: "crm.dict."},
			{Code: "company", Type: String, Expr: "l.company_title"},
			{Code: "created_at", Type: Date, Expr: "l.created_at"},
			{Code: "converted", Type: Bool, Expr: "l.deal_id IS NOT NULL"},
			{Code: "amount", Type: Number, Expr: inBase("l.amount", "l.currency")},
		}},
	{Code: "crm_contact", Section: "crm", id: "c.id", Period: "created_at",
		from: `crm_contacts c JOIN users u ON u.id = c.responsible_id LEFT JOIN crm_companies co ON co.id = c.company_id` +
			dictJoin("src", "c.source_id") + dictJoin("ty", "c.contact_type_id"),
		scope: crmScope("contact", "c"),
		Fields: []Field{
			{Code: "name", Type: String, Expr: "c.name"},
			{Code: "position", Type: String, Expr: "c.position"},
			{Code: "company", Type: String, Expr: "coalesce(co.title, '')"},
			{Code: "responsible", Type: String, Expr: "u.name"},
			dict("source", "src"),
			dict("contact_type", "ty"),
			{Code: "created_at", Type: Date, Expr: "c.created_at"},
		}},
	{Code: "crm_company", Section: "crm", id: "c.id", Period: "created_at",
		from:  `crm_companies c JOIN users u ON u.id = c.responsible_id` + dictJoin("ty", "c.company_type_id") + dictJoin("ind", "c.industry_id"),
		scope: crmScope("company", "c"),
		Fields: []Field{
			{Code: "title", Type: String, Expr: "c.title"},
			{Code: "responsible", Type: String, Expr: "u.name"},
			dict("company_type", "ty"),
			dict("industry", "ind"),
			{Code: "created_at", Type: Date, Expr: "c.created_at"},
		}},
	{Code: "crm_activity", Section: "crm", id: "a.id", Period: "deadline",
		from: `crm_activities a JOIN users u ON u.id = a.responsible_id`,
		scope: func(r *run) (string, error) { // one's own deeds, and those of the cards one may read
			sc := "a.responsible_id::text = $1"
			for _, e := range []struct{ entity, table string }{{"deal", "crm_deals"}, {"lead", "crm_leads"}, {"contact", "crm_contacts"}, {"company", "crm_companies"}} {
				c, err := r.scope(e.entity, "x")
				if err != nil {
					return "", err
				}
				sc += " OR (a.entity = '" + e.entity + "' AND EXISTS (SELECT 1 FROM " + e.table + " x WHERE x.id = a.entity_id AND (" + c + ")))"
			}
			return sc, nil
		},
		Fields: []Field{
			{Code: "subject", Type: String, Expr: "a.subject"},
			{Code: "kind", Type: String, Expr: "a.kind", Prefix: "crm.act."},
			{Code: "entity", Type: String, Expr: "a.entity", Prefix: "bi.entity."},
			{Code: "responsible", Type: String, Expr: "u.name"},
			{Code: "deadline", Type: Date, Expr: "a.deadline"},
			{Code: "done_at", Type: Date, Expr: "a.done_at"},
			{Code: "created_at", Type: Date, Expr: "a.created_at"},
			{Code: "done", Type: Bool, Expr: "a.done_at IS NOT NULL"},
			{Code: "overdue", Type: Bool, Expr: "coalesce(a.done_at, now()) > a.deadline"},
		}},
	invoiceSet("crm_invoice", "invoice", []Field{
		{Code: "due_date", Type: Date, Expr: "i.due_date::timestamptz"},
		{Code: "paid_on", Type: Date, Expr: "(SELECT max(p.paid_on) FROM crm_invoice_payments p WHERE p.invoice_id = i.id)::timestamptz"},
		{Code: "is_paid", Type: Bool, Expr: "i.status = 'paid'"},
		{Code: "overdue", Type: Bool, Expr: "i.status IN ('new', 'sent') AND i.due_date < CURRENT_DATE"},
		{Code: "paid", Type: Number, Expr: inBase(paidSQL, "i.currency")},
		{Code: "unpaid", Type: Number, Expr: "CASE WHEN i.status = 'cancelled' THEN 0 ELSE " + inBase("greatest(i.amount - "+paidSQL+", 0)", "i.currency") + " END"},
	}),
	invoiceSet("crm_quote", "quote", []Field{
		{Code: "valid_until", Type: Date, Expr: "i.due_date::timestamptz"},
		{Code: "accepted", Type: Bool, Expr: "i.status = 'accepted'"},
	}),
	{Code: "task", Section: "tasks", id: "t.id", Period: "created_at",
		from: `tasks t JOIN projects p ON p.id = t.project_id JOIN users r ON r.id = t.responsible_id JOIN users c ON c.id = t.creator_id
			LEFT JOIN project_stages ps ON ps.id = t.stage_id`,
		scope: func(r *run) (string, error) { return r.TaskScope("$1", "$2"), nil },
		Fields: []Field{
			{Code: "title", Type: String, Expr: "t.title"},
			{Code: "project", Type: String, Expr: "p.name"},
			{Code: "status", Type: String, Expr: "t.status", Prefix: "status."},
			{Code: "priority", Type: String, Expr: "t.priority::text", Prefix: "priority."},
			{Code: "stage", Type: String, Expr: "ps.title", CodeOf: "ps.code", Prefix: "stage.", Order: "ps.sort"},
			{Code: "responsible", Type: String, Expr: "r.name"},
			{Code: "creator", Type: String, Expr: "c.name"},
			{Code: "created_at", Type: Date, Expr: "t.created_at"},
			{Code: "closed_at", Type: Date, Expr: "t.closed_at"},
			{Code: "deadline", Type: Date, Expr: "t.deadline"},
			{Code: "completed", Type: Bool, Expr: "t.status = 'completed'"},
			{Code: "overdue", Type: Bool, Expr: "t.deadline IS NOT NULL AND coalesce(t.closed_at, now()) > t.deadline"},
			{Code: "estimate_hours", Type: Number, Expr: "(coalesce(t.estimate_minutes, 0) / 60.0)::float8"},
			{Code: "spent_hours", Type: Number, Expr: "(coalesce((SELECT sum(te.seconds) FROM time_entries te WHERE te.task_id = t.id), 0) / 3600.0)::float8"},
		}},
	{Code: "timeman", Section: "company", id: "(w.user_id::text || '/' || w.day::text)", Period: "day",
		from:  `workdays w JOIN users u ON u.id = w.user_id`,
		scope: staffScope("w.user_id"),
		Fields: []Field{
			{Code: "employee", Type: String, Expr: "u.name"},
			{Code: "day", Type: Date, Expr: "w.day::timestamptz"},
			{Code: "open", Type: Bool, Expr: "w.ended_at IS NULL"},
			{Code: "hours", Type: Number, Expr: "(greatest(extract(epoch FROM coalesce(w.ended_at, now()) - w.started_at) - w.pause_secs, 0) / 3600)::float8"},
			{Code: "pause_hours", Type: Number, Expr: "(w.pause_secs / 3600.0)::float8"},
			{Code: "start_hour", Type: Number, Expr: "extract(hour FROM w.started_at)::float8 + extract(minute FROM w.started_at)::float8 / 60"},
		}},
	{Code: "user", Section: "company", id: "u.id", Period: "created_at",
		from:  `users u`,
		scope: func(*run) (string, error) { return "NOT u.guest", nil },
		Fields: []Field{
			{Code: "employee", Type: String, Expr: "u.name"},
			{Code: "position", Type: String, Expr: "u.position"},
			{Code: "department", Type: String, Expr: `coalesce((SELECT string_agg(dp.name, ', ' ORDER BY dp.name) FROM user_departments ud
				JOIN departments dp ON dp.id = ud.department_id WHERE ud.user_id = u.id), '')`},
			{Code: "active", Type: Bool, Expr: "u.active"},
			{Code: "created_at", Type: Date, Expr: "u.created_at"},
			{Code: "last_login_at", Type: Date, Expr: "u.last_login_at"},
		}},
	{Code: "telephony_call", Section: "telephony", id: "c.id", Period: "started_at",
		from: `telephony_calls c LEFT JOIN users e ON e.id = coalesce(c.from_user, c.to_user)`,
		scope: func(r *run) (string, error) { // one's calls, those of the departments one heads
			from, _ := staffScope("c.from_user")(r)
			to, _ := staffScope("c.to_user")(r)
			return "(" + from + ") OR (" + to + ")", nil
		},
		Fields: []Field{
			{Code: "direction", Type: String, Expr: `CASE WHEN c.from_user IS NULL THEN 'in' WHEN c.to_user IS NULL THEN 'out' ELSE 'inner' END`,
				Prefix: "tel.kind."},
			{Code: "status", Type: String, Expr: "c.status", Prefix: "tel.status."},
			{Code: "employee", Type: String, Expr: "coalesce(e.name, '')"},
			{Code: "from_number", Type: String, Expr: "c.from_number"},
			{Code: "to_number", Type: String, Expr: "c.to_number"},
			{Code: "line", Type: String, Expr: "c.line"},
			{Code: "entity", Type: String, Expr: "coalesce(c.crm_entity, '')", Prefix: "bi.entity."},
			{Code: "started_at", Type: Date, Expr: "c.started_at"},
			{Code: "answered", Type: Bool, Expr: "c.status = 'answered'"},
			{Code: "minutes", Type: Number, Expr: "(c.seconds / 60.0)::float8"},
			{Code: "wait_seconds", Type: Number, Expr: "coalesce(extract(epoch FROM c.answered_at - c.started_at), 0)::float8"},
		}},
	{Code: "booking", Section: "booking", id: "b.id", Period: "starts_at",
		from: `bookings b JOIN booking_resources br ON br.id = b.resource_id JOIN booking_resource_types rt ON rt.id = br.type_id
			LEFT JOIN users cu ON cu.id = b.created_by`,
		scope: everyone,
		Fields: []Field{
			{Code: "resource", Type: String, Expr: "br.name"},
			{Code: "resource_type", Type: String, Expr: "rt.name", CodeOf: "rt.code", Prefix: "booking.type."},
			{Code: "status", Type: String, Expr: "b.status", Prefix: "booking.st."},
			{Code: "client", Type: String, Expr: "b.client_name"},
			{Code: "booked_by", Type: String, Expr: "coalesce(cu.name, '')"},
			{Code: "starts_at", Type: Date, Expr: "b.starts_at"},
			{Code: "created_at", Type: Date, Expr: "b.created_at"},
			{Code: "online", Type: Bool, Expr: "b.link_id IS NOT NULL"},
			{Code: "came", Type: Bool, Expr: "b.status = 'came'"},
			{Code: "no_show", Type: Bool, Expr: "b.status = 'no_show'"},
			{Code: "cancelled", Type: Bool, Expr: "b.status = 'cancelled'"},
			{Code: "duration_hours", Type: Number, Expr: "(extract(epoch FROM b.ends_at - b.starts_at) / 3600)::float8"},
			{Code: "services", Type: String, Expr: "coalesce((SELECT string_agg(bs.name, ', ' ORDER BY bs.sort) FROM booking_services bs WHERE bs.booking_id = b.id), '')"},
			{Code: "amount", Type: Number, Expr: "coalesce((SELECT sum(" + inBase("bs.price", "bs.currency") + ") FROM booking_services bs WHERE bs.booking_id = b.id), 0)::float8"},
		}},
}

// DatasetOf is the data set by its code (nil — none).
func DatasetOf(code string) *Dataset {
	for _, d := range Datasets {
		if d.Code == code {
			return d
		}
	}
	return nil
}
