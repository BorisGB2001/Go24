package bi

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestMoreSets: the goods of deals (sums after the discount, sections of the catalog), the smart processes apart
// from the deals, the invoices with their payments and the quotes within the rights on their cards, the employees,
// the calls of one's own and of one's department, the bookings.
func TestMoreSets(t *testing.T) {
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
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := pool.Exec(ctx, sql, args...)
		must(err)
	}
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ($1, $1, $1 || '@t', 'Менеджер') RETURNING id`, n).Scan(&id))
		return id
	}
	anna, petr, ivan := newUser("anna"), newUser("petr"), newUser("ivan")
	c := crm.NewStore(pool)
	s := NewStore(pool, c)
	s.TZ = "Europe/Amsterdam"
	tr := func(k string) string { return "«" + k + "»" }
	run := func(viewer string, q Query) [][]any {
		t.Helper()
		res, err := s.Run(ctx, viewer, q, tr)
		must(err)
		return res.Rows
	}
	count := []Measure{{Agg: "count"}}

	// a deal with two lines of goods (one in a section, with a discount) and a smart process's card
	pipes, err := c.Pipelines(ctx)
	must(err)
	deal, err := c.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 1900, Responsible: crm.Ref{ID: anna}})
	must(err)
	var section, product string
	must(pool.QueryRow(ctx, `INSERT INTO crm_product_sections (name) VALUES ('Мебель') RETURNING id`).Scan(&section))
	must(pool.QueryRow(ctx, `INSERT INTO crm_products (name, price, section_id, created_by) VALUES ('Стол', 1000, $1, $2) RETURNING id`, section, anna).Scan(&product))
	exec(`INSERT INTO crm_product_rows (entity, entity_id, product_id, name, price, quantity, discount) VALUES
		('deal', $1, $2, 'Стол', 1000, 2, 10), ('deal', $1, NULL, 'Доставка', 100, 1, 0)`, deal, product)
	typ, err := c.AddType(ctx, "Договоры", "")
	must(err)
	tp, err := c.PipelinesOf(ctx, typ)
	must(err)
	_, err = c.SaveDeal(ctx, anna, crm.Deal{Title: "Договор 1", PipelineID: tp[0].ID, Amount: 50, Responsible: crm.Ref{ID: anna}})
	must(err)

	rows := run(anna, Query{Dataset: "crm_deal_product_row", Dims: []string{"section"}, Measures: []Measure{{Agg: "sum", Field: "row_sum"}}, Sort: 1})
	if len(rows) != 2 || rows[0][0] != "" || rows[0][1] != 100.0 || rows[1][0] != "Мебель" || rows[1][1] != 1800.0 {
		t.Fatalf("goods by section: %v", rows)
	}
	if rows = run(anna, Query{Dataset: "crm_deal", Measures: count}); rows[0][0] != 1.0 {
		t.Fatalf("deals with a smart card: %v", rows)
	}
	rows = run(anna, Query{Dataset: "crm_dynamic_items", Dims: []string{"smart_type"}, Measures: []Measure{{Agg: "sum", Field: "amount"}}})
	if len(rows) != 1 || rows[0][0] != "Договоры" || rows[0][1] != 50.0 {
		t.Fatalf("smart processes: %v", rows)
	}

	// an invoice paid in part, an overdue one; a quote; petr, a manager of his own cards, sees none of them
	var inv string
	must(pool.QueryRow(ctx, `INSERT INTO crm_invoices (kind, number, deal_id, title, status, amount, due_date, responsible_id, created_by)
		VALUES ('invoice', 1, $1, 'Счёт', 'sent', 1000, CURRENT_DATE + 10, $2, $2) RETURNING id`, deal, anna).Scan(&inv))
	exec(`INSERT INTO crm_invoices (kind, number, deal_id, title, status, amount, due_date, responsible_id, created_by)
		VALUES ('invoice', 2, $1, 'Счёт 2', 'new', 300, CURRENT_DATE - 1, $2, $2), ('quote', 1, $1, 'КП', 'accepted', 1900, NULL, $2, $2)`, deal, anna)
	must(c.AddPayment(ctx, anna, inv, crm.Payment{Amount: 400}))
	rows = run(anna, Query{Dataset: "crm_invoice", Dims: []string{"overdue"}, Measures: []Measure{{Agg: "sum", Field: "paid"}, {Agg: "sum", Field: "unpaid"}}, Sort: 1})
	if len(rows) != 2 || rows[0][1] != 400.0 || rows[0][2] != 600.0 || rows[1][1] != 0.0 || rows[1][2] != 300.0 {
		t.Fatalf("invoices: %v", rows)
	}
	if rows = run(anna, Query{Dataset: "crm_quote", Dims: []string{"status"}, Measures: count}); len(rows) != 1 || rows[0][0] != "«crm.inv.accepted»" {
		t.Fatalf("quotes: %v", rows)
	}
	manager, err := c.SaveRole(ctx, crm.Role{Name: "Менеджер", Perms: crm.Perms{"deal": {"read": "own"}, "lead": {"read": "own"}}})
	must(err)
	must(c.SetMember(ctx, manager, petr, true))
	for _, ds := range []string{"crm_invoice", "crm_quote", "crm_deal_product_row", "crm_dynamic_items"} {
		if rows = run(petr, Query{Dataset: ds, Measures: count}); rows[0][0] != 0.0 {
			t.Fatalf("%s of someone else's deal: %v", ds, rows)
		}
	}

	// employees: everyone sees them, the guests are not among them; with their departments
	exec(`INSERT INTO users (oidc_sub, name, guest) VALUES ('guest', 'Гость', true)`)
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, anna).Scan(&dept))
	exec(`INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $3)`, anna, petr, dept)
	rows = run(ivan, Query{Dataset: "user", Dims: []string{"department"}, Measures: count, Sort: 1})
	if len(rows) != 2 || rows[0][0] != "" || rows[0][1] != 1.0 || rows[1][0] != "Продажи" || rows[1][1] != 2.0 {
		t.Fatalf("employees: %v", rows)
	}

	// calls: petr's incoming answered, ivan's outgoing missed; anna heads petr; petr sees his own only
	exec(`INSERT INTO telephony_calls (uniqueid, from_number, to_number, to_user, started_at, answered_at, seconds, status)
		VALUES ('1', '+79001', '101', $1, now(), now() + interval '6 seconds', 120, 'answered')`, petr)
	exec(`INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status)
		VALUES ('2', '102', '+79002', $1, now(), 'noanswer')`, ivan)
	q := Query{Dataset: "telephony_call", Dims: []string{"direction"}, Measures: []Measure{{Agg: "sum", Field: "minutes"}, {Agg: "max", Field: "wait_seconds"}}}
	if rows = run(anna, q); len(rows) != 1 || rows[0][0] != "«tel.kind.in»" || rows[0][1] != 2.0 || rows[0][2] != 6.0 {
		t.Fatalf("calls of a head: %v", rows)
	}
	if rows = run(ivan, q); len(rows) != 1 || rows[0][0] != "«tel.kind.out»" {
		t.Fatalf("one's own calls: %v", rows)
	}
	res, err := s.Run(tasks.WithAdmin(ctx), ivan, q, tr)
	must(err)
	if len(res.Rows) != 2 {
		t.Fatalf("calls of an administrator: %v", res.Rows)
	}

	// bookings: by status, the hours booked
	var res1 string
	must(pool.QueryRow(ctx, `INSERT INTO booking_resources (type_id, name) SELECT id, 'Кабинет 1' FROM booking_resource_types WHERE code = 'room'
		RETURNING id`).Scan(&res1))
	exec(`INSERT INTO bookings (resource_id, starts_at, ends_at, status) VALUES ($1, now(), now() + interval '90 minutes', 'came'),
		($1, now() + interval '1 day', now() + interval '1 day 30 minutes', 'no_show')`, res1)
	rows = run(ivan, Query{Dataset: "booking", Dims: []string{"resource_type", "no_show"}, Measures: []Measure{{Agg: "sum", Field: "duration_hours"}}, Sort: 2})
	if len(rows) != 2 || rows[0][0] != "«booking.type.room»" || rows[0][2] != 1.5 || rows[1][1] != "«bi.yes»" {
		t.Fatalf("bookings: %v", rows)
	}
}
