// Package telephony is go24's own PBX, as «Телефония»: an Asterisk server whose accounts go24 keeps.
// An employee with an inner number (given by the administrators) has two SIP accounts named by it: "<number>" for
// a desk or mobile SIP phone (its password is shown to the employee) and "w<number>" for go24's softphone in the
// browser. Calling the number rings both.
package telephony

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// settingEnabled is the company setting: telephony is on.
const settingEnabled = "telephony"

// innerMax is the longest inner number: a longer one is outside.
const innerMax = 8

var (
	ErrNotFound = errors.New("telephony: no such employee")
	numberRe    = regexp.MustCompile(`^[0-9]{1,8}$`)
)

// Store keeps the SIP accounts of the employees.
type Store struct {
	db  *pgxpool.Pool
	key []byte // AES-256 key for the passwords
}

// NewStore: secret is go24's own secret (the session secret); the key of the passwords is derived from it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24 sip passwords\x00" + secret))
	return &Store{db: db, key: k[:]}
}

// Line is the telephony of one employee.
type Line struct {
	UserID, Name, Number string
	Password             string // of the SIP phone
	WebPassword          string // of go24's softphone
	Dup                  bool   // another employee has the same number first: this one has no account
}

// Login of the SIP phone and of the softphone.
func (l Line) Login() string    { return l.Number }
func (l Line) WebLogin() string { return "w" + l.Number }

// Enabled tells whether telephony is on.
func (s *Store) Enabled(ctx context.Context) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, settingEnabled).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

// SetEnabled turns telephony on or off.
func (s *Store) SetEnabled(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingEnabled, on, userID)
	return err
}

// Lines are the employees with an inner number (active, not guests), by number; ones without passwords yet get them.
func (s *Store) Lines(ctx context.Context) ([]Line, error) {
	return s.lines(ctx, "")
}

// LineOf is the telephony of one employee; false — they have no inner number.
func (s *Store) LineOf(ctx context.Context, userID string) (Line, bool, error) {
	ls, err := s.lines(ctx, userID)
	if err != nil || len(ls) == 0 {
		return Line{}, false, err
	}
	return ls[0], true, nil
}

func (s *Store) lines(ctx context.Context, userID string) ([]Line, error) {
	// first: whether someone earlier (by number, then by when they came) has the same number
	rows, err := s.db.Query(ctx, `SELECT id::text, name, inner_phone, first, secret, web_secret FROM (
		SELECT u.id, CASE WHEN u.name <> '' THEN u.name ELSE u.email END AS name, u.inner_phone, l.secret, l.web_secret,
			row_number() OVER (PARTITION BY u.inner_phone ORDER BY u.created_at, u.id) = 1 AS first
		FROM users u LEFT JOIN telephony_lines l ON l.user_id = u.id
		WHERE u.active AND NOT u.guest AND u.oidc_sub <> 'legacy:0' AND u.inner_phone <> '') t
		WHERE $1 = '' OR id::text = $1 ORDER BY inner_phone, name`, userID)
	if err != nil {
		return nil, err
	}
	type row struct {
		l           Line
		sec, webSec []byte
	}
	var got []row
	for rows.Next() {
		var r row
		var first bool
		if err := rows.Scan(&r.l.UserID, &r.l.Name, &r.l.Number, &first, &r.sec, &r.webSec); err != nil {
			rows.Close()
			return nil, err
		}
		r.l.Dup = !first
		got = append(got, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Line, 0, len(got))
	for _, r := range got {
		if r.sec == nil {
			if r.sec, r.webSec, err = s.ensure(ctx, r.l.UserID); err != nil {
				return nil, err
			}
		}
		if r.l.Password, err = s.open(r.sec); err != nil {
			return nil, err
		}
		if r.l.WebPassword, err = s.open(r.webSec); err != nil {
			return nil, err
		}
		out = append(out, r.l)
	}
	return out, nil
}

// ensure gives the employee their passwords if they have none (another request may be doing it at the same time).
func (s *Store) ensure(ctx context.Context, userID string) (sec, webSec []byte, err error) {
	if sec, err = s.seal(Password(12)); err != nil {
		return nil, nil, err
	}
	if webSec, err = s.seal(Password(24)); err != nil {
		return nil, nil, err
	}
	err = s.db.QueryRow(ctx, `INSERT INTO telephony_lines (user_id, secret, web_secret) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET user_id = EXCLUDED.user_id RETURNING secret, web_secret`, userID, sec, webSec).Scan(&sec, &webSec)
	return sec, webSec, err
}

// ResetPassword gives the employee a new password of their SIP phone (the old one stops working) and returns it.
func (s *Store) ResetPassword(ctx context.Context, userID string) (string, error) {
	if _, ok, err := s.LineOf(ctx, userID); err != nil || !ok {
		if err == nil {
			err = ErrNotFound
		}
		return "", err
	}
	pw := Password(12)
	sec, err := s.seal(pw)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `UPDATE telephony_lines SET secret = $2, updated_at = now() WHERE user_id = $1`, userID, sec)
	return pw, err
}

// SetNumber gives the employee an inner number ("" — takes it away); no other employee may have it.
func (s *Store) SetNumber(ctx context.Context, userID, number string) error {
	number = strings.TrimSpace(number)
	if number != "" && !numberRe.MatchString(number) {
		return i18n.E("err.inner_phone")
	}
	if number != "" {
		var other string
		err := s.db.QueryRow(ctx, `SELECT CASE WHEN name <> '' THEN name ELSE email END FROM users
			WHERE inner_phone = $1 AND id::text <> $2 AND active AND NOT guest LIMIT 1`, number, userID).Scan(&other)
		if err == nil {
			return i18n.E("err.inner_phone_taken", number, other)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE users SET inner_phone = $2 WHERE id::text = $1`, userID, number)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Password is a random password of n letters and digits (without the ones easy to mix up: 0 O 1 l I).
func Password(n int) string {
	const abc = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, big.NewInt(int64(len(abc))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported systems
		}
		b[i] = abc[k.Int64()]
	}
	return string(b)
}

func (s *Store) seal(plain string) ([]byte, error) {
	gcm, err := s.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) open(sealed []byte) (string, error) {
	gcm, err := s.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("telephony: bad secret")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

func (s *Store) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// NumberOf is the employee's inner number while telephony is on and the number is theirs ("" — none: no softphone).
func (s *Store) NumberOf(ctx context.Context, userID string) (string, error) {
	var n string
	err := s.db.QueryRow(ctx, `SELECT u.inner_phone FROM users u
		WHERE u.id::text = $1 AND u.active AND NOT u.guest AND u.inner_phone <> ''
		  AND EXISTS (SELECT 1 FROM company_settings WHERE key = $2 AND value = 'true'::jsonb)
		  AND NOT EXISTS (SELECT 1 FROM users o WHERE o.inner_phone = u.inner_phone AND o.active AND NOT o.guest
		                  AND (o.created_at, o.id) < (u.created_at, u.id))`, userID, settingEnabled).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// Who is the one on the other end of a call, as far as go24 knows them.
type Who struct {
	UserID, Name, PhotoKey string
}

// WhoIs finds the employee with the inner number; false — nobody (an outside number).
func (s *Store) WhoIs(ctx context.Context, number string) (Who, bool, error) {
	var w Who
	err := s.db.QueryRow(ctx, `SELECT id::text, CASE WHEN name <> '' THEN name ELSE email END, photo_key FROM users
		WHERE inner_phone = $1 AND active AND NOT guest ORDER BY created_at, id LIMIT 1`, number).Scan(&w.UserID, &w.Name, &w.PhotoKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Who{}, false, nil
	}
	return w, err == nil, err
}

// Dialable is a number one may dial: digits, maybe with + * # (spaces, dashes and brackets are dropped); "" — not one.
func Dialable(s string) string {
	s = strings.TrimPrefix(strings.TrimSpace(s), "tel:")
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '*', r == '#', r == '+' && i == 0:
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '(' || r == ')' || r == '.':
		default:
			return ""
		}
	}
	if b.Len() == 0 || b.Len() > 20 {
		return ""
	}
	return b.String()
}

// Notify hands an event to go24's pages (go24_notify → the live streams): key "c<user>" — the user's pages,
// "p<user>.<phone>" — one softphone window of theirs.
func (s *Store) Notify(ctx context.Context, key string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `SELECT pg_notify('go24_notify', $1 || ':' || $2)`, key, string(data))
	return err
}
