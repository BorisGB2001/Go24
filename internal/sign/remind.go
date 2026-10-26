package sign

import (
	"context"
	"slices"
	"time"
)

// RemindDays are the choices of «Напоминать подписанту»: never, every day, every 2 or 3 days, once a week.
var RemindDays = []int{0, 1, 2, 3, 7}

// RemindMax: a signer is reminded by itself no more times than this.
const RemindMax = 5

// RemindOf is the choice made, the daily reminder for one not of the choices.
func RemindOf(days int) int {
	if slices.Contains(RemindDays, days) {
		return days
	}
	return 1
}

// SetRemind changes how often the signer of the document is reminded.
func (s *Store) SetRemind(ctx context.Context, docID string, days int) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_docs SET remind_days = $2 WHERE id::text = $1`, docID, RemindOf(days))
	return err
}

// Reminded marks the signer as reminded now (by «Отправить ещё раз» or a reminder of the sending): the next reminder
// counts from it.
func (s *Store) Reminded(ctx context.Context, partyIDs ...string) error {
	_, err := s.db.Exec(ctx, `UPDATE sign_parties SET reminded_at = now() WHERE id::text = ANY($1) AND status = 'ready'`, partyIDs)
	return err
}

// Remind is a signer due a reminder: the document, the signer, and the HR sending's representative and title, if
// the document is of one.
type Remind struct {
	DocID, PartyID          string
	BatchSigner, BatchTitle string
}

// RemindDue claims the signers due a reminder at now: whose turn has lasted since before the remind_days-th day back
// (day — from midnight, today the midnight given), reminded fewer than RemindMax times. Each is marked as it is
// claimed, so none is reminded twice.
func (s *Store) RemindDue(ctx context.Context, now, midnight time.Time) ([]Remind, error) {
	rows, err := s.db.Query(ctx, `UPDATE sign_parties p SET reminded_at = $1, reminders = p.reminders + 1
		FROM sign_docs d
		WHERE d.id = p.doc_id AND d.status = 'signing' AND d.remind_days > 0 AND p.status = 'ready' AND p.reminders < $3
			AND coalesce(p.reminded_at, p.ready_at, d.created_at) < $2::timestamptz - make_interval(days => d.remind_days - 1)
		RETURNING d.id::text, p.id::text,
			coalesce((SELECT b.signer_id::text FROM sign_batches b WHERE b.id = d.batch_id), ''),
			coalesce((SELECT b.title FROM sign_batches b WHERE b.id = d.batch_id), '')`, now, midnight, RemindMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Remind
	for rows.Next() {
		var m Remind
		if err := rows.Scan(&m.DocID, &m.PartyID, &m.BatchSigner, &m.BatchTitle); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
