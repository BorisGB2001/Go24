package crm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestInvoicePayments: an invoice is made of a quote with the quote's products; it is paid in parts, no more than is
// left, and paid in full it is «Оплачен»; a payment removed makes it «Отправлен» again; an invoice past its due date
// is overdue and found as such; a quote takes no payments; the client's link finds the document until switched off.
func TestInvoicePayments(t *testing.T) {
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
	errKey := func(err error) string {
		if e, ok := err.(*i18n.Error); ok {
			return e.Key
		}
		return ""
	}
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "deal", deal, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 3}})
	must(err)
	q, err := s.MakeInvoice(ctx, anna, "quote", deal, 0)
	must(err)
	// the quote's own rows go to the invoice made of it, not the deal's
	_, err = s.SetProductRows(ctx, anna, "quote", q, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 2}, {Name: "Внедрение", Price: 500, Quantity: 1}})
	must(err)
	if _, err := s.InvoiceOfQuote(ctx, anna, deal, 5); err != ErrNotFound {
		t.Fatalf("an invoice of a deal's id: %v", err)
	}
	inv, err := s.InvoiceOfQuote(ctx, anna, q, 5)
	must(err)
	v, err := s.Invoice(ctx, inv)
	must(err)
	rows, err := s.ProductRows(ctx, "invoice", inv)
	must(err)
	if v.Kind != "invoice" || v.SourceID != q || v.SourceNumber != 1 || v.Amount != 2500 || v.DealID != deal || len(rows) != 2 || v.Status != "new" {
		t.Fatalf("invoice of the quote %+v %+v", v, rows)
	}
	if _, err := s.InvoiceOfQuote(ctx, anna, inv, 5); errKey(err) != "err.bad_form" {
		t.Fatalf("an invoice of an invoice: %v", err)
	}
	if err := s.AddPayment(ctx, anna, q, Payment{Amount: 100}); errKey(err) != "err.crm_pay_closed" {
		t.Fatalf("a quote paid: %v", err)
	}

	// paid in parts
	for _, bad := range []float64{0, -5, 2500.01} {
		if err := s.AddPayment(ctx, anna, inv, Payment{Amount: bad}); errKey(err) != "err.crm_pay_amount" {
			t.Fatalf("payment %v: %v", bad, err)
		}
	}
	must(s.AddPayment(ctx, anna, inv, Payment{Amount: 1000, Comment: "аванс", PaidOn: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)}))
	v, _ = s.Invoice(ctx, inv)
	if v.Paid != 1000 || v.Due() != 1500 || v.Status != "new" {
		t.Fatalf("after the advance %+v", v)
	}
	if err := s.AddPayment(ctx, anna, inv, Payment{Amount: 1600}); errKey(err) != "err.crm_pay_amount" {
		t.Fatalf("overpaid: %v", err)
	}
	must(s.AddPayment(ctx, anna, inv, Payment{Amount: 1500}))
	v, _ = s.Invoice(ctx, inv)
	if v.Paid != 2500 || v.Due() != 0 || v.Status != "paid" {
		t.Fatalf("paid in full %+v", v)
	}
	pays, err := s.Payments(ctx, inv)
	must(err)
	if len(pays) != 2 || pays[0].Comment != "аванс" || pays[0].PaidOn.Format(time.DateOnly) != "2026-10-01" || pays[0].By.Name != "anna" {
		t.Fatalf("payments %+v", pays)
	}
	if err := s.AddPayment(ctx, anna, inv, Payment{Amount: 1}); errKey(err) != "err.crm_pay_closed" {
		t.Fatalf("a paid invoice paid again: %v", err)
	}
	tl, err := s.Timeline(ctx, "deal", deal)
	must(err)
	var evs []string
	for _, e := range tl {
		if e.Kind == "invoice" {
			evs = append(evs, e.Body)
		}
	}
	if got := strings.Join(evs, " "); !strings.Contains(got, "invoice:1:pay:1000.00 RUB") || !strings.Contains(got, "invoice:1:paid") {
		t.Fatalf("timeline %v", evs)
	}
	if err := s.DeletePayment(ctx, anna, q, pays[1].ID); err != ErrNotFound {
		t.Fatalf("a payment removed by another document: %v", err)
	}
	must(s.DeletePayment(ctx, anna, inv, pays[1].ID))
	if v, _ = s.Invoice(ctx, inv); v.Status != "sent" || v.Paid != 1000 {
		t.Fatalf("a payment removed %+v", v)
	}

	// overdue: past its due date, not paid
	if v.Overdue() {
		t.Fatal("overdue before its date")
	}
	past := time.Now().AddDate(0, 0, -1)
	v.DueDate = &past
	must(s.SaveInvoice(ctx, v))
	if v, _ = s.Invoice(ctx, inv); !v.Overdue() {
		t.Fatalf("not overdue %+v", v)
	}
	list, err := s.Invoices(ctx, anna, "", "invoice", "overdue")
	must(err)
	if len(list) != 1 || list[0].ID != inv {
		t.Fatalf("overdue list %+v", list)
	}
	must(s.AddPayment(ctx, anna, inv, Payment{Amount: 1500}))
	if list, _ = s.Invoices(ctx, anna, "", "invoice", "overdue"); len(list) != 0 {
		t.Fatalf("a paid invoice overdue %+v", list)
	}

	// the client's link
	if _, err := s.PublicInvoice(ctx, ""); err != ErrNotFound {
		t.Fatal("an empty token")
	}
	token, err := s.SetInvoicePublic(ctx, q, true)
	must(err)
	if p, err := s.PublicInvoice(ctx, token); err != nil || p.ID != q || p.PublicToken != token {
		t.Fatalf("public %+v %v", p, err)
	}
	_, err = s.SetInvoicePublic(ctx, q, false)
	must(err)
	if _, err := s.PublicInvoice(ctx, token); err != ErrNotFound {
		t.Fatal("the link works after switched off")
	}
	// the quote goes, the invoice stays without it
	must(s.DeleteInvoice(ctx, q))
	if v, _ = s.Invoice(ctx, inv); v.SourceID != "" {
		t.Fatalf("source after the quote's deletion %+v", v)
	}
}

// TestLeadInvoices: a quote and an invoice are made of a lead with its products and client, its timeline tells of
// them and of the payment; they are found by the lead and in the lists; an invoice of a quote stays with the lead;
// converted into a deal, the lead's documents go to the deal.
func TestLeadInvoices(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'anna', 'anna@t') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка с сайта", Name: "Иван Петров", CompanyTitle: "ООО Ромашка", Amount: 2000, Currency: "RUB",
		Responsible: Ref{ID: anna}})
	must(err)
	_, err = s.SetProductRows(ctx, anna, "lead", lead, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 2}})
	must(err)
	if _, err := s.MakeCardInvoice(ctx, anna, "invoice", "contact", lead, 0); err != ErrNotFound {
		t.Fatalf("an invoice of a contact: %v", err)
	}
	q, err := s.MakeCardInvoice(ctx, anna, "quote", "lead", lead, 0)
	must(err)
	inv, err := s.InvoiceOfQuote(ctx, anna, q, 5)
	must(err)
	v, err := s.Invoice(ctx, inv)
	must(err)
	if e, id := v.Card(); e != "lead" || id != lead || v.DealID != "" || v.CardTitle != "Заявка с сайта" || v.CardPath() != "/crm/leads/"+lead ||
		v.Company.Name != "ООО Ромашка" || v.Contact.Name != "Иван Петров" || v.Amount != 2000 || v.SourceID != q {
		t.Fatalf("the lead's invoice: %+v", v)
	}
	if rows, _ := s.ProductRows(ctx, "invoice", inv); len(rows) != 1 || rows[0].Quantity != 2 {
		t.Fatalf("the invoice's products: %+v", rows)
	}
	must(s.AddPayment(ctx, anna, inv, Payment{Amount: 2000}))
	if v, _ = s.Invoice(ctx, inv); v.Status != "paid" {
		t.Fatalf("paid in full: %s", v.Status)
	}
	tl, err := s.Timeline(ctx, "lead", lead)
	must(err)
	n := 0
	for _, e := range tl {
		if e.Kind == "invoice" {
			n++
		}
	}
	if n != 4 { // the quote, the invoice, the payment, «Оплачен»
		t.Fatalf("the lead's timeline tells of %d documents' events", n)
	}
	if list, _ := s.Invoices(ctx, anna, lead, "", ""); len(list) != 2 {
		t.Fatalf("the lead's documents: %d", len(list))
	}
	if list, _ := s.Invoices(ctx, anna, "", "invoice", "paid"); len(list) != 1 || list[0].ID != inv {
		t.Fatalf("the paid invoices: %+v", list)
	}

	deal, err := s.ConvertLead(ctx, anna, lead)
	must(err)
	if v, _ = s.Invoice(ctx, inv); v.DealID != deal || v.LeadID != "" {
		t.Fatalf("after the conversion the invoice is of %q / %q", v.DealID, v.LeadID)
	}
	if list, _ := s.Invoices(ctx, "", deal, "", ""); len(list) != 2 {
		t.Fatalf("the deal's documents: %d", len(list))
	}
	if _, err := pool.Exec(ctx, `UPDATE crm_invoices SET lead_id = $1 WHERE id = $2`, lead, inv); err == nil {
		t.Fatal("a document of both a deal and a lead")
	}
}
