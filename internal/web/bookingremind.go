package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/booking"
)

// ---------- «Онлайн-запись»: letters to the client, the reminder, the lateness, the rating ----------

// BookingDue sends what is due (the worker, every minute): the letters to the clients from the mailbox of the
// booking's sender, the notes of lateness to the employee. Each is marked before it goes, so none goes twice; a letter
// with no mailbox to send it from is dropped.
func (s *Server) BookingDue(ctx context.Context) error {
	list, err := s.Booking.DueAt(ctx, time.Now())
	if err != nil {
		return err
	}
	lang := s.I18n.Match("")
	for _, d := range list {
		ok, err := s.Booking.Claim(ctx, d)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		when := d.B.Start.Local().Format("02.01.2006 15:04")
		if d.Kind == booking.DueLate && d.Notify != "" {
			s.bookingNotify(ctx, d, "booking_late")
		}
		if d.Kind == booking.DueUnconfirmed {
			if d.Notify != "" {
				s.bookingNotify(ctx, d, "booking_unconfirmed")
			}
			continue
		}
		if !d.Mail {
			continue
		}
		subject := s.I18n.T(lang, "booking.mail."+d.Kind+"_subject", d.Resource, when)
		body := s.I18n.T(lang, "booking.mail."+d.Kind+"_body", d.B.ClientName, d.Resource, when,
			s.BaseURL+"/bk/b/"+d.B.ClientToken)
		s.bookingMail(ctx, d, subject, body)
	}
	return nil
}

// bookingMail sends a letter to the client from the first mailbox of the sender.
func (s *Server) bookingMail(ctx context.Context, d booking.Due, subject, body string) {
	if s.Mail == nil || d.Sender == "" {
		return
	}
	boxes, err := s.Mail.Mailboxes(ctx, d.Sender)
	if err != nil || len(boxes) == 0 {
		return
	}
	if err := s.Mail.Send(ctx, d.Sender, boxes[0].ID, d.B.ClientEmail, subject, body); err != nil {
		s.Log.Warn("booking mail", "booking", d.B.ID, "kind", d.Kind, "err", err)
	}
}

// bookingNotify tells the one responsible of the booking (kind: booking_late, booking_unconfirmed, booking_confirmed, booking_cancelled).
func (s *Server) bookingNotify(ctx context.Context, d booking.Due, kind string) {
	body := d.B.ClientName + " · " + d.B.Start.Local().Format("02.01.2006 15:04") + " · " + d.Resource
	if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, $2, $3, $4)`,
		d.Notify, kind, body, "/booking?date="+d.B.Start.Local().Format("2006-01-02")); err != nil {
		s.Log.Warn("booking notify", "err", err)
	}
}

// bookingClientRate: the client rates the visit from its page (stars 1–5, note); the one responsible is told of the
// first rating.
func (s *Server) bookingClientRate(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	stars, _ := strconv.Atoi(r.FormValue("stars"))
	b, first, err := s.Booking.Rate(r.Context(), tok, stars, r.FormValue("note"))
	if err != nil {
		if b.ID == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, bookingURL(r, "/bk/b/"+tok+"?msg="+url.QueryEscape(s.bookingErr(r, err))), http.StatusSeeOther)
		return
	}
	if d, err := s.Booking.DueOf(r.Context(), b.ID); first && err == nil && d.Notify != "" {
		d.B.ClientName = strings.Repeat("★", b.Rating) + " " + d.B.ClientName
		s.bookingNotify(r.Context(), d, "booking_rated")
	}
	http.Redirect(w, r, bookingURL(r, "/bk/b/"+tok), http.StatusSeeOther)
}

// bookingClientStatus: the client confirms or cancels the booking from its page (status=confirmed|cancelled); the one
// responsible is told.
func (s *Server) bookingClientStatus(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	b, changed, err := s.Booking.ClientStatus(r.Context(), tok, r.FormValue("status"))
	if err != nil {
		if b.ID == "" {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, bookingURL(r, "/bk/b/"+tok+"?msg="+url.QueryEscape(s.bookingErr(r, err))), http.StatusSeeOther)
		return
	}
	if d, err := s.Booking.DueOf(r.Context(), b.ID); changed && err == nil && d.Notify != "" {
		s.bookingNotify(r.Context(), d, "booking_"+b.Status)
	}
	if changed {
		old := b
		old.Status = ""
		s.bookingTriggers(r.Context(), old, b)
	}
	http.Redirect(w, r, bookingURL(r, "/bk/b/"+tok), http.StatusSeeOther)
}
