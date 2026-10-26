package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
)

// ---------- «Поделиться слотами» of the calendar: one's free time for anyone to book a meeting ----------

// slotMinutes are the times of the rules' selects, every half an hour.
func slotMinutes() []int {
	var out []int
	for m := 0; m <= 24*60; m += 30 {
		out = append(out, m)
	}
	return out
}

// slotLang is the language of what go24 writes into the organizer's calendar: the portal's.
func (s *Server) slotLang() string {
	if s.Portal != nil {
		if l := s.Portal.Current().Lang; s.I18n.Supported(l) {
			return l
		}
	}
	return s.I18n.Match("")
}

// calendarSlots: the viewer's links, the personal one first (?edit=id — its form, ?edit=new — a new joint one).
func (s *Server) calendarSlots(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	links, err := s.Calendar.SlotLinks(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "calendar slots", err)
		return
	}
	data := map[string]any{"Links": links, "Base": s.BaseURL, "Msg": r.URL.Query().Get("msg")}
	if e := r.URL.Query().Get("edit"); e != "" {
		var ed calendar.SlotLink
		if e == "new" {
			x, err := s.Calendar.Settings(r.Context(), sess.UserID)
			if err != nil {
				s.fail(w, "calendar slots", err)
				return
			}
			ed = calendar.SlotLink{Length: 60, Rules: calendar.DefaultSlotRules(x), DaysAhead: 30, Active: true}
		} else if ed, err = s.Calendar.SlotLink(r.Context(), sess.UserID, e); err != nil {
			s.taskErr(w, r, err, "/calendar/slots")
			return
		}
		users, err := s.Tasks.ListUsers(r.Context())
		if err != nil {
			s.fail(w, "calendar slots", err)
			return
		}
		// the rules and two empty rows to add more
		rules := append(append([]calendar.SlotRule{}, ed.Rules...), calendar.SlotRule{From: 9 * 60, To: 18 * 60}, calendar.SlotRule{From: 9 * 60, To: 18 * 60})
		data["Edit"], data["New"], data["Rules"], data["Users"] = ed, e == "new", rules, users
		data["Lengths"], data["Days"], data["Minutes"] = calendar.SlotLengths, []int{1, 2, 3, 4, 5, 6, 7}, slotMinutes()
	}
	s.render(w, r, "calslots", page{TitleKey: "calslots.title", Nav: "calendar", Data: data})
}

// calendarSlotSave: a link from its form — a new joint one (no id) or a change.
func (s *Server) calendarSlotSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/calendar/slots?edit=" + url.QueryEscape(map[bool]string{true: "new", false: id}[id == ""])
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	num := func(k string) int {
		n, err := strconv.Atoi(r.FormValue(k))
		if err != nil {
			return -1
		}
		return n
	}
	l := calendar.SlotLink{ID: id, Length: num("length"), DaysAhead: num("days_ahead"), Active: r.FormValue("active") == "on",
		Members: r.Form["members"]}
	for i := 0; r.Form.Has(fmt.Sprintf("from_%d", i)); i++ {
		rule := calendar.SlotRule{From: num(fmt.Sprintf("from_%d", i)), To: num(fmt.Sprintf("to_%d", i))}
		for _, v := range r.Form[fmt.Sprintf("days_%d", i)] {
			n, err := strconv.Atoi(v)
			if err != nil {
				n = 0
			}
			rule.Days = append(rule.Days, n)
		}
		l.Rules = append(l.Rules, rule)
	}
	if _, err := s.Calendar.SaveSlotLink(r.Context(), sess.UserID, l); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/calendar/slots?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// calendarSlotToggle switches a link on or off.
func (s *Server) calendarSlotToggle(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	l, err := s.Calendar.SlotLink(r.Context(), sess.UserID, r.PathValue("id"))
	if err == nil {
		l.Active = !l.Active
		_, err = s.Calendar.SaveSlotLink(r.Context(), sess.UserID, l)
	}
	if err != nil {
		s.taskErr(w, r, err, "/calendar/slots")
		return
	}
	http.Redirect(w, r, "/calendar/slots", http.StatusSeeOther)
}

func (s *Server) calendarSlotDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Calendar.DeleteSlotLink(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/calendar/slots")
		return
	}
	http.Redirect(w, r, "/calendar/slots", http.StatusSeeOther)
}

// slotGone is the page of a link or meeting that is not there (or switched off).
func (s *Server) slotGone(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
}

// slotPublic: the page of a link for guests, without signing in — the days of the link a week at a time (?date=),
// the free times of the day and, with one chosen (?t=HH:MM), the guest's form.
func (s *Server) slotPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	l, err := s.Calendar.SlotLinkByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.slotGone(w, r)
		return
	}
	q := r.URL.Query()
	now := time.Now()
	today, last := l.SlotDays(now)
	day := time.Time{}
	if d, err := time.ParseInLocation("2006-01-02", q.Get("date"), time.Local); err == nil && !d.Before(today) && !d.After(last) {
		day = d
	}
	// the first day with a free time, else the first the link offers
	for d := today; day.IsZero() && !d.After(last) && d.Before(today.AddDate(0, 0, 14)); d = d.AddDate(0, 0, 1) {
		if free, err := s.Calendar.SlotFree(r.Context(), l, d, now); err == nil && len(free) > 0 {
			day = d
		}
	}
	for d := today; day.IsZero() && !d.After(last); d = d.AddDate(0, 0, 1) {
		if l.Offers(d) {
			day = d
		}
	}
	if day.IsZero() {
		day = today
	}
	w0 := today.AddDate(0, 0, int(day.Sub(today).Hours()/24)/7*7)
	var days []bookingDay
	for d := w0; d.Before(w0.AddDate(0, 0, 7)) && !d.After(last); d = d.AddDate(0, 0, 1) {
		days = append(days, bookingDay{d, d.Format("2006-01-02"), l.Offers(d)})
	}
	free, err := s.Calendar.SlotFree(r.Context(), l, day, now)
	if err != nil {
		s.fail(w, "calendar slots", err)
		return
	}
	slot := ""
	for _, f := range free {
		if f.Format("15:04") == q.Get("t") {
			slot = q.Get("t")
		}
	}
	keep := url.Values{}
	for _, k := range []string{"name", "contact", "note"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	data := map[string]any{"Link": l, "Token": l.Token, "Msg": q.Get("msg"), "Day": day, "DayValue": day.Format("2006-01-02"),
		"Days": days, "Free": free, "Slot": slot, "Name": q.Get("name"), "Contact": q.Get("contact"), "Note": q.Get("note"),
		"Keep": template.URL(keep.Encode())}
	if w0.After(today) {
		data["PrevWeek"] = w0.AddDate(0, 0, -7).Format("2006-01-02")
	}
	if nw := w0.AddDate(0, 0, 7); !nw.After(last) {
		data["NextWeek"] = nw.Format("2006-01-02")
	}
	s.render(w, r, "calslotpub", page{TitleKey: "calslots.pub_title", Data: data})
}

// slotPublicBook: the guest takes the time; then the guest goes to the meeting's own page.
func (s *Server) slotPublicBook(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	l, err := s.Calendar.SlotLinkByToken(r.Context(), tok)
	if err != nil {
		s.slotGone(w, r)
		return
	}
	date, at := r.FormValue("date"), r.FormValue("t")
	b := calendar.SlotBooking{GuestName: r.FormValue("name"), GuestContact: r.FormValue("contact"), Note: r.FormValue("note"), ClientIP: clientIP(r)}
	start, err := time.ParseInLocation("2006-01-02 15:04", date+" "+at, time.Local)
	var m calendar.SlotMeeting
	switch {
	case r.FormValue("website") != "": // the field people do not see: a robot filled it
		err = calendar.ErrSlotTaken
	case err != nil:
		err = calendar.ErrSlotTaken
	default:
		lang := s.slotLang()
		b.Start = start
		b.Title = s.I18n.T(lang, "calslots.event_title", b.GuestName)
		b.Description = s.I18n.T(lang, "calslots.event_guest", b.GuestName, b.GuestContact)
		if n := b.Note; n != "" {
			b.Description += "\n\n" + n
		}
		m, err = s.Calendar.BookSlot(r.Context(), l, b, time.Now())
	}
	if err != nil {
		v := url.Values{"date": {date}, "name": {b.GuestName}, "contact": {b.GuestContact}, "note": {b.Note}, "msg": {s.slotErr(r, err)}}
		if !errors.Is(err, calendar.ErrSlotTaken) {
			v.Set("t", at)
		}
		http.Redirect(w, r, "/cs/"+tok+"?"+v.Encode(), http.StatusSeeOther)
		return
	}
	s.Log.Info("calendar slot booked", "link", l.ID, "event", m.EventID)
	http.Redirect(w, r, "/cs/m/"+m.Token, http.StatusSeeOther)
}

// slotErr is the message of an error for the guest: a known one in their language, else a common one.
func (s *Server) slotErr(r *http.Request, err error) string {
	for _, e := range []error{calendar.ErrSlotTaken, calendar.ErrSlotGuest, calendar.ErrSlotTooMany} {
		if errors.Is(err, e) {
			return s.errText(r, e)
		}
	}
	s.Log.Warn("calendar slot", "err", err)
	return s.tr(r, "booking.err.public")
}

// slotJoinBefore: how long before the start the guest may join the video meeting.
const slotJoinBefore = 15 * time.Minute

// slotMeeting: the guest's page of the meeting — when, with whom, the calendar file, the video meeting near its time,
// cancelling it before it starts.
func (s *Server) slotMeeting(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	m, err := s.Calendar.SlotMeetingByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.slotGone(w, r)
		return
	}
	now := time.Now()
	s.render(w, r, "calslotpub", page{TitleKey: "calslots.pub_title", Data: map[string]any{"Meeting": m, "Msg": r.URL.Query().Get("msg"),
		"Open": !m.Cancelled && m.Start.After(now),
		"Join": !m.Cancelled && m.Video && s.CallGuests != nil && now.After(m.Start.Add(-slotJoinBefore)) && now.Before(m.End)}})
}

func (s *Server) slotMeetingCancel(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	if _, err := s.Calendar.CancelSlotMeeting(r.Context(), tok, time.Now()); err != nil {
		if errors.Is(err, calendar.ErrNotFound) {
			s.slotGone(w, r)
			return
		}
		if !errors.Is(err, calendar.ErrForbidden) {
			s.Log.Warn("calendar slot cancel", "err", err)
		}
		http.Redirect(w, r, "/cs/m/"+tok+"?msg="+url.QueryEscape(s.tr(r, "calslots.err.cancel")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/cs/m/"+tok, http.StatusSeeOther)
}

// slotMeetingICS: the meeting as a calendar file for the guest's own calendar.
func (s *Server) slotMeetingICS(w http.ResponseWriter, r *http.Request) {
	m, err := s.Calendar.SlotMeetingByToken(r.Context(), r.PathValue("token"))
	if err != nil || m.Cancelled {
		s.slotGone(w, r)
		return
	}
	writeICSFile(w, calendar.Event{ID: m.EventID, Title: m.Title, Start: m.Start, End: m.End,
		Where: s.BaseURL + "/cs/m/" + m.Token, Accessibility: "busy"})
}

// slotMeetingCall: the guest joins the video meeting by a guest link of its call, made by the organizer's name
// (one link per meeting, made when first needed).
func (s *Server) slotMeetingCall(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	m, err := s.Calendar.SlotMeetingByToken(r.Context(), tok)
	if err != nil {
		s.slotGone(w, r)
		return
	}
	now := time.Now()
	if m.Cancelled || !m.Video || s.CallGuests == nil || now.Before(m.Start.Add(-slotJoinBefore)) || !now.Before(m.End) {
		http.Redirect(w, r, "/cs/m/"+tok+"?msg="+url.QueryEscape(s.tr(r, "calslots.err.call")), http.StatusSeeOther)
		return
	}
	room := "event-" + m.EventID
	links, err := s.CallGuests.Links(r.Context(), room)
	if err != nil {
		s.fail(w, "calendar slot call", err)
		return
	}
	link := ""
	for _, l := range links {
		if l.CreatedBy == m.OwnerID && !l.Password && !l.Lobby {
			link = l.Token
		}
	}
	if link == "" {
		l, err := s.CallGuests.CreateLink(r.Context(), m.OwnerID, room, m.Title, "event="+m.EventID, "", false, 24*time.Hour)
		if err != nil {
			s.fail(w, "calendar slot call", err)
			return
		}
		link = l.Token
	}
	http.Redirect(w, r, s.guestLinkURL(link), http.StatusSeeOther)
}
