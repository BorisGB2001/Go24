package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Таблица рабочего времени» and a working day with its changes: managers see their people's month,
// an employee changes their own day with a reason (a manager approves), managers change their people's days at once.

// sheetPeople are the people whose days the user sees: an administrator — everyone, a manager — themself and the
// people below them; dept narrows to a department with its sub-departments.
func (s *Server) sheetPeople(ctx context.Context, sess auth.Session, dept string) ([]company.Person, error) {
	var people []company.Person
	if sess.Admin || dept != "" {
		list, err := s.Company.Employees(ctx, company.Filter{DepartmentID: dept})
		if err != nil {
			return nil, err
		}
		for _, p := range list {
			people = append(people, company.Person{ID: p.ID, Name: p.Name, Position: p.Position, PhotoKey: p.PhotoKey})
		}
		if sess.Admin {
			return people, nil
		}
	}
	under, err := s.Company.Under(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	me, err := s.Company.Profile(ctx, sess.UserID)
	if err != nil {
		return nil, err
	}
	mine := append([]company.Person{{ID: me.ID, Name: me.Name, Position: me.Position, PhotoKey: me.PhotoKey}}, under...)
	if dept == "" {
		return mine, nil
	}
	return slices.DeleteFunc(people, func(p company.Person) bool {
		return !slices.ContainsFunc(mine, func(m company.Person) bool { return m.ID == p.ID })
	}), nil
}

func sheetMonth(r *http.Request, now time.Time) time.Time {
	if m, err := time.ParseInLocation("2006-01", r.URL.Query().Get("month"), time.Local); err == nil {
		return m
	}
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
}

// worksheet: GET /worktime/table?month=2026-10&dept=ID — people × days of the month.
func (s *Server) worksheet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	now := time.Now()
	month := sheetMonth(r, now)
	dept := r.URL.Query().Get("dept")
	people, err := s.sheetPeople(r.Context(), sess, dept)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = p.ID
	}
	end := month.AddDate(0, 1, 0)
	days, err := s.Company.SheetDays(r.Context(), ids, month, end)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	absences, err := s.Company.Absences(r.Context(), month, end.AddDate(0, 0, -1))
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	pending, err := s.Company.PendingDays(r.Context(), ids, month, end)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	norms, err := s.Company.Norms(r.Context(), ids, sched, month, end)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	_, depts, err := s.Company.Tree(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	var dates []time.Time
	for t := month; t.Before(end); t = t.AddDate(0, 0, 1) {
		dates = append(dates, t)
	}
	off := map[string]bool{}
	for _, t := range dates {
		off[t.Format(time.DateOnly)] = !sched.WorkingDay(t)
	}
	rows := company.BuildSheet(people, days, absences, pending, norms, sched, month, now)
	if r.URL.Query().Get("export") == "csv" {
		s.worksheetCSV(w, r, rows, month)
		return
	}
	s.render(w, r, "worksheet", page{TitleKey: "worksheet.title", Nav: "timeman", Data: map[string]any{
		"Rows": rows, "Dates": dates, "Off": off,
		"Today": now.Format(time.DateOnly), "Month": month, "Dept": dept, "Depts": depts,
		"Prev": month.AddDate(0, -1, 0).Format("2006-01"), "Next": month.AddDate(0, 1, 0).Format("2006-01"),
		"Sched": sched,
	}})
}

// worksheetCSV: the month of the table for Excel — a person per row, the hours of each day (absences by their
// letter, days off by «В»), the totals and the hours by the schedule.
func (s *Server) worksheetCSV(w http.ResponseWriter, r *http.Request, rows []company.SheetRow, month time.Time) {
	hm := func(secs int) string { return fmt.Sprintf("%d:%02d", secs/3600, secs/60%60) }
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=worktime-"+month.Format("2006-01")+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	head := []string{s.tr(r, "workday.col.who"), s.tr(r, "company.col.position")}
	for t := month; t.Month() == month.Month(); t = t.AddDate(0, 0, 1) {
		head = append(head, t.Format("02.01"))
	}
	head = append(head, s.tr(r, "worksheet.days"), s.tr(r, "worksheet.hours"), s.tr(r, "worksheet.plan"),
		s.tr(r, "worksheet.violations"))
	_ = cw.Write(head)
	for _, row := range rows {
		rec := []string{row.Name, row.Position}
		for _, c := range row.Cells {
			v := ""
			switch c.Kind {
			case "work":
				v = hm(c.Worked)
				if len(c.Flags) > 0 {
					v += " !"
				}
			case "absence":
				v = s.tr(r, "worksheet.abs."+c.Absence)
			case "off":
				v = s.tr(r, "worksheet.csv_off")
			case "none":
				v = s.tr(r, "worksheet.csv_none")
			}
			rec = append(rec, v)
		}
		rec = append(rec, strconv.Itoa(row.Days), hm(row.Worked), hm(row.Plan), strconv.Itoa(row.Violations))
		_ = cw.Write(rec)
	}
	cw.Flush()
}

// dayParams: the person and the day of /worktime/d/{user}/{day}; the user may see it — their own, or as a manager.
func (s *Server) dayParams(r *http.Request, sess auth.Session) (string, time.Time, bool, error) {
	userID := r.PathValue("user")
	day, err := time.ParseInLocation(time.DateOnly, r.PathValue("day"), time.Local)
	if err != nil {
		return "", day, false, i18n.E("err.bad_form")
	}
	manager, err := s.Company.Manages(r.Context(), sess.UserID, userID, sess.Admin)
	if err != nil {
		return "", day, false, err
	}
	if !manager && userID != sess.UserID {
		return "", day, false, i18n.E("err.forbidden")
	}
	return userID, day, manager, nil
}

// workdayPage: a day of a person — start, end, pauses, report, the violations, the changes and the change form.
func (s *Server) workdayPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	userID, day, manager, err := s.dayParams(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	prof, err := s.Company.Profile(r.Context(), userID)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	d, err := s.Company.Day(r.Context(), userID, day)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	changes, err := s.Company.DayChanges(r.Context(), userID, day)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	dayTasks, err := s.Tasks.DayTasks(r.Context(), sess.UserID, userID, day)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	norm, err := s.Company.NormOf(r.Context(), userID, sched, day, day.AddDate(0, 0, 1))
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	now := time.Now()
	var flags []string
	start, end, pause := "", "", 0
	if d != nil {
		flags = norm.Violations(*d, now)
		start, pause = d.Started.Local().Format("15:04"), d.PauseSecs/60
		if d.Ended != nil {
			end = d.Ended.Local().Format("15:04")
		}
	}
	var pending *company.WorkdayChange
	for i := range changes {
		if changes[i].Status == "pending" {
			pending = &changes[i]
		}
	}
	s.render(w, r, "workday", page{TitleKey: "workday.day_title", Nav: "timeman", Data: map[string]any{
		"Person": prof, "Day": day, "D": d, "Tasks": dayTasks, "Flags": flags, "Changes": changes, "Pending": pending, "Manager": manager,
		"Own": userID == sess.UserID, "Approval": !manager && !sched.FreeEdit, "Now": now,
		"CanEdit": day.Format(time.DateOnly) <= now.Format(time.DateOnly),
		"Start":   start, "End": end, "Pause": pause, "Path": dayPath(userID, day), "Norm": norm, "Plan": norm.Plan(day),
	}})
}

func dayPath(userID string, day time.Time) string {
	return "/worktime/d/" + userID + "/" + day.Format(time.DateOnly)
}

// atClock is the time hh:mm of the day ("" — nil); an end before the start is on the next day (a night shift).
func atClock(day time.Time, hhmm string) (*time.Time, error) {
	if hhmm = strings.TrimSpace(hhmm); hhmm == "" {
		return nil, nil
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return nil, i18n.E("err.bad_form")
	}
	v := time.Date(day.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, time.Local)
	return &v, nil
}

// changeWorkday: POST /worktime/d/{user}/{day} — start, end, pause (minutes) and the reason; only what differs from
// the day is changed.
func (s *Server) changeWorkday(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	userID, day, _, err := s.dayParams(r, sess)
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = dayPath(userID, day)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	d, err := s.Company.Day(r.Context(), userID, day)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	var c company.DayChange
	c.Reason = r.FormValue("reason")
	if c.Started, err = atClock(day, r.FormValue("start")); err == nil {
		c.Ended, err = atClock(day, r.FormValue("end"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if c.Started != nil && c.Ended != nil && !c.Ended.After(*c.Started) {
		e := c.Ended.AddDate(0, 0, 1)
		c.Ended = &e
	}
	if v := strings.TrimSpace(r.FormValue("pause")); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil || m < 0 {
			s.taskErr(w, r, i18n.E("err.workday_pause"), back)
			return
		}
		m *= 60
		c.PauseSecs = &m
	}
	if d != nil { // unchanged fields are left as they are
		if c.Started != nil && c.Started.Equal(d.Started.Truncate(time.Minute)) {
			c.Started = nil
		}
		if c.Ended != nil && d.Ended != nil && c.Ended.Equal(d.Ended.Truncate(time.Minute)) {
			c.Ended = nil
		}
		if c.PauseSecs != nil && *c.PauseSecs/60 == d.PauseSecs/60 {
			c.PauseSecs = nil
		}
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	applied, err := s.Company.ChangeDay(r.Context(), sess.UserID, userID, day, c, sess.Admin, sched.FreeEdit, time.Now())
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("workday changed", "user_id", sess.UserID, "of", userID, "day", day.Format(time.DateOnly), "applied", applied)
	msg := "workday.changed"
	if !applied {
		msg = "workday.sent"
	}
	http.Redirect(w, r, back+querySep(back)+"msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}

func querySep(u string) string {
	if strings.Contains(u, "?") {
		return "&"
	}
	return "?"
}

// decideWorkday: POST /worktime/changes/{id} decision=approve|decline.
func (s *Server) decideWorkday(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/worktime"
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	approve := r.FormValue("decision") == "approve"
	if err := s.Company.DecideChange(r.Context(), sess.UserID, id, approve, sess.Admin, time.Now()); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("workday change decided", "user_id", sess.UserID, "id", id, "approve", approve)
	msg := "workday.declined"
	if approve {
		msg = "workday.approved"
	}
	http.Redirect(w, r, back+querySep(back)+"msg="+url.QueryEscape(s.tr(r, msg)), http.StatusSeeOther)
}
