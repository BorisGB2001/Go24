package mail

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Отозвать сообщение» and «Отправить повторно», as Outlook's. Outlook recalls a letter from the people of its own
// organisation; go24 — from those whose mailbox is in go24 (the address of a mailbox or one of its aliases): their
// copy, still unread, is deleted on their server, and they are told. A copy already read stays (as Outlook's), an
// outside address cannot be reached. What came of it for each person is kept and shown on the sender's letter.
// «Удалить непрочитанные копии и заменить новым сообщением» opens the letter again in the editor (mode "resend").

// Recall results of a person.
const (
	RecallDone     = "recalled"
	RecallRead     = "read"
	RecallMissing  = "missing"
	RecallExternal = "external"
	RecallFailed   = "failed"
)

// RecallResult is what came of recalling the letter from one of its people.
type RecallResult struct {
	Addr, Status string
	At           time.Time
}

// Recalls are the results of recalling the user's letter (none — never recalled), by address.
func (s *Store) Recalls(ctx context.Context, userID, messageID string) ([]RecallResult, error) {
	if messageID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT addr, status, at FROM mail_recalls WHERE user_id::text = $1 AND message_id = $2 ORDER BY addr`, userID, messageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[RecallResult])
}

// Recall recalls the user's sent letter from its people (To, Cc, Bcc): the mailboxes of go24 at their addresses are
// fetched first, so a copy just come is found too.
func (s *Store) Recall(ctx context.Context, userID, id string) ([]RecallResult, error) {
	m, err := s.message(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if m.FolderRole != "sent" || m.MessageID == "" {
		return nil, i18n.E("err.bad_form")
	}
	froms, err := s.FromAddresses(ctx, userID)
	if err != nil {
		return nil, err
	}
	var own []string // the addresses the letter may be from: a copy from another is not the user's to delete
	for _, f := range froms {
		if f.MailboxID == m.MailboxID {
			own = append(own, strings.ToLower(f.Email))
		}
	}
	seen := map[string]bool{}
	for _, list := range []string{m.To, m.CC, m.BCC} {
		as, _ := addresses(strings.ReplaceAll(list, ";", ","))
		for _, a := range as {
			addr := strings.ToLower(a.Address)
			if seen[addr] {
				continue
			}
			seen[addr] = true
			status, err := s.recallFrom(ctx, userID, m, addr, own)
			if err != nil {
				if ctx.Err() != nil {
					return nil, err
				}
				if s.Log != nil {
					s.Log("mail: recall "+addr, m.ID, err)
				}
				status = RecallFailed
			}
			if _, err := s.db.Exec(ctx, `INSERT INTO mail_recalls (user_id, message_id, addr, status) VALUES ($1, $2, $3, $4)
				ON CONFLICT (user_id, message_id, addr) DO UPDATE SET status = excluded.status, at = now()
				WHERE mail_recalls.status <> 'recalled'`, userID, m.MessageID, addr, status); err != nil {
				return nil, err
			}
		}
	}
	if len(seen) == 0 {
		return nil, i18n.E("err.bad_form")
	}
	return s.Recalls(ctx, userID, m.MessageID)
}

// recallFrom recalls the letter from one address: its copies in the mailboxes of go24 at it (never in «Отправленные»
// or «Черновики»).
func (s *Store) recallFrom(ctx context.Context, userID string, m Message, addr string, own []string) (string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, user_id::text, email, send_as FROM mailboxes
		WHERE lower(email) = $1 OR position($1 in lower(send_as)) > 0`, addr)
	if err != nil {
		return "", err
	}
	type box struct{ ID, Owner, Email, SendAs string }
	boxes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[box])
	if err != nil {
		return "", err
	}
	status := RecallExternal
	for _, b := range boxes {
		if !strings.EqualFold(b.Email, addr) && !hasAddress(b.SendAs, addr) {
			continue
		}
		if status == RecallExternal {
			status = RecallMissing
		}
		if err := s.Sync(ctx, b.ID); err != nil && s.Log != nil { // what is here already is recalled all the same
			s.Log("mail: recall: fetch", b.ID, err)
		}
		copies, err := s.lettersWhere(ctx, `x.mailbox_id::text = $1 AND x.message_id = $2 AND f.role NOT IN ('sent', 'drafts')
			AND lower(x.from_addr) = ANY($3)`, b.ID, m.MessageID, own)
		if err != nil {
			return "", err
		}
		if len(copies) == 0 {
			continue
		}
		var read bool
		if err := s.db.QueryRow(ctx, `SELECT bool_or(seen) FROM mail_messages WHERE id::text = ANY($1)`, ids(copies)).Scan(&read); err != nil {
			return "", err
		}
		if read {
			if status != RecallDone {
				status = RecallRead
			}
			continue
		}
		if err := s.purge(ctx, copies); err != nil {
			return "", err
		}
		status = RecallDone
		subject := m.Subject
		if subject == "" {
			subject = s.tr("mail.no_subject")
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'mail_recall', left($3, 300), '/mail')`,
			b.Owner, userID, subject); err != nil {
			return "", err
		}
	}
	return status, nil
}
