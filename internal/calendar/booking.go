package calendar

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- «Онлайн-запись» in the calendar: the bookings of the resources that are the employee ----------

// BookingKind: the Kind of a booking in the calendar (Event) and in the planner (Slot).
const BookingKind = "booking"

// bookingsOf: the bookings (not cancelled) between from and to of the resources that are these people — as the
// resource of a booking or one of its others; each once for a person, with the name of that person's resource.
func (s *Store) bookingsOf(ctx context.Context, people []string, from, to time.Time) ([]Event, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT ON (b.id, r.user_id) b.id::text, r.user_id::text, b.client_name,
			r.name, b.status, b.starts_at, b.ends_at
		FROM bookings b JOIN booking_resources r ON r.id = b.resource_id
			OR r.id IN (SELECT e.resource_id FROM booking_extra_resources e WHERE e.booking_id = b.id)
		WHERE b.status <> 'cancelled' AND r.user_id::text = ANY($1::text[]) AND b.starts_at < $3 AND b.ends_at > $2
		ORDER BY b.id, r.user_id, r.id <> b.resource_id`, people, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		e := Event{Kind: BookingKind}
		return e, row.Scan(&e.ID, &e.OwnerID, &e.Title, &e.Where, &e.Status, &e.Start, &e.End)
	})
}
