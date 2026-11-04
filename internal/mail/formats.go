package mail

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Условное форматирование», as Outlook's: the user's own rules that set off letters in the list — a colour, bold,
// italic, underlined — by the conditions of the rules (Cond: all of them must hold) and «Не прочитано». The first rule
// on that takes a letter (a conversation: its newest letter) decides its look; a letter no rule takes looks as usual.

// Look is how a letter of the list is set off.
type Look struct {
	Color     string `json:"color"` // one of Colors
	Bold      bool   `json:"bold,omitempty"`
	Italic    bool   `json:"italic,omitempty"`
	Underline bool   `json:"underline,omitempty"`
}

// Format is a rule of «Условное форматирование».
type Format struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Pos     int    `json:"pos"`
	If      Cond   `json:"if"`
	Unread  bool   `json:"unread,omitempty"` // the letter is not read
	Look    Look   `json:"look"`
}

// MaxFormats bounds the user's rules of «Условное форматирование».
const MaxFormats = 50

// takes: does the rule take the letter?
func (f Format) takes(m Message) bool {
	if f.Unread && m.Seen && m.Unread == 0 {
		return false
	}
	x := facts{from: m.FromAddr, name: m.FromName, to: m.To, cc: m.CC, subject: m.Subject, body: m.Body, me: m.MailboxEmail,
		attach: m.Attached > 0, importance: m.Importance, sensitivity: m.Sensitivity}
	for _, ok := range f.If.tests(x) {
		if !ok {
			return false
		}
	}
	return true
}

// LookOf is the look the first of the rules (those on) gives the letter; nil — none takes it.
func LookOf(formats []Format, m Message) *Look {
	for _, f := range formats {
		if f.Enabled && f.takes(m) {
			l := f.Look
			return &l
		}
	}
	return nil
}

// checkFormat: the name, a condition at least, a colour of Colors.
func checkFormat(f *Format) error {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || utf8.RuneCountInString(f.Name) > 200 {
		return i18n.E("err.mail_rule_name")
	}
	if f.If.Empty() && !f.Unread {
		return i18n.E("err.mail_format_if")
	}
	if f.If.Importance != "" && !oneOf(f.If.Importance, Importances) || f.If.Sensitivity != "" && f.If.Sensitivity != "normal" && !ValidSensitivity(f.If.Sensitivity) {
		return i18n.E("err.bad_form")
	}
	if !oneOf(f.Look.Color, Colors) {
		return i18n.E("err.bad_form")
	}
	return nil
}

const formatCols = `id::text, name, enabled, pos, cond, unread, look`

func scanFormats(rows pgx.Rows, err error) ([]Format, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Format
	for rows.Next() {
		var f Format
		var c, l []byte
		if err := rows.Scan(&f.ID, &f.Name, &f.Enabled, &f.Pos, &c, &f.Unread, &l); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(c, &f.If); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(l, &f.Look); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Formats are the user's rules of «Условное форматирование» in their order.
func (s *Store) Formats(ctx context.Context, userID string) ([]Format, error) {
	return scanFormats(s.db.Query(ctx, `SELECT `+formatCols+` FROM mail_formats WHERE user_id::text = $1 ORDER BY pos, created_at`, userID))
}

// Format is a rule of «Условное форматирование» of the user.
func (s *Store) Format(ctx context.Context, userID, id string) (Format, error) {
	list, err := scanFormats(s.db.Query(ctx, `SELECT `+formatCols+` FROM mail_formats WHERE id::text = $1 AND user_id::text = $2`, id, userID))
	if err != nil {
		return Format{}, err
	}
	if len(list) == 0 {
		return Format{}, ErrNotFound
	}
	return list[0], nil
}

// SaveFormat makes a rule of «Условное форматирование» (f.ID "": the last one) or changes one; its id.
func (s *Store) SaveFormat(ctx context.Context, userID string, f Format) (string, error) {
	if err := checkFormat(&f); err != nil {
		return "", err
	}
	c, _ := json.Marshal(f.If)
	l, _ := json.Marshal(f.Look)
	if f.ID == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_formats WHERE user_id::text = $1`, userID).Scan(&n); err != nil {
			return "", err
		}
		if n >= MaxFormats {
			return "", i18n.E("err.mail_formats_many", MaxFormats)
		}
		err := s.db.QueryRow(ctx, `INSERT INTO mail_formats (user_id, name, enabled, cond, unread, look, pos)
			VALUES ($1, $2, $3, $4, $5, $6, (SELECT coalesce(max(pos), 0) + 1 FROM mail_formats WHERE user_id = $1)) RETURNING id::text`,
			userID, f.Name, f.Enabled, c, f.Unread, l).Scan(&f.ID)
		return f.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE mail_formats SET name = $3, enabled = $4, cond = $5, unread = $6, look = $7
		WHERE id::text = $1 AND user_id::text = $2`, f.ID, userID, f.Name, f.Enabled, c, f.Unread, l)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return f.ID, err
}

// DeleteFormat deletes a rule of «Условное форматирование» of the user.
func (s *Store) DeleteFormat(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM mail_formats WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// EnableFormat turns a rule of «Условное форматирование» on or off.
func (s *Store) EnableFormat(ctx context.Context, userID, id string, on bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_formats SET enabled = $3 WHERE id::text = $1 AND user_id::text = $2`, id, userID, on)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// MoveFormat moves a rule of «Условное форматирование» up (d < 0) or down in the order.
func (s *Store) MoveFormat(ctx context.Context, userID, id string, d int) error {
	list, err := s.Formats(ctx, userID)
	if err != nil {
		return err
	}
	at := -1
	for i, f := range list {
		if f.ID == id {
			at = i
		}
	}
	if at < 0 {
		return ErrNotFound
	}
	to := at + 1
	if d < 0 {
		to = at - 1
	}
	if to < 0 || to >= len(list) {
		return nil
	}
	list[at], list[to] = list[to], list[at]
	for i, f := range list {
		if _, err := s.db.Exec(ctx, `UPDATE mail_formats SET pos = $2 WHERE id::text = $1`, f.ID, i+1); err != nil {
			return err
		}
	}
	return nil
}

// Dress gives the letters of a list the looks of the user's rules of «Условное форматирование».
func (s *Store) Dress(ctx context.Context, userID string, list []Message) error {
	if len(list) == 0 {
		return nil
	}
	formats, err := s.Formats(ctx, userID)
	if err != nil {
		return err
	}
	for i := range list {
		list[i].Look = LookOf(formats, list[i])
	}
	return nil
}
