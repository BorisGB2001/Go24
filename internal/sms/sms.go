// Package sms sends SMS to clients, as «Центр сообщений»: the company's providers (SMS.ru, SMSC, Twilio
// or an own gateway that takes a JSON post), one of them by default, and the log of what was sent through them.
package sms

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Kinds of the providers in the order the settings offer them.
var Kinds = []string{"smsru", "smsc", "twilio", "gateway"}

// MaxText is how long a message may be (in characters; the provider splits it into parts).
const MaxText = 1000

var (
	errNoSender = i18n.E("err.sms_no_sender")
	errPhone    = i18n.E("err.sms_phone")
	errText     = i18n.E("err.sms_text")
	errSender   = i18n.E("err.sms_sender")
)

// Store keeps the providers and the messages.
type Store struct {
	db  *pgxpool.Pool
	key []byte
	// Post is how the providers are called (nil — over HTTP); tests replace it
	Post func(ctx context.Context, req Request) (Response, error)
}

// NewStore: secret is go24's own secret (the session secret); the key of the providers' keys is derived from it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24 sms senders\x00" + secret))
	return &Store{db: db, key: k[:]}
}

// Sender is a provider. Its secret (API key, password, token) is written but never read back to the pages.
type Sender struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Name    string    `json:"name"`
	Login   string    `json:"login,omitempty"`  // SMSC login, Twilio account SID, the gateway's address
	From    string    `json:"sender,omitempty"` // the sender's name or number
	Default bool      `json:"is_default"`
	Active  bool      `json:"active"`
	HasKey  bool      `json:"has_key"`
	Created time.Time `json:"created_at"`
}

// Message is one SMS sent (or not) through a provider.
type Message struct {
	ID       string    `json:"id"`
	SenderID string    `json:"sender_id,omitempty"`
	Phone    string    `json:"phone"`
	Text     string    `json:"text"`
	Status   string    `json:"status"` // sent | error
	Error    string    `json:"error,omitempty"`
	ExtID    string    `json:"external_id,omitempty"`
	Created  time.Time `json:"created_at"`
}

const senderCols = `id::text, kind, name, login, sender, is_default, active, secret IS NOT NULL, created_at`

// Senders are the providers, the default one first.
func (s *Store) Senders(ctx context.Context) ([]Sender, error) {
	rows, err := s.db.Query(ctx, `SELECT `+senderCols+` FROM sms_senders ORDER BY is_default DESC, lower(name), created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Sender])
}

// Ready tells whether there is a provider to send through.
func (s *Store) Ready(ctx context.Context) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sms_senders WHERE active)`).Scan(&ok)
	return ok
}

// SaveSender adds a provider (no ID) or changes one; an empty secret keeps the one it has. The first provider is the
// default one.
func (s *Store) SaveSender(ctx context.Context, x Sender, secret string) (string, error) {
	x.Name, x.Login, x.From, secret = strings.TrimSpace(x.Name), strings.TrimSpace(x.Login), strings.TrimSpace(x.From), strings.TrimSpace(secret)
	if !slices.Contains(Kinds, x.Kind) || x.Name == "" || len(x.Name) > 200 || len(x.Login) > 500 || len(x.From) > 50 || len(secret) > 500 {
		return "", errSender
	}
	if x.Kind == "gateway" && !strings.HasPrefix(x.Login, "http://") && !strings.HasPrefix(x.Login, "https://") {
		return "", errSender
	}
	var sealed []byte
	if secret != "" {
		var err error
		if sealed, err = s.seal(secret); err != nil {
			return "", err
		}
	}
	if x.ID == "" {
		if x.Kind != "gateway" && secret == "" || (x.Kind == "smsc" || x.Kind == "twilio") && x.Login == "" {
			return "", errSender
		}
		err := s.db.QueryRow(ctx, `INSERT INTO sms_senders (kind, name, login, secret, sender, active, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, NOT EXISTS (SELECT 1 FROM sms_senders WHERE is_default)) RETURNING id::text`,
			x.Kind, x.Name, x.Login, sealed, x.From, x.Active).Scan(&x.ID)
		return x.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE sms_senders SET kind = $2, name = $3, login = $4, secret = coalesce($5, secret), sender = $6,
		active = $7 WHERE id::text = $1`, x.ID, x.Kind, x.Name, x.Login, sealed, x.From, x.Active)
	if err == nil && tag.RowsAffected() == 0 {
		err = errSender
	}
	return x.ID, err
}

// SetDefault makes the provider the default one.
func (s *Store) SetDefault(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE sms_senders SET is_default = false WHERE is_default`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE sms_senders SET is_default = true WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errSender
	}
	return tx.Commit(ctx)
}

// DeleteSender removes the provider; when it was the default one, the oldest of the others becomes it.
func (s *Store) DeleteSender(ctx context.Context, id string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM sms_senders WHERE id::text = $1`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE sms_senders SET is_default = true WHERE id = (SELECT id FROM sms_senders ORDER BY created_at LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM sms_senders WHERE is_default)`)
	return err
}

// Out is what to send: through the provider (empty — the default one), to the phone, by whom and from which card.
type Out struct {
	SenderID, Phone, Text, AuthorID string
	Entity, EntityID                string // the CRM card, if any
}

type senderKey struct{}

// WithSender: Send through the provider unless its Out names one (a business process's «Отправка SMS» with its
// provider chosen sends through the card's timeline, which knows nothing of providers).
func WithSender(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, senderKey{}, id)
}

// Send sends the message and keeps it in the log whether it went or not; a provider's refusal is the error then.
func (s *Store) Send(ctx context.Context, o Out) (Message, error) {
	if o.SenderID == "" {
		o.SenderID, _ = ctx.Value(senderKey{}).(string)
	}
	m := Message{Phone: Normalize(o.Phone), Text: strings.TrimSpace(o.Text)}
	if m.Phone == "" {
		return m, errPhone
	}
	if m.Text == "" || utf8.RuneCountInString(m.Text) > MaxText {
		return m, errText
	}
	var x Sender
	var sealed []byte
	q, arg := `SELECT `+senderCols+`, secret FROM sms_senders WHERE active ORDER BY is_default DESC, created_at LIMIT 1`, []any{}
	if o.SenderID != "" {
		q, arg = `SELECT `+senderCols+`, secret FROM sms_senders WHERE active AND id::text = $1`, []any{o.SenderID}
	}
	err := s.db.QueryRow(ctx, q, arg...).Scan(&x.ID, &x.Kind, &x.Name, &x.Login, &x.From, &x.Default, &x.Active, &x.HasKey, &x.Created, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, errNoSender
	}
	if err != nil {
		return m, err
	}
	secret := ""
	if sealed != nil {
		if secret, err = s.open(sealed); err != nil {
			return m, err
		}
	}
	m.SenderID = x.ID
	ext, sendErr := s.deliver(ctx, x, secret, m.Phone, m.Text)
	m.Status, m.ExtID = "sent", ext
	if sendErr != nil {
		m.Status, m.Error = "error", sendErr.Error()
		if len(m.Error) > 500 {
			m.Error = m.Error[:500]
		}
	}
	var ent, eid any
	if o.Entity != "" && o.EntityID != "" {
		ent, eid = o.Entity, o.EntityID
	}
	var author any
	if o.AuthorID != "" {
		author = o.AuthorID
	}
	if err := s.db.QueryRow(ctx, `INSERT INTO sms_messages (sender_id, phone, body, status, error, external_id, author_id, entity, entity_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, coalesce($8, ''), $9) RETURNING id::text, created_at`,
		x.ID, m.Phone, m.Text, m.Status, m.Error, m.ExtID, author, ent, eid).Scan(&m.ID, &m.Created); err != nil {
		return m, err
	}
	return m, sendErr
}

// Normalize is the phone as the providers take it: + and digits; a Russian 8XXXXXXXXXX becomes +7XXXXXXXXXX, ten
// digits starting with 9 — +7 and them. "" — not a phone.
func Normalize(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	switch {
	case len(d) == 11 && d[0] == '8':
		d = "7" + d[1:]
	case len(d) == 10 && d[0] == '9':
		d = "7" + d
	}
	if len(d) < 10 || len(d) > 15 {
		return ""
	}
	return "+" + d
}

// Parts is how many SMS the text takes: 160 characters in one (153 in each of several) for the GSM alphabet, 70
// (67) when it has others (Cyrillic and so on).
func Parts(text string) int {
	n, one, many := utf8.RuneCountInString(text), 160, 153
	for _, r := range text {
		if !strings.ContainsRune(gsm7, r) {
			one, many = 70, 67
			break
		}
	}
	switch {
	case n == 0:
		return 0
	case n <= one:
		return 1
	}
	return (n + many - 1) / many
}

// gsm7 is the basic GSM 03.38 alphabet (the extension characters count as one here).
const gsm7 = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà^{}\\[~]|€"

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
	g, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("sms: bad secret")
	}
	out, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
	return string(out), err
}
