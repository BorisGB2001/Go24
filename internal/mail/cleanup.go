package mail

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Очистить беседу», «Очистить папку» and «Очистить папку и вложенные папки», as Outlook's Clean Up: a letter whose
// whole text is quoted in a later letter of its conversation (in any folder of the mailbox) is redundant and goes to
// «Удалённые» or «Архив». The latest letter stays, and so do the branches whose reply changed the quote, letters with
// files (a reply does not carry them), pinned letters, drafts and those the settings keep: unread, with a category,
// with a flag, digitally signed («Обработка сообщений → Очистка беседы»). Letters of «Удалённые» and «Нежелательная
// почта» are neither cleaned nor quote others.

// Cleanup is the user's «Очистка беседы» settings.
type Cleanup struct {
	Target                                               string // one of CleanupTargets
	KeepUnread, KeepCategorized, KeepFlagged, KeepSigned bool
}

// CleanupTargets: where the redundant letters go.
var CleanupTargets = []string{"delete", "archive"}

// minQuoted: a shorter text («Ок») is not taken for quoted — it may be met in any letter.
const minQuoted = 10

// Cleanup of the user (the defaults, as Outlook's, when not set).
func (s *Store) Cleanup(ctx context.Context, userID string) (Cleanup, error) {
	c := Cleanup{Target: "delete", KeepUnread: true, KeepCategorized: true, KeepFlagged: true, KeepSigned: true}
	err := s.db.QueryRow(ctx, `SELECT cleanup_target, cleanup_unread, cleanup_categorized, cleanup_flagged, cleanup_signed FROM mail_settings
		WHERE user_id::text = $1`, userID).Scan(&c.Target, &c.KeepUnread, &c.KeepCategorized, &c.KeepFlagged, &c.KeepSigned)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return c, err
}

// SaveCleanup keeps the user's «Очистка беседы» settings.
func (s *Store) SaveCleanup(ctx context.Context, userID string, c Cleanup) error {
	if !oneOf(c.Target, CleanupTargets) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, cleanup_target, cleanup_unread, cleanup_categorized, cleanup_flagged, cleanup_signed)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (user_id) DO UPDATE SET cleanup_target = EXCLUDED.cleanup_target, cleanup_unread = EXCLUDED.cleanup_unread,
		cleanup_categorized = EXCLUDED.cleanup_categorized, cleanup_flagged = EXCLUDED.cleanup_flagged, cleanup_signed = EXCLUDED.cleanup_signed`,
		userID, c.Target, c.KeepUnread, c.KeepCategorized, c.KeepFlagged, c.KeepSigned)
	return err
}

// CleanUpConversations cleans the conversations of the letters of the user; how many letters went.
func (s *Store) CleanUpConversations(ctx context.Context, userID string, letterIDs []string) (int, error) {
	return s.cleanUp(ctx, userID, `SELECT y.mailbox_id, y.thread FROM mail_messages y JOIN mailboxes my ON my.id = y.mailbox_id
		WHERE y.id::text = ANY($2) AND my.user_id::text = $1 AND y.thread <> ''`, letterIDs, nil)
}

// CleanUpFolder cleans the conversations of a folder of the user (with sub — of its subfolders too): only the
// letters of these folders go; how many.
func (s *Store) CleanUpFolder(ctx context.Context, userID, folderID string, sub bool) (int, error) {
	f, err := s.Folder(ctx, userID, folderID)
	if err != nil {
		return 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM mail_folders WHERE mailbox_id::text = $1 AND (id::text = $2
		OR ($3 AND delim <> '' AND starts_with(name, $4 || delim)))`, f.MailboxID, f.ID, sub, f.Name)
	if err != nil {
		return 0, err
	}
	folders, err := pgxCollectStrings(rows)
	if err != nil {
		return 0, err
	}
	in := map[string]bool{}
	for _, id := range folders {
		in[id] = true
	}
	return s.cleanUp(ctx, userID, `SELECT y.mailbox_id, y.thread FROM mail_messages y WHERE y.folder_id::text = ANY($2) AND y.thread <> ''`, folders, in)
}

// cleanUp moves the redundant letters of the conversations threads picks ($1 — the user, $2 — ids); folders — the
// only folders whose letters may go (nil — any).
func (s *Store) cleanUp(ctx context.Context, userID, threads string, ids []string, folders map[string]bool) (int, error) {
	c, err := s.Cleanup(ctx, userID)
	if err != nil {
		return 0, err
	}
	keepRole := "-" // a letter of «Архив» is not archived again
	if c.Target == "archive" {
		keepRole = "archive"
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.folder_id::text, x.mailbox_id::text || ' ' || x.thread, x.sent_at, x.body, x.body_html,
			x.pinned OR EXISTS (SELECT 1 FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = '') OR ($3 AND NOT x.seen)
			OR ($4 AND cardinality(x.categories) > 0) OR ($5 AND x.flagged) OR ($6 AND x.smime_sign <> '') OR f.role = $7
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE m.user_id::text = $1 AND f.role NOT IN ('trash', 'junk', 'drafts') AND NOT EXISTS (SELECT 1 FROM mail_drafts d WHERE d.id = x.id)
			AND (x.mailbox_id, x.thread) IN (`+threads+`)`, userID, ids, c.KeepUnread, c.KeepCategorized, c.KeepFlagged, c.KeepSigned, keepRole)
	if err != nil {
		return 0, err
	}
	var list []cleanLetter
	for rows.Next() {
		var l cleanLetter
		var body, html string
		if err := rows.Scan(&l.id, &l.folder, &l.thread, &l.sent, &body, &html, &l.keep); err != nil {
			rows.Close()
			return 0, err
		}
		if strings.TrimSpace(body) == "" {
			body = HTMLText(html)
		}
		l.text = quoteText(body)
		l.keep = l.keep || (folders != nil && !folders[l.folder])
		list = append(list, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	gone := redundant(list)
	if len(gone) == 0 {
		return 0, nil
	}
	letters, err := s.letters(ctx, userID, gone)
	if err != nil {
		return 0, err
	}
	return len(letters), s.file(ctx, userID, letters, c.Target)
}

// cleanLetter is a letter of a conversation being cleaned.
type cleanLetter struct {
	id, folder, thread string // thread: the mailbox and the thread
	sent               time.Time
	text               string // quoteText of its text
	keep               bool   // stays even when quoted
}

// redundant are the letters whose text a later letter of their conversation holds whole (the kept ones stay).
func redundant(list []cleanLetter) []string {
	var out []string
	for _, l := range list {
		if l.keep || utf8.RuneCountInString(l.text) < minQuoted {
			continue
		}
		for _, later := range list {
			if later.id != l.id && later.thread == l.thread && later.sent.After(l.sent) && strings.Contains(later.text, l.text) {
				out = append(out, l.id)
				break
			}
		}
	}
	return out
}

// quoteText is a text as a quote of it is compared: its letters and digits only, in lower case — the quote marks
// («>»), the line breaks of the quoting program and the spacing do not matter.
func quoteText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
