package mail

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// What the user sets up for writing letters, as in Outlook's «Параметры → Почта → Создание и ответ»: signatures
// (several; each mailbox has one put into new letters and one into replies and forwards), «Мои шаблоны», the
// seconds to undo sending and the letter's font. Recipients are suggested from the people the user wrote with.

// Snippet is a signature or a template of the user: a name and its HTML.
type Snippet struct{ ID, Name, HTML string }

// snippetTable: signatures or templates.
func snippetTable(kind string) string {
	if kind == "template" {
		return "mail_templates"
	}
	return "mail_signatures"
}

// Snippets are the user's signatures (kind "signature") or templates ("template"), by name.
func (s *Store) Snippets(ctx context.Context, userID, kind string) ([]Snippet, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, html FROM `+snippetTable(kind)+` WHERE user_id::text = $1 ORDER BY lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Snippet])
}

// Snippet is a signature or a template of the user.
func (s *Store) Snippet(ctx context.Context, userID, kind, id string) (Snippet, error) {
	var x Snippet
	err := s.db.QueryRow(ctx, `SELECT id::text, name, html FROM `+snippetTable(kind)+` WHERE id::text = $1 AND user_id::text = $2`, id, userID).
		Scan(&x.ID, &x.Name, &x.HTML)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// SaveSnippet makes (id "") or changes a signature or a template of the user.
func (s *Store) SaveSnippet(ctx context.Context, userID, kind, id, name, html string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 100 {
		return "", i18n.E("err.mail_snippet_name")
	}
	html = Sanitize(html)
	var err error
	if id == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO `+snippetTable(kind)+` (user_id, name, html) VALUES ($1, $2, $3) RETURNING id::text`, userID, name, html).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		tag, err = s.db.Exec(ctx, `UPDATE `+snippetTable(kind)+` SET name = $3, html = $4 WHERE id::text = $1 AND user_id::text = $2`, id, userID, name, html)
		if err == nil && tag.RowsAffected() == 0 {
			err = ErrNotFound
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", i18n.E("err.mail_snippet_exists")
	}
	return id, err
}

// DeleteSnippet deletes a signature (the mailboxes using it are left without one) or a template of the user.
func (s *Store) DeleteSnippet(ctx context.Context, userID, kind, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM `+snippetTable(kind)+` WHERE id::text = $1 AND user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Signatures of a mailbox: put into new letters, into replies and forwards ("" — none).
type Signatures struct{ New, Reply string }

// MailboxSignatures are the mailbox's signatures.
func (s *Store) MailboxSignatures(ctx context.Context, userID, mailboxID string) (Signatures, error) {
	var sg Signatures
	err := s.db.QueryRow(ctx, `SELECT coalesce(sig_new::text, ''), coalesce(sig_reply::text, '') FROM mailboxes WHERE id::text = $1 AND user_id::text = $2`,
		mailboxID, userID).Scan(&sg.New, &sg.Reply)
	if errors.Is(err, pgx.ErrNoRows) {
		return sg, ErrNotFound
	}
	return sg, err
}

// SetSignatures sets the mailbox's signatures (the user's own only).
func (s *Store) SetSignatures(ctx context.Context, userID, mailboxID string, sg Signatures) error {
	for _, id := range []string{sg.New, sg.Reply} {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT $1 = '' OR EXISTS (SELECT 1 FROM mail_signatures WHERE id::text = $1 AND user_id::text = $2)`,
			id, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET sig_new = nullif($3, '')::uuid, sig_reply = nullif($4, '')::uuid WHERE id::text = $1 AND user_id::text = $2`,
		mailboxID, userID, sg.New, sg.Reply)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Prefs are the user's mail settings.
type Prefs struct {
	UndoSeconds    int // «Отменить» is offered so long after «Отправить» (0 — off)
	Font, FontSize string
	Focused        bool // the inbox shows «Отсортированные» and «Другие»
}

// Fonts and sizes the editor offers (the letter's default among them).
var (
	Fonts     = []string{"Calibri", "Arial", "Verdana", "Tahoma", "Segoe UI", "Times New Roman", "Georgia", "Courier New"}
	FontSizes = []string{"8pt", "9pt", "10pt", "11pt", "12pt", "14pt", "16pt", "18pt", "24pt", "36pt"}
)

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Prefs are the user's mail settings (the defaults when not set).
func (s *Store) Prefs(ctx context.Context, userID string) (Prefs, error) {
	p := Prefs{UndoSeconds: 5, Focused: true}
	err := s.db.QueryRow(ctx, `SELECT undo_seconds, font, font_size, focused FROM mail_settings WHERE user_id::text = $1`, userID).Scan(&p.UndoSeconds,
		&p.Font, &p.FontSize, &p.Focused)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}

// SavePrefs keeps the user's mail settings.
func (s *Store) SavePrefs(ctx context.Context, userID string, p Prefs) error {
	if p.UndoSeconds < 0 || p.UndoSeconds > 10 || (p.Font != "" && !oneOf(p.Font, Fonts)) || (p.FontSize != "" && !oneOf(p.FontSize, FontSizes)) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, undo_seconds, font, font_size) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET undo_seconds = EXCLUDED.undo_seconds, font = EXCLUDED.font, font_size = EXCLUDED.font_size`,
		userID, p.UndoSeconds, p.Font, p.FontSize)
	return err
}

// SetFocused turns «Отсортированные» and «Другие» of the inbox on or off.
func (s *Store) SetFocused(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, focused) VALUES ($1, $2) ON CONFLICT (user_id) DO UPDATE SET focused = EXCLUDED.focused`,
		userID, on)
	return err
}

// Handling is Outlook's «Обработка сообщений»: when an opened letter is marked read, what is done with a letter
// asking for a read receipt, what the reading pane shows after its letter is moved or deleted.
type Handling struct {
	ReadAfter int    // 0 — when it opens in the reading pane, N — after N seconds there, -1 — never by itself
	Receipts  string // one of ReceiptModes
	AfterMove string // one of AfterMoveModes
}

// The choices of «Обработка сообщений».
var (
	ReadAfters     = []int{0, 5, 10, 30, -1}
	ReceiptModes   = []string{"ask", "always", "never"}
	AfterMoveModes = []string{"next", "prev", "folder"}
)

// Handling of the user (the defaults when not set).
func (s *Store) Handling(ctx context.Context, userID string) (Handling, error) {
	h := Handling{Receipts: "ask", AfterMove: "next"}
	err := s.db.QueryRow(ctx, `SELECT read_after, receipts, after_move FROM mail_settings WHERE user_id::text = $1`, userID).Scan(&h.ReadAfter,
		&h.Receipts, &h.AfterMove)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return h, err
}

// SaveHandling keeps the user's «Обработка сообщений».
func (s *Store) SaveHandling(ctx context.Context, userID string, h Handling) error {
	ok := false
	for _, n := range ReadAfters {
		ok = ok || n == h.ReadAfter
	}
	if !ok || !oneOf(h.Receipts, ReceiptModes) || !oneOf(h.AfterMove, AfterMoveModes) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, read_after, receipts, after_move) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET read_after = EXCLUDED.read_after, receipts = EXCLUDED.receipts, after_move = EXCLUDED.after_move`,
		userID, h.ReadAfter, h.Receipts, h.AfterMove)
	return err
}

// Layout is Outlook's «Макет»: how dense the letter list is, the text size of the list and the reading pane, what a
// row shows besides the sender and the subject.
type Layout struct {
	Density   string // one of Densities
	TextSize  string // one of TextSizes
	ShowText  bool   // a line of the letter's text
	ShowPics  bool   // the sender's picture
	ShowFiles bool   // the names of the letter's files
}

// The choices of «Макет».
var (
	Densities = []string{"roomy", "cozy", "compact"}
	TextSizes = []string{"small", "medium", "large"}
)

// Layout of the user (the defaults when not set).
func (s *Store) Layout(ctx context.Context, userID string) (Layout, error) {
	l := Layout{Density: "cozy", TextSize: "medium", ShowText: true, ShowPics: true, ShowFiles: true}
	err := s.db.QueryRow(ctx, `SELECT density, text_size, show_text, show_pics, show_files FROM mail_settings WHERE user_id::text = $1`,
		userID).Scan(&l.Density, &l.TextSize, &l.ShowText, &l.ShowPics, &l.ShowFiles)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return l, err
}

// SaveLayout keeps the user's «Макет».
func (s *Store) SaveLayout(ctx context.Context, userID string, l Layout) error {
	if !oneOf(l.Density, Densities) || !oneOf(l.TextSize, TextSizes) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, density, text_size, show_text, show_pics, show_files) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id) DO UPDATE SET density = EXCLUDED.density, text_size = EXCLUDED.text_size, show_text = EXCLUDED.show_text,
			show_pics = EXCLUDED.show_pics, show_files = EXCLUDED.show_files`,
		userID, l.Density, l.TextSize, l.ShowText, l.ShowPics, l.ShowFiles)
	return err
}

// Recipient is an address suggested while typing whom a letter is to.
type Recipient struct {
	Name, Email string
	Kind        string // employee, crm, history
}

// Correspondents are the addresses of the user's letters matching q — the people who wrote to the user and those
// the user wrote to, the most frequent and recent first (the user's own mailboxes and junk left out).
func (s *Store) Correspondents(ctx context.Context, userID, q string, limit int) ([]Recipient, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `WITH a AS (
			SELECT lower(x.from_addr) AS email, x.from_name AS name, x.sent_at, 1 AS w FROM mail_messages x
				JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
				WHERE m.user_id::text = $1 AND f.role NOT IN ('junk', 'sent', 'drafts') AND x.from_addr <> ''
			UNION ALL
			SELECT lower(trim(t)), '', x.sent_at, 3 FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id,
				unnest(string_to_array(x.to_addr || ',' || x.cc_addr || ',' || x.bcc_addr, ',')) t
				WHERE m.user_id::text = $1 AND f.role = 'sent')
		SELECT max(name), email FROM a WHERE email LIKE '%@%' AND email NOT IN (SELECT lower(email) FROM mailboxes WHERE user_id::text = $1)
			AND (email ILIKE $2 OR name ILIKE $2)
		GROUP BY email ORDER BY sum(w) DESC, max(sent_at) DESC LIMIT $3`, userID, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		r := Recipient{Kind: "history"}
		if err := rows.Scan(&r.Name, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Employees are go24's people (working ones, with an e-mail) matching q.
func (s *Store) Employees(ctx context.Context, q string, limit int) ([]Recipient, error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `SELECT name, email FROM users WHERE active AND email LIKE '%@%' AND (name ILIKE $1 OR email ILIKE $1)
		ORDER BY lower(name) LIMIT $2`, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recipient
	for rows.Next() {
		r := Recipient{Kind: "employee"}
		if err := rows.Scan(&r.Name, &r.Email); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
