package booking

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

var ErrClosed = i18n.E("booking.err.closed")

// The letters of a booking to its client and its notes to the employees.
const (
	DueNew    = "new"    // the booking is made: the letter with its page
	DueRemind = "remind" // RemindHours before its time
	DueLate   = "late"   // LateMinutes after its time, the client has not come
	DueRate   = "rate"   // the visit is over, the client came: asked to rate it («Обратная связь»)
	// «Подтверждение записи»: ConfirmHours before its time the client is asked to confirm; ConfirmAlert hours before
	// it the employee is told the asked client has not
	DueConfirm     = "confirm"
	DueUnconfirmed = "unconfirmed"
)

// Due is a booking a letter or a note is due for: from whose mailbox the letter goes (the resource's employee, else
// who booked, else who made the link) and who is told of the lateness (the link's responsible one, else the same).
type Due struct {
	Kind     string
	B        Booking
	Resource string
	Mail     bool // the resource sends letters and the client gave an e-mail
	Sender   string
	Notify   string
}

// dueSQL: the bookings of the resources with their letters, columns of Due after the booking's.
const dueSQL = `SELECT b.id, b.resource_id, b.starts_at, b.ends_at, b.client_name, b.client_phone, b.client_email,
		b.note, b.status, coalesce(b.created_by::text, ''), coalesce(u.name, ''), coalesce(b.link_id::text, ''),
		coalesce(l.name, ''), b.client_ip, coalesce(b.client_token, ''), r.name, r.notify_mail AND b.client_email <> '',
		coalesce(r.user_id::text, b.created_by::text, l.created_by::text, ''),
		coalesce(l.responsible_id::text, r.user_id::text, b.created_by::text, l.created_by::text, '')
	FROM bookings b JOIN booking_resources r ON r.id = b.resource_id LEFT JOIN users u ON u.id = b.created_by
		LEFT JOIN booking_links l ON l.id = b.link_id`

func scanDue(r pgx.Row) (Due, error) {
	var d Due
	b := &d.B
	err := r.Scan(&b.ID, &b.ResourceID, &b.Start, &b.End, &b.ClientName, &b.ClientPhone, &b.ClientEmail, &b.Note,
		&b.Status, &b.CreatedBy, &b.CreatedByName, &b.LinkID, &b.LinkName, &b.ClientIP, &b.ClientToken,
		&d.Resource, &d.Mail, &d.Sender, &d.Notify)
	return d, err
}

// DueAt: what is due at now — the letters of new bookings yet to come, the reminders within their hours (not of a
// booking made inside them: its first letter says it all), the lateness of today's bookings not marked as come, the
// requests to confirm within their hours (not of a booking made inside them) and the notes on the asked clients who have
// not confirmed, the requests to rate the visits that are over (within a week) of clients who came.
func (s *Store) DueAt(ctx context.Context, now time.Time) ([]Due, error) {
	var out []Due
	for _, q := range []struct{ kind, where string }{
		{DueNew, `b.mailed_at IS NULL AND b.status <> 'cancelled' AND b.starts_at > $1 AND r.notify_mail AND b.client_email <> ''`},
		{DueRemind, `b.reminded_at IS NULL AND b.status IN ('unconfirmed', 'confirmed') AND r.remind_hours > 0
			AND r.notify_mail AND b.client_email <> '' AND b.starts_at > $1
			AND b.starts_at - make_interval(hours => r.remind_hours) <= $1
			AND b.created_at < b.starts_at - make_interval(hours => r.remind_hours)`},
		{DueLate, `b.late_at IS NULL AND b.status IN ('unconfirmed', 'confirmed') AND r.late_minutes > 0
			AND b.starts_at + make_interval(mins => r.late_minutes) <= $1 AND b.starts_at > $1 - interval '1 day'`},
		{DueConfirm, `b.confirm_asked_at IS NULL AND b.status = 'unconfirmed' AND r.confirm_hours > 0
			AND r.notify_mail AND b.client_email <> '' AND b.starts_at > $1
			AND b.starts_at - make_interval(hours => r.confirm_hours) <= $1
			AND b.created_at < b.starts_at - make_interval(hours => r.confirm_hours)`},
		{DueUnconfirmed, `b.unconfirmed_at IS NULL AND b.confirm_asked_at IS NOT NULL AND b.status = 'unconfirmed'
			AND r.confirm_alert_hours > 0 AND b.starts_at > $1
			AND b.starts_at - make_interval(hours => r.confirm_alert_hours) <= $1`},
		{DueRate, `b.feedback_at IS NULL AND b.status = 'came' AND b.rating IS NULL AND r.feedback AND r.notify_mail
			AND b.client_email <> '' AND b.ends_at <= $1 AND b.ends_at > $1 - interval '7 days'`},
	} {
		rows, err := s.db.Query(ctx, dueSQL+` WHERE `+q.where+` ORDER BY b.starts_at LIMIT 100`, now)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Due, error) {
			d, err := scanDue(r)
			d.Kind = q.kind
			return d, err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, list...)
	}
	return out, nil
}

// Claim marks what is due as done before it is sent, so another round does not send it again; false — it was done.
func (s *Store) Claim(ctx context.Context, d Due) (bool, error) {
	col := map[string]string{DueNew: "mailed_at", DueRemind: "reminded_at", DueLate: "late_at", DueRate: "feedback_at",
		DueConfirm: "confirm_asked_at", DueUnconfirmed: "unconfirmed_at"}[d.Kind]
	if col == "" {
		return false, ErrNotFound
	}
	tag, err := s.db.Exec(ctx, `UPDATE bookings SET `+col+` = now() WHERE id = $1 AND `+col+` IS NULL`, d.B.ID)
	return err == nil && tag.RowsAffected() == 1, err
}

// ClientStatus is the client confirming («confirmed») or cancelling («cancelled») the booking from its page: only
// one yet to come and not cancelled; false — it had the status already.
func (s *Store) ClientStatus(ctx context.Context, tok, status string) (Booking, bool, error) {
	b, err := s.ByClientToken(ctx, tok)
	if err != nil {
		return b, false, err
	}
	if status != "confirmed" && status != "cancelled" {
		return b, false, ErrNotFound
	}
	if (b.Status != "unconfirmed" && b.Status != "confirmed") || !b.Start.After(time.Now()) {
		return b, false, ErrClosed
	}
	if b.Status == status {
		return b, false, nil
	}
	b.Status = status
	_, err = s.SaveBooking(ctx, "", b)
	return b, err == nil, err
}

// DueOf: a booking with its sender and who is told of it (Kind is empty).
func (s *Store) DueOf(ctx context.Context, id string) (Due, error) {
	d, err := scanDue(s.db.QueryRow(ctx, dueSQL+` WHERE b.id::text = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return d, err
}

var ErrRating = i18n.E("booking.err.rating")

// Rate is the client rating the visit from the booking's page: stars 1–5 and a comment, only when they came; a rating
// may be changed. first — it is the first one (the employee is told of it).
func (s *Store) Rate(ctx context.Context, tok string, stars int, note string) (b Booking, first bool, err error) {
	if b, err = s.ByClientToken(ctx, tok); err != nil {
		return b, false, err
	}
	if b.Status != "came" {
		return b, false, ErrClosed
	}
	if stars < 1 || stars > 5 {
		return b, false, ErrRating
	}
	note = strings.TrimSpace(note)
	if r := []rune(note); len(r) > 2000 {
		note = string(r[:2000])
	}
	first = b.Rating == 0
	_, err = s.db.Exec(ctx, `UPDATE bookings SET rating = $2, rating_note = $3, rated_at = now() WHERE id = $1`, b.ID, stars, note)
	b.Rating, b.RatingNote = stars, note
	return b, first, err
}
