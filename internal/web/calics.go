package web

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- a meeting as a calendar file (.ics) downloads one, and «Импорт» of a file ----------

// maxImportICS: the largest calendar file imported; maxImportEvents: the most meetings of one file.
const (
	maxImportICS    = 4 << 20
	maxImportEvents = 2000
)

// eventICS downloads the meeting as a .ics file (one's own calendar program opens it).
func (s *Server) eventICS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	e, err := s.Calendar.Get(r.Context(), sess.UserID, r.PathValue("id"))
	if err == nil && e.Hidden { // someone else's private meeting: nothing to take away
		err = calendar.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/calendar")
		return
	}
	writeICSFile(w, e)
}

func writeICSFile(w http.ResponseWriter, e calendar.Event) {
	name := strings.NewReplacer("/", " ", `\`, " ").Replace(strings.TrimSpace(e.Title))
	if name == "" {
		name = "event"
	}
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(name+".ics")))
	_, _ = io.WriteString(w, calendar.EventICS(e, time.Now()))
}

// importedOf: the meetings of a calendar file, as go24 keeps them.
func importedOf(text string) ([]calendar.Imported, error) {
	list := mail.ParseCalendar(text)
	if len(list) == 0 {
		return nil, i18n.E("err.ics_empty")
	}
	if len(list) > maxImportEvents {
		return nil, i18n.E("err.ics_too_many", maxImportEvents)
	}
	out := make([]calendar.Imported, 0, len(list))
	for _, in := range list {
		out = append(out, calendar.Imported{UID: in.UID, Event: eventOfInvite(in)})
	}
	return out, nil
}

// eventOfInvite is a meeting of a calendar file as go24 keeps it.
func eventOfInvite(in mail.Invite) calendar.Event {
	e := calendar.Event{Title: in.Summary, Description: in.Description, Where: in.Location, Start: in.Start, End: in.End,
		AllDay: in.AllDay, Repeat: in.Repeat, Every: in.Every, Until: in.Until, Skip: in.Skip, Reminds: in.Alarms,
		Private: in.Private, Accessibility: "busy"}
	if strings.TrimSpace(e.Title) == "" {
		e.Title = "—"
	}
	if in.Free {
		e.Accessibility = "free"
	}
	if in.Join != "" && !strings.Contains(e.Where+e.Description, in.Join) { // the link of an online meeting stays
		e.Description = strings.TrimSpace(e.Description + "\n" + in.Join)
	}
	return e
}

// calendarImport imports a .ics file into one's calendar: «Импорт» on the page of synchronization.
func (s *Server) calendarImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportICS+1<<20)
	if err := r.ParseMultipartForm(maxImportICS); err != nil {
		s.taskErr(w, r, i18n.E("err.file_too_big"), "/calendar/sync")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		s.taskErr(w, r, i18n.E("err.ics_empty"), "/calendar/sync")
		return
	}
	defer f.Close()
	text, err := io.ReadAll(io.LimitReader(f, maxImportICS))
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	list, err := importedOf(string(text))
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	added, changed, err := s.Calendar.Import(r.Context(), sess.UserID, r.FormValue("calendar"), list)
	if err != nil {
		s.taskErr(w, r, err, "/calendar/sync")
		return
	}
	first := list[0].Event.Start
	for _, im := range list {
		if im.Event.Start.Before(first) {
			first = im.Event.Start
		}
	}
	msg := s.tr(r, "calsync.imported", strconv.Itoa(added), strconv.Itoa(changed))
	http.Redirect(w, r, "/calendar?month="+first.Format("2006-01")+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// apiCalEventICS: GET /api/v1/calendar/events/{id}/ics — the meeting as a calendar file.
func (s *Server) apiCalEventICS(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	e, err := s.Calendar.Get(r.Context(), p.UserID, r.PathValue("id"))
	if err == nil && e.Hidden {
		err = calendar.ErrNotFound
	}
	if err != nil {
		return err
	}
	writeICSFile(w, e)
	return nil
}

// apiCalImport: POST /api/v1/calendar/import — the body is a calendar file (text/calendar), ?calendar= one's own
// calendar ("" — the main one); answers how many meetings were added and changed.
func (s *Server) apiCalImport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	text, err := io.ReadAll(io.LimitReader(r.Body, maxImportICS+1))
	if err != nil {
		return err
	}
	if len(text) > maxImportICS {
		return i18n.E("err.file_too_big")
	}
	list, err := importedOf(string(text))
	if err != nil {
		return err
	}
	added, changed, err := s.Calendar.Import(r.Context(), p.UserID, r.URL.Query().Get("calendar"), list)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added, "changed": changed})
	return nil
}
