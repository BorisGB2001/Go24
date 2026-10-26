package web

import (
	"fmt"
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/booking"
)

// ---------- «Онлайн-запись»: the statistics ----------

// bookingPeriod: the days ?from–?to (both in, as dates), the current month by default; at most a year.
func bookingPeriod(r *http.Request) (from, to time.Time) {
	now := time.Now()
	from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	to = from.AddDate(0, 1, 0)
	if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("from"), time.Local); err == nil {
		from, to = d, d.AddDate(0, 1, 0)
	}
	if d, err := time.ParseInLocation("2006-01-02", r.URL.Query().Get("to"), time.Local); err == nil && !d.Before(from) {
		to = d.AddDate(0, 0, 1)
	}
	if to.After(from.AddDate(1, 0, 0)) {
		to = from.AddDate(1, 0, 0)
	}
	return from, to
}

// hours: minutes as «12:30».
func hours(m int) string { return fmt.Sprintf("%d:%02d", m/60, m%60) }

// bookingStats: «Статистика» — the load of the resources for a period (?from, ?to, ?type): their working hours, the
// booked ones, the bookings by status and the share of no-shows.
func (s *Server) bookingStats(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	from, to := bookingPeriod(r)
	typ := r.URL.Query().Get("type")
	types, err := s.Booking.Types(r.Context())
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	list, total, err := s.Booking.Stats(r.Context(), typ, from, to, time.Local)
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	s.render(w, r, "bookingstats", page{TitleKey: "booking.stats", Nav: "booking", Data: map[string]any{
		"From": from.Format("2006-01-02"), "To": to.AddDate(0, 0, -1).Format("2006-01-02"), "Type": typ, "Types": types,
		"Rows": list, "Total": total,
		"Statuses": booking.Statuses, "Hours": hours}})
}
