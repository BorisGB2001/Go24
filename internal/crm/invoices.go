package crm

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Предложения» and «Счета»: made from a deal — its title, client, currency and products — numbered per
// kind, with a status (a quote: draft, sent, accepted, declined; an invoice: new, sent, paid, cancelled) and printed
// for the client with the seller's details. The deal's timeline tells when one is made and when its status changes.
// An invoice is paid in parts («Оплаты»): paid in full it is «Оплачен»; not paid by its due date it is overdue. An
// invoice is made of a quote («Создать на основании»); a public link shows the document to the client.

// InvoiceStatuses by kind, in their order; the first is a new one's.
var InvoiceStatuses = map[string][]string{
	"quote":   {"draft", "sent", "accepted", "declined"},
	"invoice": {"new", "sent", "paid", "cancelled"},
}

// Invoice is a quote or an invoice.
type Invoice struct {
	ID, Kind, Title, Status, Currency, Comment string
	Number                                     int
	DealID, LeadID                             string // the card it belongs to: a deal or a lead
	CardTitle                                  string // the title of its deal or lead
	Company, Contact, Responsible              Ref
	IssueDate                                  time.Time
	DueDate                                    *time.Time
	Amount, Paid                               float64 // Paid: the sum of its payments
	SourceID                                   string  // the quote an invoice is made of
	SourceNumber                               int
	PublicToken                                string // the client's link; "" — off
	CreatedAt                                  time.Time
}

// Overdue: an invoice not paid (nor cancelled) by its due date.
func (v Invoice) Overdue() bool {
	return v.Kind == "invoice" && (v.Status == "new" || v.Status == "sent") && v.DueDate != nil &&
		v.DueDate.Format(time.DateOnly) < time.Now().Format(time.DateOnly)
}

// Due is what is left to pay of an invoice.
func (v Invoice) Due() float64 {
	if v.Kind != "invoice" || v.Status == "paid" || v.Status == "cancelled" || v.Paid >= v.Amount {
		return 0
	}
	return math.Round((v.Amount-v.Paid)*100) / 100
}

// Card is the deal or the lead the document belongs to: its entity ("deal", "lead") and id.
func (v Invoice) Card() (string, string) {
	if v.LeadID != "" {
		return "lead", v.LeadID
	}
	return "deal", v.DealID
}

// CardPath is the card's page.
func (v Invoice) CardPath() string {
	e, id := v.Card()
	return "/crm/" + e + "s/" + id
}

// Code is the document's number as printed: «С-12», «П-3» come from the locales; this is the bare number.
func (v Invoice) Code() string { return strconv.Itoa(v.Number) }

const invoiceSQL = `SELECT i.id, i.kind, i.title, i.status, i.currency, i.comment, i.number, coalesce(i.deal_id::text, ''), coalesce(i.lead_id::text, ''),
		coalesce(d.title, l.title), coalesce(c.id::text, ''), coalesce(c.title, nullif(l.company_title, ''), ''), coalesce(x.id::text, ''),
		coalesce(x.name, nullif(l.name, ''), ''),
		i.responsible_id::text, r.name, i.issue_date, i.due_date, i.amount::float8,
		(SELECT coalesce(sum(p.amount), 0)::float8 FROM crm_invoice_payments p WHERE p.invoice_id = i.id),
		coalesce(i.source_id::text, ''), coalesce(src.number, 0), coalesce(i.public_token, ''), i.created_at
	FROM crm_invoices i LEFT JOIN crm_invoices src ON src.id = i.source_id LEFT JOIN crm_deals d ON d.id = i.deal_id
	LEFT JOIN crm_leads l ON l.id = i.lead_id JOIN users r ON r.id = i.responsible_id
	LEFT JOIN crm_companies c ON c.id = coalesce(d.company_id, l.company_id) LEFT JOIN crm_contacts x ON x.id = coalesce(d.contact_id, l.contact_id)`

func scanInvoices(rows pgx.Rows, err error) ([]Invoice, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Invoice, error) {
		var v Invoice
		err := r.Scan(&v.ID, &v.Kind, &v.Title, &v.Status, &v.Currency, &v.Comment, &v.Number, &v.DealID, &v.LeadID, &v.CardTitle,
			&v.Company.ID, &v.Company.Name, &v.Contact.ID, &v.Contact.Name, &v.Responsible.ID, &v.Responsible.Name,
			&v.IssueDate, &v.DueDate, &v.Amount, &v.Paid, &v.SourceID, &v.SourceNumber, &v.PublicToken, &v.CreatedAt)
		return v, err
	})
}

// MakeInvoice makes a quote or an invoice of a deal with the deal's products (an invoice is due in dueDays days).
func (s *Store) MakeInvoice(ctx context.Context, userID, kind, dealID string, dueDays int) (string, error) {
	return s.MakeCardInvoice(ctx, userID, kind, "deal", dealID, dueDays)
}

// MakeCardInvoice makes a quote or an invoice of a deal or a lead (entity) with the card's products.
func (s *Store) MakeCardInvoice(ctx context.Context, userID, kind, entity, id string, dueDays int) (string, error) {
	if InvoiceStatuses[kind] == nil {
		return "", i18n.E("err.bad_form")
	}
	switch entity {
	case "deal":
		d, err := s.Deal(ctx, id)
		if err != nil {
			return "", err
		}
		return s.makeInvoice(ctx, userID, kind, "deal", d.ID, d.Title, d.Currency, d.Responsible.ID, d.Amount, "deal", d.ID, "", dueDays)
	case "lead":
		l, err := s.Lead(ctx, id)
		if err != nil {
			return "", err
		}
		return s.makeInvoice(ctx, userID, kind, "lead", l.ID, l.Title, l.Currency, l.Responsible.ID, l.Amount, "lead", l.ID, "", dueDays)
	}
	return "", ErrNotFound
}

// InvoiceOfQuote makes an invoice of a quote: its title, currency, responsible and products, in its deal or lead.
func (s *Store) InvoiceOfQuote(ctx context.Context, userID, quoteID string, dueDays int) (string, error) {
	q, err := s.Invoice(ctx, quoteID)
	if err != nil {
		return "", err
	}
	if q.Kind != "quote" {
		return "", i18n.E("err.bad_form")
	}
	entity, card := q.Card()
	return s.makeInvoice(ctx, userID, "invoice", entity, card, q.Title, q.Currency, q.Responsible.ID, q.Amount, "quote", q.ID, q.ID, dueDays)
}

// makeInvoice numbers a new document of the kind and copies the product rows of the entity it is made of.
func (s *Store) makeInvoice(ctx context.Context, userID, kind, entity, cardID, title, currency, responsible string, amount float64,
	from, fromID, sourceID string, dueDays int) (string, error) {
	var id string
	var number int
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE crm_invoices IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return err
		}
		var due *time.Time
		if dueDays > 0 {
			t := time.Now().AddDate(0, 0, dueDays)
			due = &t
		}
		if err := tx.QueryRow(ctx, `INSERT INTO crm_invoices (kind, number, deal_id, lead_id, title, status, due_date, amount, currency,
			responsible_id, created_by, issue_date, source_id)
			VALUES ($1, (SELECT coalesce(max(number), 0) + 1 FROM crm_invoices WHERE kind = $1), CASE WHEN $12 = 'deal' THEN $2::uuid END,
				CASE WHEN $12 = 'lead' THEN $2::uuid END, $3, $4, $5::date, $6, $7, $8, $9, $10::date, nullif($11, '')::uuid)
			RETURNING id, number`, kind, cardID, title, InvoiceStatuses[kind][0], dateOf(due), amount, currency, responsible, userID,
			time.Now().Format(time.DateOnly), sourceID, entity).Scan(&id, &number); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_product_rows (entity, entity_id, product_id, name, price, quantity, unit, discount, vat_rate,
			vat_included, sort) SELECT $1, $2, product_id, name, price, quantity, unit, discount, vat_rate, vat_included, sort
			FROM crm_product_rows WHERE entity = $3 AND entity_id = $4`, kind, id, from, fromID); err != nil {
			return err
		}
		return s.event(ctx, tx, entity, cardID, userID, "invoice", kind+":"+strconv.Itoa(number)+":"+InvoiceStatuses[kind][0])
	})
	return id, err
}

// dateOf is a day as the company's calendar has it (the database keeps dates without a zone).
func dateOf(t *time.Time) *string {
	if t == nil {
		return nil
	}
	d := t.Format(time.DateOnly)
	return &d
}

// Invoice is one quote or invoice.
func (s *Store) Invoice(ctx context.Context, id string) (Invoice, error) {
	list, err := scanInvoices(s.db.Query(ctx, invoiceSQL+` WHERE i.id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	return list[0], nil
}

// Invoices of a deal or a lead (cardID), or all of a kind ("" — both) in a status ("" — any; "overdue" — the invoices
// not paid by their due date) whose deals and leads the viewer may read.
func (s *Store) Invoices(ctx context.Context, viewer, cardID, kind, status string) ([]Invoice, error) {
	deals, err := s.viewScope(ctx, viewer, "deal", "d", 4)
	if err != nil {
		return nil, err
	}
	leads, err := s.viewScope(ctx, viewer, "lead", "l", 4)
	if err != nil {
		return nil, err
	}
	return scanInvoices(s.db.Query(ctx, invoiceSQL+` WHERE ($1 = '' OR i.deal_id::text = $1 OR i.lead_id::text = $1) AND ($2 = '' OR i.kind = $2)
		AND ($3 = '' OR i.status = $3 OR $3 = 'overdue' AND i.kind = 'invoice' AND i.status IN ('new', 'sent') AND i.due_date < $5::date)
		AND (i.deal_id IS NOT NULL AND (`+deals+`) OR i.lead_id IS NOT NULL AND (`+leads+`)) ORDER BY i.created_at DESC LIMIT 500`,
		cardID, kind, status, viewer, time.Now().Format(time.DateOnly)))
}

// SaveInvoice changes a quote's or an invoice's title, dates, comment and responsible.
func (s *Store) SaveInvoice(ctx context.Context, v Invoice) error {
	if err := clean(&v.Title, 300); err != nil || v.Title == "" {
		return i18n.E("err.crm_title")
	}
	if err := clean(&v.Comment, 10000); err != nil {
		return err
	}
	if v.IssueDate.IsZero() {
		v.IssueDate = time.Now()
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_invoices SET title = $2, issue_date = $3::date, due_date = $4::date, comment = $5,
		responsible_id = coalesce(nullif($6, '')::uuid, responsible_id), updated_at = now() WHERE id::text = $1`,
		v.ID, v.Title, v.IssueDate.Format(time.DateOnly), dateOf(v.DueDate), v.Comment, v.Responsible.ID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetInvoiceStatus changes the status; the timeline of its deal or lead tells.
func (s *Store) SetInvoiceStatus(ctx context.Context, userID, id, status string) error {
	v, err := s.Invoice(ctx, id)
	if err != nil {
		return err
	}
	known := false
	for _, st := range InvoiceStatuses[v.Kind] {
		known = known || st == status
	}
	if !known {
		return i18n.E("err.bad_form")
	}
	if status == v.Status {
		return nil
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE crm_invoices SET status = $2, updated_at = now() WHERE id = $1`, id, status); err != nil {
			return err
		}
		entity, card := v.Card()
		return s.event(ctx, tx, entity, card, userID, "invoice", v.Kind+":"+v.Code()+":"+status)
	})
	if err == nil && (status == "paid" || status == "accepted") {
		entity, card := v.Card()
		err = s.Fire(ctx, entity, card, map[string]string{"paid": "invoice_paid", "accepted": "quote_accepted"}[status])
	}
	return err
}

// Payment is one payment of an invoice.
type Payment struct {
	ID, Comment string
	PaidOn      time.Time
	Amount      float64
	By          Ref
	CreatedAt   time.Time
}

// Payments of an invoice, the first paid first.
func (s *Store) Payments(ctx context.Context, invoiceID string) ([]Payment, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id::text, p.comment, p.paid_on, p.amount::float8, p.created_by::text, u.name, p.created_at
		FROM crm_invoice_payments p JOIN users u ON u.id = p.created_by WHERE p.invoice_id::text = $1 ORDER BY p.paid_on, p.created_at`, invoiceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Payment, error) {
		var p Payment
		err := r.Scan(&p.ID, &p.Comment, &p.PaidOn, &p.Amount, &p.By.ID, &p.By.Name, &p.CreatedAt)
		return p, err
	})
}

// AddPayment takes a payment of an invoice (no more than is left to pay); paid in full the invoice is «Оплачен»
// (with its robots' trigger). The timeline of its deal or lead tells.
func (s *Store) AddPayment(ctx context.Context, userID, invoiceID string, p Payment) error {
	if err := clean(&p.Comment, 1000); err != nil {
		return err
	}
	p.Amount = math.Round(p.Amount*100) / 100
	if p.PaidOn.IsZero() {
		p.PaidOn = time.Now()
	}
	var v Invoice
	full := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		// the invoice is locked: two payments at once don't overpay it
		if _, err := tx.Exec(ctx, `SELECT 1 FROM crm_invoices WHERE id::text = $1 FOR UPDATE`, invoiceID); err != nil {
			return err
		}
		list, err := scanInvoices(tx.Query(ctx, invoiceSQL+` WHERE i.id::text = $1`, invoiceID))
		if err != nil {
			return err
		}
		if len(list) == 0 {
			return ErrNotFound
		}
		v = list[0]
		if v.Kind != "invoice" || v.Status == "paid" || v.Status == "cancelled" {
			return i18n.E("err.crm_pay_closed")
		}
		if p.Amount <= 0 || p.Amount > v.Amount-v.Paid+0.005 {
			return i18n.E("err.crm_pay_amount", strconv.FormatFloat(v.Amount-v.Paid, 'f', 2, 64))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_invoice_payments (invoice_id, paid_on, amount, comment, created_by) VALUES ($1, $2::date, $3, $4, $5)`,
			v.ID, p.PaidOn.Format(time.DateOnly), p.Amount, p.Comment, userID); err != nil {
			return err
		}
		full = v.Paid+p.Amount >= v.Amount-0.005
		entity, card := v.Card()
		return s.event(ctx, tx, entity, card, userID, "invoice", v.Kind+":"+v.Code()+":pay:"+strconv.FormatFloat(p.Amount, 'f', 2, 64)+" "+v.Currency)
	})
	if err == nil && full {
		err = s.SetInvoiceStatus(ctx, userID, v.ID, "paid")
	}
	return err
}

// DeletePayment removes a payment taken by mistake; an invoice paid in full no more is «Отправлен» again.
func (s *Store) DeletePayment(ctx context.Context, userID, invoiceID, paymentID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_invoice_payments WHERE id::text = $1 AND invoice_id::text = $2`, paymentID, invoiceID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return err
	}
	v, err := s.Invoice(ctx, invoiceID)
	if err == nil && v.Status == "paid" && v.Paid < v.Amount-0.005 {
		err = s.SetInvoiceStatus(ctx, userID, v.ID, "sent")
	}
	return err
}

// SetInvoicePublic switches the document's link for the client on (a new token each time) or off.
func (s *Store) SetInvoicePublic(ctx context.Context, id string, on bool) (string, error) {
	token := ""
	if on {
		b := make([]byte, 18)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		token = base64.RawURLEncoding.EncodeToString(b)
	}
	tag, err := s.db.Exec(ctx, `UPDATE crm_invoices SET public_token = nullif($2, '') WHERE id::text = $1`, id, token)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return token, err
}

// PublicInvoice is the quote or the invoice of a client's link.
func (s *Store) PublicInvoice(ctx context.Context, token string) (Invoice, error) {
	if token == "" {
		return Invoice{}, ErrNotFound
	}
	list, err := scanInvoices(s.db.Query(ctx, invoiceSQL+` WHERE i.public_token = $1`, token))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Invoice{}, err
	}
	return list[0], nil
}

// DeleteInvoice removes a quote or an invoice.
func (s *Store) DeleteInvoice(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_invoices WHERE id::text = $1`, id)
	return err
}

// Seller is the company's details printed on quotes and invoices (the administrator's text: name, INN, address, bank).
func (s *Store) Seller(ctx context.Context) (string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'crm_seller'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	var text string
	if err == nil {
		err = json.Unmarshal(raw, &text)
	}
	return text, err
}

// SetSeller keeps the company's details.
func (s *Store) SetSeller(ctx context.Context, userID, text string) error {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 5000 {
		return i18n.E("err.crm_field")
	}
	raw, _ := json.Marshal(text)
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ('crm_seller', $1, $2)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, raw, userID)
	return err
}
