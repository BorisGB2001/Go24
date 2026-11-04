package web

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Календарь» of tasks: the person's tasks on the days of a month, a week or a day — from the planned
// start to the deadline; a task dropped onto another day gets its deadline there.

// taskCalDay is a day of the grid.
type taskCalDay struct {
	Date         time.Time
	Out, Today   bool // Out: of another month
	Off, Holiday bool
	Items        []taskCalItem
}

// taskCalItem is a task on a day; Due: the day of its deadline (the task is dragged from there).
type taskCalItem struct {
	T   tasks.Task
	Due bool
}

var taskCalViews = []string{"month", "week", "day"}

// taskCalendar: GET /tasks/calendar?view=month|week|day&date=2026-10-14&role=… (a role of «Мои задачи», "" — any).
func (s *Server) taskCalendar(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	view := q.Get("view")
	if !slices.Contains(taskCalViews, view) {
		view = "month"
	}
	role := q.Get("role")
	if role != "" && !slices.Contains(myRoles, role) {
		role = ""
	}
	cs := calendar.DefaultSettings() // the first day of the week and the days off of one's calendar, when it is on
	if s.Calendar != nil {
		var err error
		if cs, err = s.Calendar.Settings(r.Context(), sess.UserID); err != nil {
			s.fail(w, "calendar", err)
			return
		}
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	day := today
	if d, err := time.ParseInLocation("2006-01-02", q.Get("date"), time.Local); err == nil {
		day = d
	}
	var start, prev, next time.Time
	n := 1
	switch view {
	case "month":
		month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, time.Local)
		start, n = cs.WeekOf(month), 42
		prev, next = month.AddDate(0, -1, 0), month.AddDate(0, 1, 0)
	case "week":
		start, n = cs.WeekOf(day), 7
		prev, next = day.AddDate(0, 0, -7), day.AddDate(0, 0, 7)
	default:
		start, prev, next = day, day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)
	}
	end := start.AddDate(0, 0, n)
	f := tasks.Filter{UserID: sess.UserID, Role: role, From: &start, To: &end}
	if role == "" {
		f.MemberID = sess.UserID
	}
	list, err := s.Tasks.ListTasks(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	days := make([]taskCalDay, n)
	for i := range days {
		d := start.AddDate(0, 0, i)
		days[i] = taskCalDay{Date: d, Out: view == "month" && d.Month() != day.Month(), Today: sameDay(d, now),
			Off: cs.Off(d), Holiday: cs.Holiday(d)}
	}
	for _, t := range list {
		from, to := taskSpan(t)
		for i := range days {
			d0, d1 := days[i].Date, days[i].Date.AddDate(0, 0, 1)
			if from.Before(d1) && !to.Before(d0) {
				days[i].Items = append(days[i].Items, taskCalItem{T: t, Due: t.Deadline != nil && sameDay(t.Deadline.Local(), d0)})
			}
		}
	}
	var weeks [][]taskCalDay
	for i := 0; i < n; i += min(n, 7) {
		weeks = append(weeks, days[i:i+min(n, 7)])
	}
	var head []int // the weekdays from the first one
	for i := range min(n, 7) {
		head = append(head, int(start.AddDate(0, 0, i).Weekday()+6)%7+1)
	}
	s.render(w, r, "taskcal", page{TitleKey: "taskcal.title", Nav: "taskcal", Data: map[string]any{
		"View": view, "Views": taskCalViews, "Role": role, "Roles": myRoles, "Day": day, "Start": start, "End": end.AddDate(0, 0, -1),
		"Prev": prev.Format("2006-01-02"), "Next": next.Format("2006-01-02"), "WeekDays": head, "Weeks": weeks,
	}})
}

// taskSpan is the days a task is on: from its planned start (or deadline) to its deadline (or planned end).
func taskSpan(t tasks.Task) (from, to time.Time) {
	pick := func(ts ...*time.Time) time.Time {
		for _, x := range ts {
			if x != nil {
				return x.Local()
			}
		}
		return time.Time{}
	}
	from, to = pick(t.StartPlan, t.Deadline, t.EndPlan), pick(t.Deadline, t.EndPlan, t.StartPlan)
	if to.Before(from) {
		to = from
	}
	return from, to
}

// taskCalMove is a drop onto a day of «Календарь» (fetch, static/js/crmcal.js): POST /t/{key}/date day=2026-10-21 →
// {deadline: "the new deadline as text"} or an error.
func (s *Server) taskCalMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	var d *time.Time
	if err == nil {
		if day, perr := time.ParseInLocation("2006-01-02", r.FormValue("day"), time.Local); perr != nil {
			err = i18n.E("err.bad_date")
		} else {
			d, err = s.Tasks.MoveToDay(r.Context(), sess.UserID, id, day)
		}
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	text := ""
	if d != nil {
		text = d.Local().Format(s.tr(r, "format.datetime"))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"deadline": text})
}
