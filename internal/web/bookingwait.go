package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/booking"
)

// ---------- «Онлайн-запись»: the waiting list ----------

// bookingWaitSave puts a client on the waiting list or changes the entry (id): the client, the resource wanted,
// a comment; the CRM cards of an entry stay as they are.
func (s *Server) bookingWaitSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/booking?date=" + url.QueryEscape(r.FormValue("date"))
	x := booking.Wait{ID: r.PathValue("id")}
	if x.ID != "" {
		var err error
		if x, err = s.Booking.Wait(r.Context(), x.ID); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	x.ResourceID, x.ClientName, x.ClientPhone = r.FormValue("resource"), r.FormValue("client_name"), r.FormValue("client_phone")
	x.ClientEmail, x.Note = r.FormValue("client_email"), r.FormValue("note")
	if _, err := s.Booking.SaveWait(r.Context(), sess.UserID, x); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bookingWaitBook: a client of the waiting list dragged onto a free time of a resource (resource, date, time) — a
// booking for a slot of the resource, then its card.
func (s *Server) bookingWaitBook(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	date := r.FormValue("date")
	back := "/booking?date=" + url.QueryEscape(date)
	start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+r.FormValue("time"), time.Local)
	if err != nil {
		s.taskErr(w, r, booking.ErrTime, back)
		return
	}
	b, err := s.Booking.BookWait(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("resource"), start)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.bookingEvents(r.Context(), sess.UserID, booking.Booking{}, b)
	http.Redirect(w, r, back+"&b="+b.ID, http.StatusSeeOther)
}

func (s *Server) bookingWaitDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/booking?date=" + url.QueryEscape(r.FormValue("date"))
	if err := s.Booking.DeleteWait(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bookingToWait: «В лист ожидания» — the client of a booking goes back on the waiting list, the booking is removed.
func (s *Server) bookingToWait(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	b, err := s.Booking.Booking(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/booking")
		return
	}
	back := "/booking?date=" + b.Start.Local().Format("2006-01-02")
	if _, err := s.Booking.ToWait(r.Context(), sess.UserID, b.ID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
