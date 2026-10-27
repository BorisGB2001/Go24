package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go24/internal/booking"
	"go24/internal/i18n"
	"go24/internal/openlines"
)

// «Онлайн-запись» in a dialogue of an open line: the operator who works with «Онлайн-запись» sends an
// active booking link to the client (openlines.SendBooking); the page of the link opened by it is the dialogue's.

// olBookingLinks are the active booking links the operator may send in a dialogue (none — the section is off or not
// theirs).
func (s *Server) olBookingLinks(ctx context.Context, userID string, admin bool) ([]booking.Link, error) {
	if s.Booking == nil || slices.Contains(s.sectionsOff(ctx), "booking") {
		return nil, nil
	}
	if use, _ := s.bookingMay(ctx, userID, admin); !use {
		return nil, nil
	}
	list, err := s.Booking.Links(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list, func(l booking.Link) bool { return !l.Active }), nil
}

// olSendBooking sends the booking link to the dialogue's client, if the operator may.
func (s *Server) olSendBooking(ctx context.Context, userID string, admin bool, id, linkID string) error {
	links, err := s.olBookingLinks(ctx, userID, admin)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(links, func(l booking.Link) bool { return l.ID == linkID }) {
		return i18n.E("err.ol_booking_off")
	}
	return s.OpenLines.SendBooking(ctx, userID, id, linkID, s.olBookingLink)
}

// olBookingLink is the address of a booking link sent in a dialogue, with the key of the dialogue's booking.
func (s *Server) olBookingLink(token, secret string) string {
	return s.BaseURL + "/bk/" + url.PathEscape(token) + "?ol=" + secret
}

// olBookingClient is the dialogue of the page's key (?ol=, the link sent in it); ok false — none.
func (s *Server) olBookingClient(r *http.Request, l booking.Link) (openlines.BookingClient, bool) {
	key := r.FormValue("ol")
	if key == "" || s.OpenLines == nil {
		return openlines.BookingClient{}, false
	}
	c, err := s.OpenLines.BookingFor(r.Context(), l.ID, key)
	return c, err == nil
}

// olBookingWhat is a booking as a line of the dialogue: the day, the time and the resource.
func (s *Server) olBookingWhat(b booking.Booking, res booking.Resource) string {
	t := b.Start.In(time.Local)
	return s.portalTr(fmt.Sprintf("wd.%d", (int(t.Weekday())+6)%7+1)) + ", " + t.Format("02.01.2006 15:04") + " · " + res.Name
}
