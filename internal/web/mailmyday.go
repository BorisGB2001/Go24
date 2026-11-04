package web

import (
	"bytes"
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/tasks"
)

// myDay is «Мой день» of Outlook beside the mail: the viewer's meetings of a day and their open tasks.
type myDay struct {
	Day, Prev, Next string // YYYY-MM-DD
	Date            time.Time
	Today           bool
	Events          []calendar.Event
	// the open tasks the viewer does (responsible or accomplice), by their deadlines
	Overdue, DueToday, Later, NoDate []tasks.Task
}

// myDayTasks is how many tasks the pane shows at most.
const myDayTasks = 50

// mailMyDay is the pane «Мой день» alone, for the script (static/js/mailday.js): ?day=YYYY-MM-DD, today by default.
func (s *Server) mailMyDay(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if d, err := time.ParseInLocation(time.DateOnly, r.URL.Query().Get("day"), time.Local); err == nil {
		day = d
	}
	d := myDay{Day: day.Format(time.DateOnly), Prev: day.AddDate(0, 0, -1).Format(time.DateOnly),
		Next: day.AddDate(0, 0, 1).Format(time.DateOnly), Date: day, Today: sameDay(day, now)}
	if s.Calendar != nil {
		list, err := s.Calendar.Range(r.Context(), sess.UserID, day, day.AddDate(0, 0, 1))
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		for _, e := range list { // as «Ближайшие события»: one's own not declined, the ones one is invited to
			if e.Kind != "event" || e.Hidden {
				continue
			}
			if e.OwnerID == sess.UserID && e.MyStatus != "declined" || e.MyStatus == "invited" || e.MyStatus == "accepted" || e.MyStatus == "tentative" {
				d.Events = append(d.Events, e)
			}
		}
	}
	if !tasks.IsGuestCtx(r.Context()) {
		list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: sess.UserID, Doing: true, OnlyOpen: true})
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		for i, t := range list {
			if i == myDayTasks {
				break
			}
			switch {
			case t.Deadline == nil:
				d.NoDate = append(d.NoDate, t)
			case t.Deadline.Before(now):
				d.Overdue = append(d.Overdue, t)
			case sameDay(t.Deadline.Local(), now):
				d.DueToday = append(d.DueToday, t)
			default:
				d.Later = append(d.Later, t)
			}
		}
	}
	var buf bytes.Buffer
	if err := s.pages[s.lang(r)]["mail"].ExecuteTemplate(&buf, "mmyday", d); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}
