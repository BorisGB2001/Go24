package mail

import (
	"context"
	"net/mail"
	"regexp"
	"strings"

	"go24/internal/i18n"
)

// «Отсортированные» and «Другие», as Outlook's Focused Inbox: a new letter of the inbox goes under «Другие» when it
// looks sent to many — a newsletter (List-Unsubscribe, List-Id, Precedence: bulk), a robot's notice (Auto-Submitted,
// no-reply@…) — unless the user wrote to its sender. go24 learns from what the user does: a letter moved to the other
// tab, a flag, a letter deleted unread change the sender's score, and «Всегда перемещать» is the sender's rule.

// Focus views of an inbox.
const (
	ViewFocused = "focused"
	ViewOther   = "other"
)

// learnt: a sender's score from this far on decides by itself.
const learnt = 2

// reRobot are the addresses of robots and newsletters.
var reRobot = regexp.MustCompile(`(?i)^(no-?reply|do-?not-?reply|notifications?|news(letter)?|mailer-daemon|bounce[sd]?|marketing|promo|digest|alerts?)([+.-].*)?@`)

// Bulk: does the letter's header say it is sent to many, or by a robot?
func Bulk(from string, header func(string) string) bool {
	if reRobot.MatchString(from) {
		return true
	}
	if header == nil {
		return false
	}
	if header("List-Unsubscribe") != "" || header("List-Id") != "" || header("X-Campaign") != "" || header("X-Mailchimp-Campaign") != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(header("Precedence"))) {
	case "bulk", "list", "junk":
		return true
	}
	auto := strings.ToLower(strings.TrimSpace(header("Auto-Submitted")))
	return auto != "" && auto != "no"
}

// otherFor: does a new inbox letter of the sender go under «Другие» for the user?
func (s *Store) otherFor(ctx context.Context, userID, from string, bulk bool) (bool, error) {
	from = strings.ToLower(strings.TrimSpace(from))
	if from == "" {
		return false, nil
	}
	var score int
	var always string
	var wrote bool
	err := s.db.QueryRow(ctx, `SELECT coalesce((SELECT score FROM mail_focus WHERE user_id::text = $1 AND sender = $2), 0),
			coalesce((SELECT always FROM mail_focus WHERE user_id::text = $1 AND sender = $2), ''),
			EXISTS (SELECT 1 FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
				WHERE m.user_id::text = $1 AND f.role = 'sent' AND (x.to_addr || ',' || x.cc_addr) ILIKE $3)`,
		userID, from, likeOf(from)).Scan(&score, &always, &wrote)
	switch {
	case err != nil:
		return false, err
	case always != "":
		return always == ViewOther, nil
	case score >= learnt:
		return false, nil
	case score <= -learnt:
		return true, nil
	case wrote:
		return false, nil
	}
	return bulk, nil
}

// senders are the senders of letters (lower case, each once).
func (s *Store) senders(ctx context.Context, ids []string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT lower(from_addr) FROM mail_messages WHERE id::text = ANY($1) AND from_addr <> ''`, ids)
	if err != nil {
		return nil, err
	}
	return pgxCollectStrings(rows)
}

// learn adds to the senders' scores (kept within ±10).
func (s *Store) learn(ctx context.Context, userID string, senders []string, d int) error {
	for _, from := range senders {
		if _, err := s.db.Exec(ctx, `INSERT INTO mail_focus (user_id, sender, score) VALUES ($1, $2, greatest(-10, least(10, $3)))
			ON CONFLICT (user_id, sender) DO UPDATE SET score = greatest(-10, least(10, mail_focus.score + $3))`, userID, from, d); err != nil {
			return err
		}
	}
	return nil
}

// focus moves letters to «Отсортированные» or «Другие» (view); always — the senders' letters go there from now on
// (and those in the inbox now go too), else go24 learns it of the senders.
func (s *Store) focus(ctx context.Context, userID string, ids []string, view string, always bool) error {
	other := view == ViewOther
	from, err := s.senders(ctx, ids)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `UPDATE mail_messages SET other = $2 WHERE id::text = ANY($1)`, ids, other); err != nil {
		return err
	}
	if !always {
		d := learnt
		if other {
			d = -learnt
		}
		return s.learn(ctx, userID, from, d)
	}
	for _, f := range from {
		if _, err := s.db.Exec(ctx, `INSERT INTO mail_focus (user_id, sender, always) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, sender) DO UPDATE SET always = EXCLUDED.always`, userID, f, view); err != nil {
			return err
		}
	}
	_, err = s.db.Exec(ctx, `UPDATE mail_messages x SET other = $3 FROM mail_folders f, mailboxes m
		WHERE f.id = x.folder_id AND m.id = f.mailbox_id AND m.user_id::text = $1 AND f.role = 'inbox' AND lower(x.from_addr) = ANY($2)`, userID, from, other)
	return err
}

// FocusRule is a sender's «Всегда перемещать».
type FocusRule struct{ Sender, View string }

// FocusRules are the user's rules, by sender.
func (s *Store) FocusRules(ctx context.Context, userID string) ([]FocusRule, error) {
	rows, err := s.db.Query(ctx, `SELECT sender, always FROM mail_focus WHERE user_id::text = $1 AND always <> '' ORDER BY sender`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FocusRule
	for rows.Next() {
		var r FocusRule
		if err := rows.Scan(&r.Sender, &r.View); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetFocusRule sets a sender's «Всегда перемещать» (view: ViewFocused or ViewOther); the sender's letters in the
// inbox go there now.
func (s *Store) SetFocusRule(ctx context.Context, userID, sender, view string) error {
	sender = strings.ToLower(strings.TrimSpace(sender))
	if view != ViewFocused && view != ViewOther {
		return i18n.E("err.bad_form")
	}
	if a, err := mail.ParseAddress(sender); err != nil || a.Address != sender {
		return i18n.E("err.mail_email")
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO mail_focus (user_id, sender, always) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, sender) DO UPDATE SET always = EXCLUDED.always`, userID, sender, view); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE mail_messages x SET other = $3 FROM mail_folders f, mailboxes m
		WHERE f.id = x.folder_id AND m.id = f.mailbox_id AND m.user_id::text = $1 AND f.role = 'inbox' AND lower(x.from_addr) = $2`,
		userID, sender, view == ViewOther)
	return err
}

// DropFocusRule takes a sender's rule off (what go24 learnt stays).
func (s *Store) DropFocusRule(ctx context.Context, userID, sender string) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_focus SET always = '' WHERE user_id::text = $1 AND sender = lower($2) AND always <> ''`, userID, sender)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// OtherNews is what the hint over «Отсортированные» tells of «Другие»: the unread letters there (in a folder, or in
// all the inboxes) and the names of their latest senders (three at most).
func (s *Store) OtherNews(ctx context.Context, userID, folderID string) (int, []string, error) {
	where := `f.id::text = $2`
	if folderID == AllInboxes {
		where = `f.role = 'inbox' AND $2 <> ''`
	}
	var n int
	var names []string
	err := s.db.QueryRow(ctx, `SELECT count(*)::int, coalesce((SELECT array_agg(name) FROM (SELECT name FROM (
				SELECT coalesce(nullif(x.from_name, ''), x.from_addr) AS name, max(x.sent_at) AS at FROM mail_messages x
				JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
				WHERE m.user_id::text = $1 AND `+where+` AND x.other AND NOT x.seen AND (x.snooze_until IS NULL OR x.snooze_until <= now())
				GROUP BY 1) a ORDER BY at DESC LIMIT 3) b), '{}')
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE m.user_id::text = $1 AND `+where+` AND x.other AND NOT x.seen AND (x.snooze_until IS NULL OR x.snooze_until <= now())`,
		userID, folderID).Scan(&n, &names)
	return n, names, err
}
