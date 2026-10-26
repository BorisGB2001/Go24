package booking

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// Filter of «Список записей»: the bookings that touch [From, To) and match the rest (empty — any).
type Filter struct {
	From, To   time.Time
	Query      string // a part of the client's name, phone or e-mail
	Status     string // one of Statuses, "late" — the client is late (see Late), "attention" — see attentionSQL
	ResourceID string // the booking takes the resource
	Mine       string // the employee's bookings (see mineSQL)
	ByLink     bool   // booked by the client by a link
}

// mineSQL: the bookings of employee $1 — those they made, of the resource they are, of a link they answer for (a link
// without one — the resource's employee, else who made the link, as the notices go).
const mineSQL = `(b.created_by = $1::uuid OR r.user_id = $1::uuid OR l.responsible_id = $1::uuid
	OR (l.id IS NOT NULL AND l.responsible_id IS NULL AND r.user_id IS NULL AND l.created_by = $1::uuid))`

// lateSQL: the client of the booking is late at $2 — it is not confirmed or confirmed only, and the resource's
// LateMinutes have passed since its start.
const lateSQL = `(b.status IN ('unconfirmed', 'confirmed') AND r.late_minutes > 0
	AND b.starts_at + r.late_minutes * interval '1 minute' < $2)`

// attentionSQL: the booking needs the attention of an employee at $2, as the counter counts — it is not
// confirmed and yet to come, or its client is late.
const attentionSQL = `((b.status = 'unconfirmed' AND b.starts_at >= $2) OR ` + lateSQL + `)`

// Late: the client of b on resource r has not come by now.
func (b Booking) Late(r Resource, now time.Time) bool {
	return r.LateMinutes > 0 && (b.Status == "unconfirmed" || b.Status == "confirmed") &&
		now.After(b.Start.Add(time.Duration(r.LateMinutes)*time.Minute))
}

// Find: the bookings by f, by time, at most 1000.
func (s *Store) Find(ctx context.Context, f Filter, now time.Time) ([]Booking, error) {
	where := []string{"b.starts_at < $4 AND b.ends_at > $3"}
	args := []any{f.Mine, now, f.From, f.To}
	if f.Mine != "" {
		where = append(where, mineSQL)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q)+"%")
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(b.client_name ILIKE "+n+" OR b.client_phone ILIKE "+n+" OR b.client_email ILIKE "+n+
			" OR regexp_replace(b.client_phone, '\\D', '', 'g') LIKE '%' || nullif(regexp_replace("+n+", '\\D', '', 'g'), '') || '%')")
	}
	switch {
	case f.Status == "late":
		where = append(where, lateSQL)
	case f.Status == "attention":
		where = append(where, attentionSQL)
	case f.Status != "":
		args = append(args, f.Status)
		where = append(where, "b.status = $"+strconv.Itoa(len(args)))
	}
	if f.ResourceID != "" {
		args = append(args, f.ResourceID)
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(b.resource_id::text = "+n+
			" OR EXISTS (SELECT 1 FROM booking_extra_resources e WHERE e.booking_id = b.id AND e.resource_id::text = "+n+"))")
	}
	if f.ByLink {
		where = append(where, "b.link_id IS NOT NULL")
	}
	// $1 and $2 are always passed: typed here, so that a query not using them is still valid
	return s.bookings(ctx, bookingSQL+` WHERE $1::text IS NOT NULL AND $2::timestamptz IS NOT NULL AND `+
		strings.Join(where, " AND ")+` ORDER BY b.starts_at, b.created_at LIMIT 1000`, args...)
}

// Attention: the counter of «Онлайн-запись» in the menu — the employee's bookings needing their attention:
// not confirmed ones yet to come and those of today whose client is late.
func (s *Store) Attention(ctx context.Context, userID string, now time.Time) (int, error) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM bookings b JOIN booking_resources r ON r.id = b.resource_id
		LEFT JOIN booking_links l ON l.id = b.link_id
		WHERE `+mineSQL+` AND b.starts_at >= $3 AND `+attentionSQL,
		userID, now, day).Scan(&n)
	return n, err
}
