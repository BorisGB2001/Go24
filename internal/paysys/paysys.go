// Package paysys takes online payments of CRM invoices, as «Платёжные системы»: the company connects its
// payment services (ЮKassa, Т-Банк), the client pays an invoice by its link on the service's page, and the service's
// notice (or a check of the payment when the client comes back) confirms it — the CRM then takes the payment.
package paysys

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Kinds of the services in the order the settings offer them.
var Kinds = []string{"yookassa", "tbank"}

var (
	ErrNotFound = errors.New("paysys: not found")
	errSystem   = i18n.E("err.pay_system")
	errAmount   = i18n.E("err.pay_amount")
)

// Store keeps the services and the payments started through them.
type Store struct {
	db  *pgxpool.Pool
	key []byte
	// Call is how the services' APIs are called (nil — over HTTP); tests replace it
	Call func(ctx context.Context, req Request) (Response, error)
}

// NewStore: secret is go24's own secret (the session secret); the key of the services' keys is derived from it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24 pay systems\x00" + secret))
	return &Store{db: db, key: k[:]}
}

// System is a payment service of the company. Its secret is written but never read back to the pages.
type System struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Login   string    `json:"login"` // ЮKassa shopId, Т-Банк TerminalKey
	Active  bool      `json:"active"`
	HasKey  bool      `json:"has_key"`
	Created time.Time `json:"created_at"`
}

// Payment is one payment the client started through a service.
type Payment struct {
	ID        string     `json:"id"`
	SystemID  string     `json:"system_id"`
	System    string     `json:"system"` // the service's name
	InvoiceID string     `json:"invoice_id"`
	ExtID     string     `json:"external_id"`
	Amount    float64    `json:"amount"`
	Currency  string     `json:"currency"`
	Status    string     `json:"status"` // pending | succeeded | canceled
	URL       string     `json:"-"`
	Error     string     `json:"error,omitempty"`
	Created   time.Time  `json:"created_at"`
	PaidAt    *time.Time `json:"paid_at,omitempty"`
}

const systemCols = `id::text, kind, name, login, active, secret IS NOT NULL, created_at`

func scanSystems(rows pgx.Rows, err error) ([]System, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (System, error) {
		var x System
		err := r.Scan(&x.ID, &x.Kind, &x.Name, &x.Login, &x.Active, &x.HasKey, &x.Created)
		return x, err
	})
}

// Systems are the company's services in their order.
func (s *Store) Systems(ctx context.Context) ([]System, error) {
	return scanSystems(s.db.Query(ctx, `SELECT `+systemCols+` FROM pay_systems ORDER BY sort, created_at`))
}

// Active are the services a client may pay through now.
func (s *Store) Active(ctx context.Context) ([]System, error) {
	return scanSystems(s.db.Query(ctx, `SELECT `+systemCols+` FROM pay_systems WHERE active AND secret IS NOT NULL ORDER BY sort, created_at`))
}

// SaveSystem adds a service (no ID) or changes one; an empty secret keeps the one it has.
func (s *Store) SaveSystem(ctx context.Context, x System, secret string) (string, error) {
	x.Name, x.Login, secret = strings.TrimSpace(x.Name), strings.TrimSpace(x.Login), strings.TrimSpace(secret)
	if !slices.Contains(Kinds, x.Kind) || x.Name == "" || len(x.Name) > 200 || x.Login == "" || len(x.Login) > 200 || len(secret) > 500 {
		return "", errSystem
	}
	var sealed []byte
	if secret != "" {
		var err error
		if sealed, err = s.seal(secret); err != nil {
			return "", err
		}
	}
	if x.ID == "" {
		if secret == "" {
			return "", errSystem
		}
		err := s.db.QueryRow(ctx, `INSERT INTO pay_systems (kind, name, login, secret, active, sort)
			VALUES ($1, $2, $3, $4, $5, (SELECT coalesce(max(sort), 0) + 1 FROM pay_systems)) RETURNING id::text`,
			x.Kind, x.Name, x.Login, sealed, x.Active).Scan(&x.ID)
		return x.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE pay_systems SET kind = $2, name = $3, login = $4, secret = coalesce($5, secret), active = $6
		WHERE id::text = $1`, x.ID, x.Kind, x.Name, x.Login, sealed, x.Active)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return x.ID, err
}

// DeleteSystem removes a service with the payments started through it (the invoices keep the payments taken).
func (s *Store) DeleteSystem(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM pay_systems WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// system reads a service with its secret.
func (s *Store) system(ctx context.Context, id string) (System, string, error) {
	var x System
	var sealed []byte
	err := s.db.QueryRow(ctx, `SELECT `+systemCols+`, secret FROM pay_systems WHERE id::text = $1`, id).
		Scan(&x.ID, &x.Kind, &x.Name, &x.Login, &x.Active, &x.HasKey, &x.Created, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, "", ErrNotFound
	}
	if err != nil {
		return x, "", err
	}
	secret, err := s.open(sealed)
	return x, secret, err
}

// Start is what a payment is started with.
type Start struct {
	InvoiceID   string
	Amount      float64
	Currency    string
	Description string // what the client sees on the service's page
	ReturnURL   string // where the service brings the client back; {id} — the payment's id
	NotifyURL   string // where the service tells about the payment (Т-Банк takes it with the payment)
}

// Start begins a payment through an active service: the payment is pending, and its URL is the service's page the
// client goes to; a refusal of the service is the error.
func (s *Store) Start(ctx context.Context, systemID string, in Start) (Payment, error) {
	x, secret, err := s.system(ctx, systemID)
	if err == nil && (!x.Active || secret == "") {
		err = ErrNotFound
	}
	if err != nil {
		return Payment{}, err
	}
	in.Amount = math.Round(in.Amount*100) / 100
	if in.Amount <= 0 {
		return Payment{}, errAmount
	}
	p := Payment{SystemID: x.ID, System: x.Name, InvoiceID: in.InvoiceID, Amount: in.Amount, Currency: in.Currency, Status: "pending"}
	if err := s.db.QueryRow(ctx, `INSERT INTO pay_payments (system_id, invoice_id, amount, currency) VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at`, x.ID, in.InvoiceID, in.Amount, in.Currency).Scan(&p.ID, &p.Created); err != nil {
		return Payment{}, err
	}
	in.ReturnURL = strings.ReplaceAll(in.ReturnURL, "{id}", p.ID)
	ext, payURL, startErr := s.create(ctx, x, secret, p, in)
	if startErr != nil {
		_, _ = s.db.Exec(ctx, `UPDATE pay_payments SET status = 'canceled', error = $2 WHERE id = $1`, p.ID, cut(startErr.Error(), 1000))
		return Payment{}, i18n.E("err.pay_failed", startErr.Error())
	}
	p.ExtID, p.URL = ext, payURL
	_, err = s.db.Exec(ctx, `UPDATE pay_payments SET external_id = $2, url = $3 WHERE id = $1`, p.ID, ext, payURL)
	return p, err
}

const paymentSQL = `SELECT p.id::text, p.system_id::text, x.name, p.invoice_id::text, p.external_id, p.amount::float8, p.currency, p.status,
	p.url, p.error, p.created_at, p.paid_at FROM pay_payments p JOIN pay_systems x ON x.id = p.system_id`

func scanPayments(rows pgx.Rows, err error) ([]Payment, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Payment, error) {
		var p Payment
		err := r.Scan(&p.ID, &p.SystemID, &p.System, &p.InvoiceID, &p.ExtID, &p.Amount, &p.Currency, &p.Status, &p.URL, &p.Error, &p.Created, &p.PaidAt)
		return p, err
	})
}

// Payment reads one payment.
func (s *Store) Payment(ctx context.Context, id string) (Payment, error) {
	list, err := scanPayments(s.db.Query(ctx, paymentSQL+` WHERE p.id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Payment{}, err
	}
	return list[0], nil
}

// Payments of an invoice started online, the last first.
func (s *Store) Payments(ctx context.Context, invoiceID string) ([]Payment, error) {
	return scanPayments(s.db.Query(ctx, paymentSQL+` WHERE p.invoice_id::text = $1 ORDER BY p.created_at DESC`, invoiceID))
}

// ByExternal finds the payment a service's notice is about.
func (s *Store) ByExternal(ctx context.Context, systemID, extID string) (Payment, error) {
	list, err := scanPayments(s.db.Query(ctx, paymentSQL+` WHERE p.system_id::text = $1 AND p.external_id = $2 AND p.external_id <> ''`, systemID, extID))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Payment{}, err
	}
	return list[0], nil
}

// Check asks the service how a pending payment is: paid is true only for the one call that saw it become paid —
// the caller takes the payment into the invoice then, once. A payment no more pending is as it is.
func (s *Store) Check(ctx context.Context, id string) (p Payment, paid bool, err error) {
	if p, err = s.Payment(ctx, id); err != nil || p.Status != "pending" || p.ExtID == "" {
		return p, false, err
	}
	x, secret, err := s.system(ctx, p.SystemID)
	if err != nil {
		return p, false, err
	}
	status, amount, err := s.state(ctx, x, secret, p.ExtID)
	if err != nil || status == "pending" {
		return p, false, err
	}
	if status == "succeeded" && math.Abs(amount-p.Amount) > 0.005 {
		status = "canceled" // the service paid another sum than was asked: not the invoice's payment
	}
	tag, err := s.db.Exec(ctx, `UPDATE pay_payments SET status = $2, paid_at = CASE WHEN $2 = 'succeeded' THEN now() END
		WHERE id = $1 AND status = 'pending'`, p.ID, status)
	if err != nil || tag.RowsAffected() == 0 {
		p, err = s.Payment(ctx, id)
		return p, false, err
	}
	p, err = s.Payment(ctx, id)
	return p, err == nil && p.Status == "succeeded", err
}

// Failed notes why a paid payment did not become the invoice's one (the invoice was paid or cancelled meanwhile).
func (s *Store) Failed(ctx context.Context, id, why string) error {
	_, err := s.db.Exec(ctx, `UPDATE pay_payments SET error = $2 WHERE id::text = $1`, id, cut(why, 1000))
	return err
}

// Pending are the payments of the last day still waiting for the service — checked again by the worker in case
// the notice did not come.
func (s *Store) Pending(ctx context.Context) ([]Payment, error) {
	return scanPayments(s.db.Query(ctx, paymentSQL+` WHERE p.status = 'pending' AND p.external_id <> ''
		AND p.created_at > now() - interval '1 day' ORDER BY p.created_at`))
}

func cut(v string, n int) string {
	if r := []rune(v); len(r) > n {
		return string(r[:n])
	}
	return v
}

func (s *Store) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Store) seal(plain string) ([]byte, error) {
	g, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) open(sealed []byte) (string, error) {
	if sealed == nil {
		return "", nil
	}
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("paysys: bad secret")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
	return string(out), err
}
