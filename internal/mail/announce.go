package mail

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Notifications of new letters, as Outlook's «Уведомления»: a new letter of the inbox — of «Отсортированные» only
// unless the user wants all of them — is a notification of go24's bell (and so of the messenger, as the user's
// notification settings say; kind "mail"), a pop-up on the desktop with a sound and the counter of the menu; a rule
// may announce letters of any folder (Do.Notify). Open pages hear of new letters through go24's live stream
// (PostgreSQL NOTIFY go24_notify "m<user>:<JSON>").

// The notification modes: off, «Отсортированные» only, all the new letters of the inbox.
var NotifyModes = []string{"off", "focused", "all"}

// NotifyPrefs are how the user is told of new letters.
type NotifyPrefs struct {
	Mode           string // one of NotifyModes
	Sound, Desktop bool
}

// NotifyPrefs of the user (the defaults when not set).
func (s *Store) NotifyPrefs(ctx context.Context, userID string) (NotifyPrefs, error) {
	p := NotifyPrefs{Mode: "focused", Sound: true, Desktop: true}
	err := s.db.QueryRow(ctx, `SELECT notify, notify_sound, notify_desktop FROM mail_settings WHERE user_id::text = $1`, userID).Scan(&p.Mode,
		&p.Sound, &p.Desktop)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return p, err
}

// SaveNotifyPrefs keeps how the user is told of new letters.
func (s *Store) SaveNotifyPrefs(ctx context.Context, userID string, p NotifyPrefs) error {
	if !oneOf(p.Mode, NotifyModes) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO mail_settings (user_id, notify, notify_sound, notify_desktop) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET notify = EXCLUDED.notify, notify_sound = EXCLUDED.notify_sound, notify_desktop = EXCLUDED.notify_desktop`,
		userID, p.Mode, p.Sound, p.Desktop)
	return err
}

// Unread is the menu's counter: the unread letters of the user's inboxes (of «Отсортированные» when the inbox is
// split), not snoozed.
func (s *Store) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE m.user_id::text = $1 AND f.role = 'inbox' AND NOT x.seen AND (x.snooze_until IS NULL OR x.snooze_until <= now())
			AND NOT (x.other AND coalesce((SELECT focused FROM mail_settings WHERE user_id = m.user_id), true))`, userID).Scan(&n)
	return n, err
}

// News is what open pages hear of new letters: the letters to pop up (with a sound), or none — the counters and
// the lists only.
type News struct {
	Letters []NewLetter `json:"letters,omitempty"`
	Sound   bool        `json:"sound,omitempty"`
	Desktop bool        `json:"desktop,omitempty"`
	New     int         `json:"new,omitempty"` // letters new in the inbox (the lists reload)
}

// NewLetter is a new letter as a pop-up shows it.
type NewLetter struct {
	ID      string `json:"id"`
	Folder  string `json:"folder"`
	From    string `json:"from"`
	Subject string `json:"subject"`
	Preview string `json:"preview"`
}

// maxAnnounce: more new letters at once are one notification of the bell («N новых писем»).
const maxAnnounce = 3

// announce tells the user of the letters new in a mailbox (still there after the rules) and wakes the open pages.
func (s *Store) announce(ctx context.Context, userID string, news []arrived) error {
	p, err := s.NotifyPrefs(ctx, userID)
	if err != nil {
		return err
	}
	ids := make([]string, len(news))
	for i, n := range news {
		ids[i] = n.id
	}
	byRule, err := s.notifyRules(ctx, userID, ids)
	if err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.folder_id::text, f.role, x.other, x.seen, coalesce(nullif(x.from_name, ''), x.from_addr),
			x.subject, left(x.body, 200)
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = x.mailbox_id
		WHERE x.id::text = ANY($1) AND m.user_id::text = $2 ORDER BY x.sent_at`, ids, userID)
	if err != nil {
		return err
	}
	var told []NewLetter
	for rows.Next() {
		var l NewLetter
		var role string
		var other, seen bool
		if err := rows.Scan(&l.ID, &l.Folder, &role, &other, &seen, &l.From, &l.Subject, &l.Preview); err != nil {
			rows.Close()
			return err
		}
		inbox := role == "inbox" && !seen && (p.Mode == "all" || (p.Mode == "focused" && !other))
		if (inbox || contains(byRule, l.ID)) && role != "junk" && role != "trash" {
			l.Preview = squeeze(l.Preview)
			told = append(told, l)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(told) > 0 && len(told) <= maxAnnounce {
		for _, l := range told {
			subject := l.Subject
			if subject == "" {
				subject = s.tr("mail.no_subject")
			}
			if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail', left($2, 300), $3)`,
				userID, l.From+" · "+subject, "/mail?folder="+l.Folder+"&letter="+l.ID); err != nil {
				return err
			}
		}
	} else if len(told) > maxAnnounce {
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'mail', $2, '/mail')`,
			userID, s.tr("mail.notify_many", len(told))); err != nil {
			return err
		}
	}
	n := News{Letters: told, Sound: p.Sound && len(told) > 0, Desktop: p.Desktop, New: len(news)}
	if len(n.Letters) > maxAnnounce { // a NOTIFY payload is small: the newest ones
		n.Letters = n.Letters[len(n.Letters)-maxAnnounce:]
	}
	return s.wake(ctx, userID, n)
}

// wake tells the user's open pages that the mail changed (the counters, the list; the news to pop up).
func (s *Store) wake(ctx context.Context, userID string, n News) error {
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `SELECT pg_notify('go24_notify', 'm' || $1 || ':' || $2)`, userID, string(data))
	return err
}

// notifyRules are the letters a rule of the user announces («Уведомить»), wherever the rules put them.
func (s *Store) notifyRules(ctx context.Context, userID string, ids []string) ([]string, error) {
	rules, err := s.enabledRules(ctx, userID)
	if err != nil {
		return nil, err
	}
	some := false
	for _, r := range rules {
		some = some || r.Do.Notify
	}
	if !some {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.mailbox_id::text, x.folder_id::text, x.from_addr, x.from_name, x.to_addr, x.cc_addr,
			x.subject, x.body, m.email, EXISTS (SELECT 1 FROM files fl WHERE fl.mail_id = x.id AND fl.content_id = ''), x.importance::int
		FROM mail_messages x JOIN mailboxes m ON m.id = x.mailbox_id WHERE x.id::text = ANY($1) AND m.user_id::text = $2`, ids, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var f facts
		if err := rows.Scan(&f.id, &f.mailbox, &f.folder, &f.from, &f.name, &f.to, &f.cc, &f.subject, &f.body, &f.me, &f.attach, &f.importance); err != nil {
			return nil, err
		}
		for _, r := range rules { // in their order, up to the one that stops the others
			if !r.matches(f) {
				continue
			}
			if r.Do.Notify {
				out = append(out, f.id)
				break
			}
			if r.Stop {
				break
			}
		}
	}
	return out, rows.Err()
}

// Changed tells the user's open pages that letters were read, moved or deleted elsewhere (the menu's counter).
func (s *Store) Changed(ctx context.Context, userID string) {
	if err := s.wake(ctx, userID, News{}); err != nil && s.Log != nil {
		s.Log("mail: wake", userID, err)
	}
}
