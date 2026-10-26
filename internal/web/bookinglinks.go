package web

import (
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/booking"
)

// ---------- «Онлайн-запись»: links for clients to book themselves ----------

// bookingLinks: the links of the company with their addresses; administrators add and change them (?edit=id, ?edit=new).
func (s *Server) bookingLinks(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	links, err := s.Booking.Links(r.Context())
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	res, err := s.Booking.Resources(r.Context(), "")
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	names := map[string]string{}
	for _, x := range res {
		names[x.ID] = x.Name
	}
	data := map[string]any{"Links": links, "Resources": res, "Names": names, "Admin": sess.Admin, "Base": s.BaseURL,
		"Msg": r.URL.Query().Get("msg")}
	if e := r.URL.Query().Get("edit"); e != "" && sess.Admin {
		ed := booking.Link{DaysAhead: 30, Active: true}
		if e != "new" {
			if ed, err = s.Booking.Link(r.Context(), e); err != nil {
				s.taskErr(w, r, err, "/booking/links")
				return
			}
		}
		users, err := s.Tasks.ListUsers(r.Context())
		if err != nil {
			s.fail(w, "booking", err)
			return
		}
		data["Edit"], data["Users"] = ed, users
	}
	s.render(w, r, "bookinglinks", page{TitleKey: "booking.links", Nav: "booking", Data: data})
}

// bookingLinkSave: a link from its form (administrators).
func (s *Server) bookingLinkSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/booking/links")
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days_ahead"))
	l := booking.Link{ID: r.PathValue("id"), Name: r.FormValue("name"), Description: strings.TrimSpace(r.FormValue("description")),
		Resources: r.Form["resources"], DaysAhead: days, ResponsibleID: r.FormValue("responsible"),
		Active: r.FormValue("active") == "1"}
	back := "/booking/links?edit=" + url.QueryEscape(map[bool]string{true: "new", false: l.ID}[l.ID == ""])
	if _, err := s.Booking.SaveLink(r.Context(), sess.UserID, l); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/booking/links?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) bookingLinkDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Booking.DeleteLink(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/booking/links")
		return
	}
	http.Redirect(w, r, "/booking/links", http.StatusSeeOther)
}

// bookingDay is a day of the public page: the resource works on it or not.
type bookingDay struct {
	Date  time.Time
	Value string
	Works bool
}

// bookingPublic: the page of a link for clients, without signing in — the resource (?r=, when the link has several),
// the day (?date=, the days of the link a week at a time), its free slots and, with a slot chosen (?t=HH:MM), the
// client's form.
func (s *Server) bookingPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	l, err := s.Booking.LinkByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	res, err := s.Booking.LinkResources(r.Context(), l)
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	q := r.URL.Query()
	// what the client typed comes back with a message and goes on with the slots chosen again
	keep := url.Values{}
	for _, k := range []string{"name", "phone", "email", "note"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	data := map[string]any{"Link": l, "Resources": res, "Token": l.Token, "Msg": q.Get("msg"),
		"Name": q.Get("name"), "Phone": q.Get("phone"), "Email": q.Get("email"), "Note": q.Get("note"), "Keep": template.URL(keep.Encode())}
	var cur *booking.Resource
	for i := range res {
		if res[i].ID == q.Get("r") || len(res) == 1 {
			cur = &res[i]
		}
	}
	if cur != nil {
		now := time.Now()
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		last := today.AddDate(0, 0, l.DaysAhead-1)
		day := time.Time{}
		if d, err := time.ParseInLocation("2006-01-02", q.Get("date"), time.Local); err == nil && !d.Before(today) && !d.After(last) {
			day = d
		}
		for d := today; day.IsZero() && !d.After(last); d = d.AddDate(0, 0, 1) { // the first working day
			if cur.WorksOn(d) {
				day = d
			}
		}
		if day.IsZero() {
			day = today
		}
		// the week of the day, within the link's days
		w0 := today.AddDate(0, 0, int(day.Sub(today).Hours()/24)/7*7)
		var days []bookingDay
		for d := w0; d.Before(w0.AddDate(0, 0, 7)) && !d.After(last); d = d.AddDate(0, 0, 1) {
			days = append(days, bookingDay{d, d.Format("2006-01-02"), cur.WorksOn(d)})
		}
		free, err := s.Booking.Free(r.Context(), *cur, day, time.Local)
		if err != nil {
			s.fail(w, "booking", err)
			return
		}
		slot := ""
		for _, f := range free {
			if f.Format("15:04") == q.Get("t") {
				slot = q.Get("t")
			}
		}
		data["Res"], data["Day"], data["DayValue"], data["Days"], data["Free"], data["Slot"] = cur, day, day.Format("2006-01-02"), days, free, slot
		if w0.After(today) {
			data["PrevWeek"] = w0.AddDate(0, 0, -7).Format("2006-01-02")
		}
		if nw := w0.AddDate(0, 0, 7); !nw.After(last) {
			data["NextWeek"] = nw.Format("2006-01-02")
		}
	}
	s.render(w, r, "bookingpub", page{Title: l.Name, Data: data})
}

// bookingPublicBook: the client books the slot; then the client goes to the booking's own page, and the responsible
// one (the link's, else the resource's employee, else who made the link) is told.
func (s *Server) bookingPublicBook(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	l, err := s.Booking.LinkByToken(r.Context(), tok)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	date, at := r.FormValue("date"), r.FormValue("t")
	start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+at, time.Local)
	b := booking.Booking{ResourceID: r.FormValue("r"), Start: start, ClientName: r.FormValue("name"),
		ClientPhone: r.FormValue("phone"), ClientEmail: r.FormValue("email"), Note: r.FormValue("note"), ClientIP: clientIP(r)}
	switch {
	case r.FormValue("website") != "": // the field people do not see: a robot filled it
		err = booking.ErrSlot
	case err != nil:
		err = booking.ErrSlot
	default:
		b, err = s.Booking.BookByLink(r.Context(), l, b, time.Local)
	}
	if err != nil {
		v := url.Values{"r": {b.ResourceID}, "date": {date}, "name": {b.ClientName}, "phone": {b.ClientPhone},
			"email": {b.ClientEmail}, "note": {b.Note}, "msg": {s.bookingErr(r, err)}}
		if !errors.Is(err, booking.ErrSlot) {
			v.Set("t", at)
		}
		http.Redirect(w, r, "/bk/"+tok+"?"+v.Encode(), http.StatusSeeOther)
		return
	}
	s.Log.Info("booking by link", "link", l.ID, "booking", b.ID)
	res, err := s.Booking.Resource(r.Context(), b.ResourceID)
	if err == nil {
		to := l.ResponsibleID
		if to == "" {
			to = res.UserID
		}
		if to == "" {
			to = l.CreatedBy
		}
		s.bookingClientCard(r.Context(), to, b)
		if to != "" {
			body := b.ClientName + " · " + b.Start.Local().Format("02.01.2006 15:04") + " · " + res.Name
			if _, err := s.DB.Exec(r.Context(), `INSERT INTO notifications (user_id, kind, body, url)
				VALUES ($1, 'booking_new', $2, $3)`, to, body, "/booking?date="+b.Start.Local().Format("2006-01-02")); err != nil {
				s.Log.Warn("booking notify", "err", err)
			}
		}
	}
	http.Redirect(w, r, "/bk/b/"+b.ClientToken, http.StatusSeeOther)
}

// bookingErr is the message of an error for the client: a known one in their language, else a common one.
func (s *Server) bookingErr(r *http.Request, err error) string {
	for _, e := range []error{booking.ErrSlot, booking.ErrClient, booking.ErrPhone, booking.ErrTooMany, booking.ErrClosed, booking.ErrNotFound} {
		if errors.Is(err, e) {
			return s.errText(r, e)
		}
	}
	s.Log.Warn("booking by link", "err", err)
	return s.tr(r, "booking.err.public")
}

// bookingClient: the client's page of a booking made by a link.
func (s *Server) bookingClient(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	b, err := s.Booking.ByClientToken(r.Context(), r.PathValue("token"))
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	res, err := s.Booking.Resource(r.Context(), b.ResourceID)
	if err != nil {
		s.fail(w, "booking", err)
		return
	}
	s.render(w, r, "bookingpub", page{TitleKey: "booking.title", Data: map[string]any{"Booking": b, "Res": &res,
		"Open": (b.Status == "unconfirmed" || b.Status == "confirmed") && b.Start.After(time.Now()), "Msg": r.URL.Query().Get("msg")}})
}
