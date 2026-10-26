package mail

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Пересылка», as Outlook's «Параметры → Почта → Пересылка»: a mailbox sends each new letter of its inbox on to the
// addresses given, as it is (a redirect: the sender stays the letter's, answers go to it), and keeps a copy or not —
// then the letter goes to «Удалённые» and the rules do not see it. A letter sent on automatically by another mail
// program or by go24 (Auto-Submitted: auto-forwarded, Resent-From) is not forwarded again: two mailboxes forwarding
// to each other do not make a loop.

// MaxForward is how many addresses a mailbox forwards to.
const MaxForward = 10

// BoxForward is a mailbox's «Пересылка».
type BoxForward struct {
	On   bool
	To   string // addresses, comma-separated
	Keep bool   // «Сохранить копию пересылаемых сообщений»
}

// MailboxForward: the mailbox's «Пересылка» (the user's own only).
func (s *Store) MailboxForward(ctx context.Context, userID, mailboxID string) (BoxForward, error) {
	var b BoxForward
	err := s.db.QueryRow(ctx, `SELECT forward_on, forward_to, forward_keep FROM mailboxes WHERE id::text = $1 AND user_id::text = $2`, mailboxID, userID).
		Scan(&b.On, &b.To, &b.Keep)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}

// SetMailboxForward sets it: the addresses are checked (none of the user's own mailboxes); on without them is an error.
func (s *Store) SetMailboxForward(ctx context.Context, userID, mailboxID string, b BoxForward) error {
	list, err := addresses(b.To)
	if err != nil || len(list) > MaxForward || (b.On && len(list) == 0) {
		return i18n.E("err.mail_forward_to", MaxForward)
	}
	rows, err := s.db.Query(ctx, `SELECT lower(email) FROM mailboxes WHERE user_id::text = $1`, userID)
	if err != nil {
		return err
	}
	own, err := pgxCollectStrings(rows)
	if err != nil {
		return err
	}
	for _, a := range list {
		if oneOf(strings.ToLower(a.Address), own) {
			return i18n.E("err.mail_forward_own", a.Address)
		}
	}
	tag, err := s.db.Exec(ctx, `UPDATE mailboxes SET forward_on = $3, forward_to = $4, forward_keep = $5 WHERE id::text = $1 AND user_id::text = $2`,
		mailboxID, userID, b.On, joinAddrs(list), b.Keep)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// forward sends the letters new in a mailbox's inbox on by its «Пересылка»; the letters left for the rules (all of
// them when a copy is kept, none otherwise: those go to «Удалённые»).
func (s *Store) forward(ctx context.Context, userID, mailboxID string, news []arrived) ([]arrived, error) {
	b, err := s.MailboxForward(ctx, userID, mailboxID)
	if errors.Is(err, ErrNotFound) {
		return news, nil
	}
	if err != nil || !b.On || b.To == "" {
		return news, err
	}
	var errs []error
	var rest []arrived
	var gone []string
	for _, n := range news {
		if n.fwd {
			rest = append(rest, n)
			continue
		}
		if err := s.sendOn(ctx, userID, n.id, "redirect", b.To, "auto-forwarded"); err != nil {
			errs = append(errs, err)
			rest = append(rest, n) // not sent on: the letter stays whatever was chosen
			continue
		}
		if b.Keep {
			rest = append(rest, n)
		} else {
			gone = append(gone, n.id)
		}
	}
	if len(gone) > 0 {
		errs = append(errs, s.Act(ctx, userID, gone, "delete", ""))
	}
	return rest, errors.Join(errs...)
}
