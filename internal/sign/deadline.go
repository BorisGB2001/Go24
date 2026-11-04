package sign

import (
	"context"
	"strings"
	"time"

	"go24/internal/i18n"
)

// ErrDeadline: the deadline is not a date or has passed.
var ErrDeadline = i18n.E("sign.err.deadline")

// DeadlineMax: a template of sending keeps a deadline no further than this many days from the sending.
const DeadlineMax = 365

// ParseDeadline is «Подписать до» of a form or a request, YYYY-MM-DD (or a time, its day): nil when not given; a day
// before today is ErrDeadline. The day is kept at UTC midnight, as a date column gives it.
func ParseDeadline(v string, today time.Time) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if len(v) > 10 {
		v = v[:10]
	}
	t, err := time.Parse(time.DateOnly, v)
	if err != nil || t.Before(Day(today)) {
		return nil, ErrDeadline
	}
	return &t, nil
}

// Day is the date of a moment in its own zone, at UTC midnight.
func Day(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC) }

// DeadlineIn is the deadline days after today (nil — none, days 0).
func DeadlineIn(today time.Time, days int) *time.Time {
	if days <= 0 {
		return nil
	}
	t := Day(today).AddDate(0, 0, min(days, DeadlineMax))
	return &t
}

// Days is how many days from today the deadline is (0 — none or today).
func Days(today time.Time, deadline *time.Time) int {
	if deadline == nil {
		return 0
	}
	return max(0, int(Day(*deadline).Sub(Day(today)).Hours()/24))
}

// SetDeadline changes the deadline of the document being signed (nil — none).
func (s *Store) SetDeadline(ctx context.Context, docID string, deadline *time.Time) error {
	tag, err := s.db.Exec(ctx, `UPDATE sign_docs SET deadline = $2 WHERE id::text = $1 AND status = 'signing'`, docID, deadline)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrTurn
	}
	return err
}

// Expire closes the documents being signed whose deadline was before today, with «Срок подписания истёк» in their
// history; their ids.
func (s *Store) Expire(ctx context.Context, today time.Time) ([]string, error) {
	rows, err := s.db.Query(ctx, `WITH x AS (UPDATE sign_docs SET status = 'expired', done_at = now()
			WHERE status = 'signing' AND deadline < $1::date RETURNING id),
		e AS (INSERT INTO sign_events (doc_id, kind) SELECT id, '`+EvExpired+`' FROM x)
		SELECT id::text FROM x`, Day(today).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
