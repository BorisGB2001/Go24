package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Рабочее время»: the working day in the top bar, and the page with one's days and the team's today.

// workdayAction: start, pause, resume, finish (with the report), reopen.
func (s *Server) workdayAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/worktime"
	}
	at, err := s.workPlace(r, r.FormValue("action"), company.ParseGeo(r.FormValue("lat"), r.FormValue("lon"), r.FormValue("acc")))
	if err == nil {
		at.Device = "browser"
		err = s.Company.WorkAction(r.Context(), sess.UserID, r.FormValue("action"), r.FormValue("report"), time.Now(), at)
	}
	if err != nil {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	s.Log.Info("workday", "user_id", sess.UserID, "action", r.FormValue("action"))
	s.tellViolations(r, sess.UserID, r.FormValue("action"))
	s.Telephony.Kick() // the clients' calls may pass them by now (or not)
	if r.FormValue("action") == "finish" { // the day that ends a report period: the report form
		if sched, err := s.Tasks.Schedule(r.Context()); err == nil {
			if due, _ := s.Company.ReportDue(r.Context(), sess.UserID, sched, time.Now()); due != nil {
				back = "/worktime/reports/new"
			}
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// tellViolations tells the managers of the day's violations once it is started or finished (company.NotifyViolations);
// a failure does not stop the action.
func (s *Server) tellViolations(r *http.Request, userID, action string) {
	if action != "start" && action != "finish" {
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err == nil {
		_, err = s.Company.NotifyViolations(r.Context(), userID, sched, time.Now())
	}
	if err != nil {
		s.Log.Warn("workday violations", "user_id", userID, "err", err)
	}
}

// workPlace is where a working-day action is done: the address and its office network, and the position the
// browser gave when «Местоположение» is on. With «Начинать рабочий день только из офисной сети» a day is not started or
// continued from elsewhere.
func (s *Server) workPlace(r *http.Request, action string, geo *company.Geo) (company.Place, error) {
	at := company.Place{IP: audit.ClientIP(r)}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		return at, err
	}
	if sched.Geo {
		at.Geo = geo
	}
	if len(sched.Offices) == 0 {
		return at, nil
	}
	name, in := sched.OfficeOf(at.IP)
	at.Office = &name
	if sched.OfficeOnly && !in && (action == "start" || action == "reopen") {
		return at, i18n.E("err.workday_office", at.IP)
	}
	return at, nil
}

// timeman: my days of a month (?month=2026-10) and today of my people (subordinates; administrators — everyone).
func (s *Server) timeman(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if m, err := time.ParseInLocation("2006-01", r.URL.Query().Get("month"), time.Local); err == nil {
		month = m
	}
	days, err := s.Company.Workdays(r.Context(), sess.UserID, month, month.AddDate(0, 1, 0))
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	var ids, editIDs []string
	if sess.Admin { // administrators see everyone's day
		ids, err = s.Company.ActiveIDs(r.Context())
		editIDs = ids
	} else { // the rest — by «Права доступа» of working time
		ids, err = s.worktimeIDs(r.Context(), sess.UserID, "view")
		if err == nil {
			editIDs, err = s.worktimeIDs(r.Context(), sess.UserID, "edit")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return id == sess.UserID })
	team, err := s.Company.TeamDay(r.Context(), ids, now)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	// late by each person's schedule: my days of the month and my people's today
	normFrom, normTo := month, month.AddDate(0, 1, 0)
	if today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local); today.Before(normFrom) {
		normFrom = today
	} else if !today.Before(normTo) {
		normTo = today.AddDate(0, 0, 1)
	}
	norms, err := s.Company.Norms(r.Context(), append([]string{sess.UserID}, ids...), sched, normFrom, normTo)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	late := map[string]bool{}
	for _, d := range append(slices.Clone(days), team...) {
		uid := d.UserID
		if uid == "" {
			uid = sess.UserID
		}
		late[uid+"|"+d.Day.Format(time.DateOnly)] = slices.Contains(norms[uid].Violations(d, now), "late")
	}
	start := sched.LateAt()
	if p := norms[sess.UserID].Plan(now); p.Work && p.Start != "" {
		start = p.Start
	}
	total := 0
	for _, d := range days {
		total += d.Worked(now)
	}
	pending, err := s.Company.PendingChanges(r.Context(), editIDs, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	pending = slices.DeleteFunc(pending, func(c company.WorkdayChange) bool { return c.UserID == sess.UserID && !sess.Admin })
	forgotten, err := s.Company.ForgottenDay(r.Context(), sess.UserID, now)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	terminals, err := s.Company.TerminalsOn(r.Context()) // «Терминал учёта времени»: one's PIN for it
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	hasPIN, err := s.Company.HasWorkPIN(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "timeman", page{TitleKey: "workday.page", Nav: "timeman", Data: map[string]any{
		"Terminals": terminals, "HasPIN": hasPIN,
		"Days": days, "Team": team, "TeamSize": len(ids), "Start": start, "Late": late, "Pending": pending, "Forgotten": forgotten,
		"Me": sess.UserID, "Now": now, "Total": total,
		"Month": month, "Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
	}})
}

// DayEvent is a meeting of today in the working day window, as «События» of the portal working day: Now — it goes
// on, Past — it is over.
type DayEvent struct {
	calendar.Event
	Now, Past bool
}

// dayEvents: the user's meetings of the day of now (declined ones and the deadlines of tasks are not among them — tasks
// are in «План на день»).
func (s *Server) dayEvents(r *http.Request, userID string, now time.Time) []DayEvent {
	if s.Calendar == nil {
		return nil
	}
	y, m, d := now.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	list, err := s.Calendar.Range(r.Context(), userID, day, day.AddDate(0, 0, 1))
	if err != nil {
		return nil
	}
	var out []DayEvent
	for _, e := range list {
		if e.Kind != "event" || e.MyStatus == "declined" {
			continue
		}
		out = append(out, DayEvent{Event: e, Now: !e.AllDay && !now.Before(e.Start) && now.Before(e.End), Past: !e.AllDay && !now.Before(e.End)})
	}
	slices.SortStableFunc(out, func(a, b DayEvent) int { // whole days first, then by the start
		if a.AllDay != b.AllDay {
			if a.AllDay {
				return -1
			}
			return 1
		}
		return a.Start.Compare(b.Start)
	})
	return out
}

// dayEventAdd: «Добавить событие» of the working day window — a meeting of today in the user's main calendar from
// the time of start to that of end (none — an hour), or the whole day when no time is given.
func (s *Server) dayEventAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/worktime"
	}
	var err error = i18n.E("err.bad_form")
	if s.Calendar != nil {
		err = nil
		now := time.Now()
		y, m, d := now.Date()
		e := calendar.Event{Title: r.FormValue("title"), Start: time.Date(y, m, d, 0, 0, 0, 0, time.Local)}
		e.End, e.AllDay = e.Start, true
		if at, perr := time.Parse("15:04", r.FormValue("start")); perr == nil {
			e.Start, e.AllDay = e.Start.Add(time.Duration(at.Hour())*time.Hour+time.Duration(at.Minute())*time.Minute), false
			e.End = e.Start.Add(time.Hour)
			if to, perr := time.Parse("15:04", r.FormValue("end")); perr == nil {
				e.End = time.Date(y, m, d, to.Hour(), to.Minute(), 0, 0, time.Local)
			}
		} else if r.FormValue("start") != "" {
			err = i18n.E("err.bad_form")
		}
		var id string
		if err == nil {
			id, err = s.Calendar.Save(r.Context(), sess.UserID, e, nil)
		}
		if err == nil {
			s.Log.Info("day event", "user_id", sess.UserID, "event_id", id)
		}
	}
	if err != nil {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		back += sep + "msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// worktimeIDs are the ids of the people whose working time the user may see («view») or change («edit»).
func (s *Server) worktimeIDs(ctx context.Context, userID, action string) ([]string, error) {
	people, err := s.Company.WorktimePeople(ctx, userID, false, action)
	ids := make([]string, 0, len(people))
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	return ids, err
}
