package mail

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// «Напоминания» (nudges), as in Outlook: over the inbox list come up old letters waiting for an answer — one received,
// written to the user personally by a person and asking something, the user has not answered («Получено 4 дн.
// назад. Ответить?»), and one the user sent asking something nobody answered («Отправлено 5 дн. назад. Нет
// ответа»). A letter is nudged from NudgeAfter to NudgeWithin old, MaxNudges at most; «Закрыть» puts its nudge
// away for good, an answer — by itself.

// Nudge limits.
const (
	NudgeAfter  = 3 * 24 * time.Hour
	NudgeWithin = 14 * 24 * time.Hour
	MaxNudges   = 3
)

// Nudge is a letter waiting for an answer.
type Nudge struct {
	ID, FolderID, MailboxID string
	Sent                    bool   // the user's letter nobody answered (else one received the user has not answered)
	Who                     string // the sender, or the first recipient of a letter sent
	Subject                 string
	At                      time.Time
	Days                    int
}

// robotFrom: the senders who wait for no answer.
var robotFrom = regexp.MustCompile(`(?i)^(no-?reply|do-?not-?reply|notifications?|news(letter)?|mailer-daemon|postmaster|bounces?|marketing|promo|digest|alerts?|support|info)([+.-].*)?@`)

// quoteStart: where the quoted letter begins in a reply's text.
var quoteStart = regexp.MustCompile(`(?im)^(>|-{2,}\s*(original message|forwarded message|исходное сообщение|пересылаемое сообщение)|(on|от|в)\s.{0,120}(wrote|пишет|написал\(?а?\)?):\s*$|from:\s|от:\s)`)

// asks: the words of a request.
var asks = []string{"please", "could you", "can you", "would you", "let me know", "kindly", "waiting for your", "awaiting your",
	"пожалуйста", "прошу", "просьба", "подскажите", "сообщите", "ответьте", "дайте знать", "жду ответа", "жду вашего", "не могли бы",
	"можете ли", "согласуйте", "подтвердите"}

// AsksAnswer tells whether a letter's own text (the quoted letter it answers left out) asks a question or for something.
func AsksAnswer(body string) bool {
	if loc := quoteStart.FindStringIndex(body); loc != nil {
		body = body[:loc[0]]
	}
	if strings.Contains(body, "?") {
		return true
	}
	low := strings.ToLower(body)
	for _, w := range asks {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// ownAddrs: the mailbox's address and its aliases, in lower case.
func ownAddrs(email, sendAs string) []string {
	return append([]string{strings.ToLower(email)}, splitAddrs(sendAs)...)
}

// splitAddrs: the addresses of a list «Имя <a@b>, c@d», in lower case.
func splitAddrs(list string) []string {
	var out []string
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if i := strings.LastIndex(p, "<"); i >= 0 {
			p = strings.TrimSuffix(p[i+1:], ">")
		}
		if strings.Contains(p, "@") {
			out = append(out, strings.ToLower(strings.TrimSpace(p)))
		}
	}
	return out
}

// nameIn: the name the list «Имя <a@b>, c@d» gives the address, else the address.
func nameIn(list, addr string) string {
	for _, p := range strings.Split(list, ",") {
		if i := strings.LastIndex(p, "<"); i > 0 && strings.Contains(strings.ToLower(p[i:]), addr) {
			if name := strings.Trim(strings.TrimSpace(p[:i]), `"`); name != "" {
				return name
			}
		}
	}
	return addr
}

func anyIn(list, of []string) bool {
	for _, a := range list {
		for _, b := range of {
			if a == b {
				return true
			}
		}
	}
	return false
}

// Nudges are the letters of the user's mailboxes waiting for an answer, the received first, the latest first. Those
// the AI found needing no answer («Предлагаемые ответы» with none) are not nudged.
func (s *Store) Nudges(ctx context.Context, userID string) ([]Nudge, error) {
	return s.nudgesAt(ctx, userID, time.Now())
}

func (s *Store) nudgesAt(ctx context.Context, userID string, now time.Time) ([]Nudge, error) {
	rows, err := s.db.Query(ctx, `SELECT x.id::text, x.folder_id::text, f.mailbox_id::text, f.role = 'sent', x.from_addr, x.from_name, x.to_addr, x.subject,
			left(x.body, 4000), x.sent_at, m.email, m.send_as
		FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE m.user_id::text = $1 AND f.role IN ('inbox', 'sent') AND NOT x.nudge_off AND x.sent_at BETWEEN $2 AND $3
			AND (f.role = 'sent' OR (x.answered = '' AND NOT x.other AND x.unsubscribe = '' AND (x.snooze_until IS NULL OR x.snooze_until <= now())
				AND NOT EXISTS (SELECT 1 FROM mail_suggestions g WHERE g.message_id = x.id AND cardinality(g.replies) = 0)))
			AND (f.role = 'inbox' OR x.message_id <> '')
			AND NOT EXISTS (SELECT 1 FROM mail_messages y JOIN mail_folders h ON h.id = y.folder_id
				WHERE h.mailbox_id = m.id AND h.role IN ('inbox', 'sent', 'archive', '') AND y.id <> x.id AND y.sent_at > x.sent_at
					AND (f.role = 'sent' OR h.role = 'sent')
					AND y.thread <> '' AND y.thread IN (nullif(x.thread, ''), nullif(x.message_id, '')))
		ORDER BY f.role = 'sent', x.sent_at DESC LIMIT 50`, userID, now.Add(-NudgeWithin), now.Add(-NudgeAfter))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Nudge
	for rows.Next() {
		var n Nudge
		var from, fromName, to, body, email, sendAs string
		if err := rows.Scan(&n.ID, &n.FolderID, &n.MailboxID, &n.Sent, &from, &fromName, &to, &n.Subject, &body, &n.At, &email, &sendAs); err != nil {
			return nil, err
		}
		own := ownAddrs(email, sendAs)
		tos := splitAddrs(to)
		if len(out) == MaxNudges || !AsksAnswer(body) {
			continue
		}
		if n.Sent {
			var others []string
			for _, a := range tos {
				if !anyIn(own, []string{a}) {
					others = append(others, a)
				}
			}
			if len(others) == 0 {
				continue
			}
			n.Who = nameIn(to, others[0])
		} else {
			if !anyIn(tos, own) || anyIn([]string{strings.ToLower(from)}, own) || robotFrom.MatchString(from) {
				continue
			}
			n.Who = fromName
			if n.Who == "" {
				n.Who = from
			}
		}
		n.Days = int(now.Sub(n.At) / (24 * time.Hour))
		out = append(out, n)
	}
	return out, rows.Err()
}

// DismissNudge is «Закрыть» of a nudge: the user's letter is not nudged again.
func (s *Store) DismissNudge(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE mail_messages x SET nudge_off = true FROM mail_folders f JOIN mailboxes m ON m.id = f.mailbox_id
		WHERE f.id = x.folder_id AND x.id::text = $1 AND m.user_id::text = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
