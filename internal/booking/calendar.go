package booking

import (
	"context"
	"time"

	"go24/internal/calendar"
)

// ---------- the employee's calendar: their meetings and absences take the resource's time ----------

// Span is a busy time of a resource outside its bookings.
type Span struct{ Start, End time.Time }

// CalendarBusy: the time between from and to its employee is busy in the calendar — meetings (not «free»; invited
// and not declined too) and absences; none for a resource that is not an employee or does not go by the calendar.
// The employee's bookings are not counted: they are the resource's own.
func (s *Store) CalendarBusy(ctx context.Context, r Resource, from, to time.Time) ([]Span, error) {
	if r.UserID == "" || !r.CalendarBusy {
		return nil, nil
	}
	people, err := calendar.NewStore(s.db).Busy(ctx, r.UserID, []string{r.UserID}, from, to, "")
	if err != nil || len(people) == 0 {
		return nil, err
	}
	var list []Span
	for _, x := range people[0].Slots {
		if x.Kind != calendar.BookingKind && x.End.After(from) && x.Start.Before(to) {
			list = append(list, Span{x.Start, x.End})
		}
	}
	return list, nil
}
