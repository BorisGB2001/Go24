package web

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/linkpreview"
	"go24/internal/mail"
)

// A calendar connected by its iCal link (see calendar.SyncLink): «Подключить календарь по ссылке» on the page
// «Синхронизация»; go24-worker loads each one every half an hour.

const maxLinkFile = 10 << 20 // the largest calendar file of a link

// linkClient loads calendars of the internet only (tests put their own).
var linkClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 10 * time.Second, Control: linkpreview.PublicOnly}).DialContext,
	TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 20 * time.Second,
}}

// loadLink loads the calendar of the link: its meetings that have not ended a year ago (and the repeated ones).
func loadLink(ctx context.Context, link string) ([]calendar.Imported, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, i18n.E("err.calendar_link")
	}
	req.Header.Set("Accept", "text/calendar, */*")
	res, err := linkClient.Do(req)
	if err != nil {
		return nil, i18n.E("err.calendar_link_load", err.Error())
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, i18n.E("err.calendar_link_load", "HTTP "+strconv.Itoa(res.StatusCode))
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxLinkFile+1))
	if err != nil {
		return nil, i18n.E("err.calendar_link_load", err.Error())
	}
	if len(raw) > maxLinkFile {
		return nil, i18n.E("err.calendar_link_load", fmt.Sprintf("> %d MB", maxLinkFile>>20))
	}
	list := mail.ParseCalendar(string(raw))
	if len(list) == 0 && !bytes.Contains(raw, []byte("BEGIN:VCALENDAR")) { // an empty calendar is a calendar
		return nil, i18n.E("err.calendar_link_not_ics")
	}
	since := time.Now().AddDate(-1, 0, 0)
	var out []calendar.Imported
	for _, in := range list {
		if in.Repeat == "" && in.End.Before(since) || len(out) == maxImportEvents {
			continue
		}
		out = append(out, calendar.Imported{UID: in.UID, Event: eventOfInvite(in)})
	}
	return out, nil
}

// syncLink loads the connected calendar x and makes its meetings those of the link (a failure is kept on x).
func (s *Server) syncLink(ctx context.Context, x calendar.Section) (int, error) {
	list, err := loadLink(ctx, x.URL)
	if err != nil {
		if ferr := s.Calendar.LinkFailed(ctx, x.ID, s.I18n.Err(s.portalLang(), err)); ferr != nil {
			return 0, ferr
		}
		return 0, err
	}
	return s.Calendar.SyncLink(ctx, x, list)
}

// SyncCalendarLinks is go24-worker's round of the connected calendars due.
func (s *Server) SyncCalendarLinks(ctx context.Context, now time.Time) error {
	list, err := s.Calendar.LinksDue(ctx, now)
	if err != nil {
		return err
	}
	for _, x := range list {
		if _, err := s.syncLink(ctx, x); err != nil && ctx.Err() == nil {
			s.Log.Info("calendar link", "calendar", x.ID, "err", err)
		}
	}
	return ctx.Err()
}

// calendarLinkAdd connects a calendar by its link and loads it at once.
func (s *Server) calendarLinkAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Calendar.AddLink(r.Context(), sess.UserID, r.FormValue("name"), r.FormValue("url"))
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	s.calendarLinkLoad(w, r, sess, id)
}

// calendarLinkSync: «Обновить сейчас».
func (s *Server) calendarLinkSync(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.calendarLinkLoad(w, r, sess, r.PathValue("id"))
}

func (s *Server) calendarLinkLoad(w http.ResponseWriter, r *http.Request, sess auth.Session, id string) {
	x, err := s.Calendar.Link(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	msg := ""
	if n, err := s.syncLink(r.Context(), x); err != nil {
		msg = s.tr(r, "calsync.link_failed", x.Name) + ": " + s.errText(r, err)
	} else {
		msg = s.tr(r, "calsync.link_loaded", x.Name, n)
	}
	http.Redirect(w, r, "/calendar/sync?msg="+url.QueryEscape(msg)+"#links", http.StatusSeeOther)
}

// apiCalLinkAdd connects a calendar by its link ({"url", "name"}) and loads it: the calendar as /calendar/sections
// shows it and the meetings loaded.
func (s *Server) apiCalLinkAdd(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		URL  string `json:"url"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id, err := s.Calendar.AddLink(r.Context(), p.UserID, in.Name, in.URL)
	if err != nil {
		return err
	}
	return s.apiCalLinkLoad(w, r, p, id, http.StatusCreated)
}

// apiCalLinkSync: «Обновить сейчас».
func (s *Server) apiCalLinkSync(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiCalLinkLoad(w, r, p, r.PathValue("id"), http.StatusOK)
}

func (s *Server) apiCalLinkLoad(w http.ResponseWriter, r *http.Request, p apikeys.Principal, id string, status int) error {
	x, err := s.Calendar.Link(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	n, serr := s.syncLink(r.Context(), x)
	if x, err = s.Calendar.Link(r.Context(), p.UserID, id); err != nil {
		return err
	}
	writeJSON(w, status, map[string]any{"calendar": apiSectionOf(x), "events": n, "ok": serr == nil})
	return nil
}
