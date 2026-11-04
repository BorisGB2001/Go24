package company

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// «Терминал учёта времени».Time: a tablet at the office entrance opens the terminal's secret link, an
// employee types the personal PIN there and starts, pauses or finishes the working day without signing in.

// Terminal is a time clock of the company.
type Terminal struct {
	ID          int64
	Name, Token string
	Active      bool
	Fails       int
	LockedUntil *time.Time
	UsedAt      *time.Time
	CreatedAt   time.Time
}

const (
	terminalFails = 5           // wrong PINs in a row that lock the terminal
	terminalLock  = time.Minute // for how long
)

const terminalSQL = `SELECT id, name, token, active, fails, locked_until, used_at, created_at FROM worktime_terminals`

func scanTerminals(rows pgx.Rows, err error) ([]Terminal, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Terminal, error) {
		var t Terminal
		err := r.Scan(&t.ID, &t.Name, &t.Token, &t.Active, &t.Fails, &t.LockedUntil, &t.UsedAt, &t.CreatedAt)
		return t, err
	})
}

// Terminals are the company's terminals by name.
func (s *Store) Terminals(ctx context.Context) ([]Terminal, error) {
	return scanTerminals(s.db.Query(ctx, terminalSQL+` ORDER BY name, id`))
}

// Terminal is one terminal (nil — none).
func (s *Store) Terminal(ctx context.Context, id int64) (*Terminal, error) {
	list, err := scanTerminals(s.db.Query(ctx, terminalSQL+` WHERE id = $1`, id))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// TerminalByToken is the working terminal of the link (nil — no such link or switched off).
func (s *Store) TerminalByToken(ctx context.Context, token string) (*Terminal, error) {
	if token == "" {
		return nil, nil
	}
	list, err := scanTerminals(s.db.Query(ctx, terminalSQL+` WHERE token = $1 AND active`, token))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func terminalName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", i18n.E("err.wterm_name")
	}
	return name, nil
}

// AddTerminal makes a terminal with a new link.
func (s *Store) AddTerminal(ctx context.Context, name string) (*Terminal, error) {
	name, err := terminalName(name)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := s.db.QueryRow(ctx, `INSERT INTO worktime_terminals (name, token) VALUES ($1, $2) RETURNING id`,
		name, newToken()).Scan(&id); err != nil {
		return nil, err
	}
	return s.Terminal(ctx, id)
}

// UpdateTerminal renames a terminal and switches it on or off.
func (s *Store) UpdateTerminal(ctx context.Context, id int64, name string, active bool) error {
	name, err := terminalName(name)
	if err != nil {
		return err
	}
	return s.terminalExec(ctx, `UPDATE worktime_terminals SET name = $2, active = $3 WHERE id = $1`, id, name, active)
}

// NewTerminalLink gives the terminal a new link: the old one stops working.
func (s *Store) NewTerminalLink(ctx context.Context, id int64) error {
	return s.terminalExec(ctx, `UPDATE worktime_terminals SET token = $2, fails = 0, locked_until = NULL WHERE id = $1`,
		id, newToken())
}

// DeleteTerminal removes a terminal; the days marked at it keep its name.
func (s *Store) DeleteTerminal(ctx context.Context, id int64) error {
	return s.terminalExec(ctx, `DELETE FROM worktime_terminals WHERE id = $1`, id)
}

func (s *Store) terminalExec(ctx context.Context, sql string, args ...any) error {
	tag, err := s.db.Exec(ctx, sql, args...)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.not_found")
	}
	return err
}

// pinHash is the kept form of a PIN: an HMAC with the server's key, the same for the same PIN — so a PIN is found
// and kept unique, while the table alone does not give the PINs away.
func (s *Store) pinHash(pin string) string {
	m := hmac.New(sha256.New, append([]byte("go24 worktime pin\x00"), s.Key...))
	m.Write([]byte(pin))
	return hex.EncodeToString(m.Sum(nil))
}

// ValidPIN: 4 to 8 digits.
func ValidPIN(pin string) bool {
	if len(pin) < 4 || len(pin) > 8 {
		return false
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// SetWorkPIN sets the person's PIN for the terminals; "" removes it. A PIN of someone else is refused.
func (s *Store) SetWorkPIN(ctx context.Context, userID, pin string) error {
	if pin == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM worktime_pins WHERE user_id = $1`, userID)
		return err
	}
	if !ValidPIN(pin) {
		return i18n.E("err.wpin_format")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO worktime_pins (user_id, pin_hash) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET pin_hash = EXCLUDED.pin_hash, set_at = now()`, userID, s.pinHash(pin))
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23505" {
		return i18n.E("err.wpin_taken")
	}
	return err
}

// HasWorkPIN: the person has a PIN for the terminals.
func (s *Store) HasWorkPIN(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM worktime_pins WHERE user_id = $1)`, userID).Scan(&ok)
	return ok, err
}

// TerminalPerson finds the active employee of the PIN typed at the terminal. Wrong PINs are counted: after
// terminalFails in a row the terminal is locked for terminalLock, the right one resets the count.
func (s *Store) TerminalPerson(ctx context.Context, t *Terminal, pin string, now time.Time) (*Person, error) {
	var locked *time.Time
	if err := s.db.QueryRow(ctx, `SELECT locked_until FROM worktime_terminals WHERE id = $1`, t.ID).Scan(&locked); err != nil {
		return nil, err
	}
	if locked != nil && now.Before(*locked) {
		return nil, i18n.E("err.wterm_locked")
	}
	var p Person
	err := pgx.ErrNoRows
	if ValidPIN(pin) {
		err = s.db.QueryRow(ctx, `SELECT u.id::text, u.name, u.position, u.photo_key FROM worktime_pins p
			JOIN users u ON u.id = p.user_id WHERE p.pin_hash = $1 AND u.active AND NOT u.guest`, s.pinHash(pin)).
			Scan(&p.ID, &p.Name, &p.Position, &p.PhotoKey)
	}
	if errors.Is(err, pgx.ErrNoRows) { // one wrong more; the last of the row locks and starts the count anew
		_, err = s.db.Exec(ctx, `UPDATE worktime_terminals SET
			locked_until = CASE WHEN fails + 1 >= $2 THEN $3::timestamptz ELSE locked_until END,
			fails = CASE WHEN fails + 1 >= $2 THEN 0 ELSE fails + 1 END WHERE id = $1`, t.ID, terminalFails, now.Add(terminalLock))
		if err != nil {
			return nil, err
		}
		return nil, i18n.E("err.wterm_pin")
	}
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(ctx, `UPDATE worktime_terminals SET fails = 0, locked_until = NULL, used_at = $2 WHERE id = $1`,
		t.ID, now)
	return &p, err
}

// WorkPINs are the people with a PIN for the terminals.
func (s *Store) WorkPINs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text FROM worktime_pins`)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// TerminalsOn: the company has a working terminal — the people need PINs.
func (s *Store) TerminalsOn(ctx context.Context) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM worktime_terminals WHERE active)`).Scan(&ok)
	return ok, err
}
