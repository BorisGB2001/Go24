package calendar

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// People from outside invited to a meeting by mail (iCalendar invitations from the organizer's mailbox): their
// addresses and answers; the meeting's UID, version and mailbox the invitations go from.

// External is a person from outside invited by mail.
type External struct {
	Email, Name string
	Status      string // invited | accepted | tentative | declined
}

// MailInfo is how the meeting goes by mail: its UID ("" — never sent), version, the mailbox.
type MailInfo struct {
	UID       string
	Sequence  int
	MailboxID string
}

// Externals are the meeting's people from outside.
func (s *Store) Externals(ctx context.Context, eventID string) ([]External, error) {
	rows, err := s.db.Query(ctx, `SELECT email, name, status FROM event_externals WHERE event_id::text = $1 ORDER BY email`, eventID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[External])
}

// Mail is how the meeting goes by mail.
func (s *Store) Mail(ctx context.Context, eventID string) (MailInfo, error) {
	var m MailInfo
	err := s.db.QueryRow(ctx, `SELECT coalesce(ical_uid, ''), ical_seq, coalesce(mailbox_id::text, '') FROM events WHERE id::text = $1`, eventID).
		Scan(&m.UID, &m.Sequence, &m.MailboxID)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// SetMail keeps how the organizer's meeting goes by mail.
func (s *Store) SetMail(ctx context.Context, viewer, eventID string, m MailInfo) error {
	var box any
	if m.MailboxID != "" {
		box = m.MailboxID
	}
	tag, err := s.db.Exec(ctx, `UPDATE events SET ical_uid = $3, ical_seq = $4, mailbox_id = $5 WHERE id::text = $1 AND owner_id::text = $2`,
		eventID, viewer, m.UID, m.Sequence, box)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetExternals makes these the organizer's meeting's people from outside (by address: those kept keep their answer);
// it gives the added and the removed ones.
func (s *Store) SetExternals(ctx context.Context, viewer, eventID string, list []External) (added, removed []External, err error) {
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT owner_id::text FROM events WHERE id::text = $1 FOR UPDATE`, eventID).Scan(&owner); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if owner != viewer {
			return ErrForbidden
		}
		rows, err := tx.Query(ctx, `SELECT email, name, status FROM event_externals WHERE event_id::text = $1`, eventID)
		if err != nil {
			return err
		}
		had, err := pgx.CollectRows(rows, pgx.RowToStructByPos[External])
		if err != nil {
			return err
		}
		want := map[string]External{}
		for _, x := range list {
			x.Email = strings.ToLower(strings.TrimSpace(x.Email))
			if x.Email != "" {
				want[x.Email] = x
			}
		}
		for _, h := range had {
			if _, ok := want[h.Email]; !ok {
				removed = append(removed, h)
				if _, err := tx.Exec(ctx, `DELETE FROM event_externals WHERE event_id::text = $1 AND email = $2`, eventID, h.Email); err != nil {
					return err
				}
			}
			delete(want, h.Email) // kept: its answer stays
		}
		for _, x := range list {
			x.Email = strings.ToLower(strings.TrimSpace(x.Email))
			if _, ok := want[x.Email]; !ok {
				continue
			}
			delete(want, x.Email)
			x.Status = "invited"
			if _, err := tx.Exec(ctx, `INSERT INTO event_externals (event_id, email, name) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				eventID, x.Email, strings.TrimSpace(x.Name)); err != nil {
				return err
			}
			added = append(added, x)
		}
		return nil
	})
	return added, removed, err
}

// ResetExternals asks everyone from outside again (the meeting moved).
func (s *Store) ResetExternals(ctx context.Context, eventID string) error {
	_, err := s.db.Exec(ctx, `UPDATE event_externals SET status = 'invited' WHERE event_id::text = $1`, eventID)
	return err
}
