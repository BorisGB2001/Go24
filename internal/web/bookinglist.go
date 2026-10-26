package web

import (
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/booking"
)

// ---------- «Онлайн-запись»: the list of bookings ----------

// bookingFilter: the filter of the list from the query — ?from, ?to (from today a month ahead by default), ?q,
// ?status, ?resource, ?mine=1 (the bookings of the user), ?link=1 (booked by clients by links).
func bookingFilter(r *http.Request, userID string) booking.Filter {
	q := r.URL.Query()
	from, to := bookingPeriod(r)
	if q.Get("from") == "" {
		now := time.Now()
		from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		to = from.AddDate(0, 0, 31)
		if d, err := time.ParseInLocation("2006-01-02", q.Get("to"), time.Local); err == nil && !d.Before(from) {
			to = d.AddDate(0, 0, 1)
		}
	}
	f := booking.Filter{From: from, To: to, Query: q.Get("q"), Status: q.Get("status"), ResourceID: q.Get("resource"),
		ByLink: q.Get("link") == "1"}
	if q.Get("mine") == "1" {
		f.Mine = userID
	}
	return f
}

// bookingListRow is a booking of the list: late — its client is late.
type bookingListRow struct {
	B    booking.Booking
	Late bool
}

// bookingList: «Список записей» — the bookings of a period with the filter and its quick presets («Требуют
// внимания» is what the counter of the menu counts); a click opens the booking on the grid.
func (s *Server) bookingList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f := bookingFilter(r, sess.UserID)
	all, err := s.Booking.Resources(r.Context(), "")
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	now := time.Now()
	list, err := s.Booking.Find(r.Context(), f, now)
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	res := map[string]booking.Resource{}
	for _, x := range all {
		res[x.ID] = x
	}
	rows := make([]bookingListRow, len(list))
	for i, b := range list {
		rows[i] = bookingListRow{b, b.Late(res[b.ResourceID], now)}
	}
	s.render(w, r, "bookinglist", page{TitleKey: "booking.list", Nav: "booking", Data: map[string]any{
		"F": f, "From": f.From.Format("2006-01-02"), "To": f.To.AddDate(0, 0, -1).Format("2006-01-02"),
		"Rows": rows, "All": all, "Statuses": booking.Statuses, "Limit": len(list) >= 1000}})
}
