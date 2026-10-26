package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
)

// «Рабочее время»: the working day in the top bar, and the page with one's days and the team's today.

// workdayAction: start, pause, resume, finish (with the report), reopen.
func (s *Server) workdayAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/worktime"
	}
	if err := s.Company.WorkAction(r.Context(), sess.UserID, r.FormValue("action"), r.FormValue("report"), time.Now()); err != nil {
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	s.Log.Info("workday", "user_id", sess.UserID, "action", r.FormValue("action"))
	if r.FormValue("action") == "finish" { // the day that ends a report period: the report form
		if sched, err := s.Tasks.Schedule(r.Context()); err == nil {
			if due, _ := s.Company.ReportDue(r.Context(), sess.UserID, sched, time.Now()); due != nil {
				back = "/worktime/reports/new"
			}
		}
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
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
	var ids []string
	if sess.Admin { // administrators see everyone's day
		ids, err = s.Company.ActiveIDs(r.Context())
	} else {
		var people []company.Person
		people, err = s.Company.Under(r.Context(), sess.UserID)
		for _, p := range people {
			ids = append(ids, p.ID)
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
	total := 0
	for _, d := range days {
		total += d.Worked(now)
	}
	pending, err := s.Company.PendingChanges(r.Context(), ids, sess.Admin)
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
	s.render(w, r, "timeman", page{TitleKey: "workday.page", Nav: "timeman", Data: map[string]any{
		"Days": days, "Team": team, "TeamSize": len(ids), "Start": sched.LateAt(), "Pending": pending, "Forgotten": forgotten,
		"Me": sess.UserID, "Now": now, "Total": total,
		"Month": month, "Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
	}})
}
