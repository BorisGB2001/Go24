package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «График отсутствий» and «Дни рождения» of «Сотрудники».

// absencesPage: GET /company/absences — the chart (people × days of a month or a week, ?period=week&from=DATE&dept=ID&absent=1)
// or ?view=list — the coming three months as a list; ?edit=ID opens an absence in the form, ?user=ID&day=DATE — a new one.
func (s *Server) absencesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	_, report, err := s.reportScope(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	data := map[string]any{"Report": report, "Users": users, "Kinds": company.AbsenceKinds, "Rights": company.ActingRights, "Me": sess.UserID,
		"Today": now, "View": q.Get("view"), "Dept": q.Get("dept"), "OnlyAbsent": q.Get("absent") == "1", "Back": r.URL.RequestURI(),
		"Close": closeAbsenceForm(r.URL.RequestURI())}
	// the form: an absence to change, or a new one for a person and a day picked on the chart
	form := company.Absence{UserID: sess.UserID, Kind: "vacation", From: today, To: today, Rights: []string{"view", "work", "notify"}}
	if id := q.Get("edit"); id != "" {
		a, err := s.Company.Absence(r.Context(), id)
		if err != nil {
			s.taskErr(w, r, err, "/company/absences")
			return
		}
		form, data["Editing"] = *a, true
	} else if d, err := time.ParseInLocation(time.DateOnly, q.Get("day"), time.Local); err == nil {
		form.From, form.To, data["Picked"] = d, d, true
		if u := q.Get("user"); u != "" {
			form.UserID = u
		}
	}
	data["Form"] = form
	if q.Get("view") == "list" {
		if data["List"], err = s.Company.Absences(r.Context(), now, now.AddDate(0, 3, 0)); err != nil {
			s.taskErr(w, r, err, "/company")
			return
		}
		s.render(w, r, "absences", page{TitleKey: "absence.title", Nav: "absences", Data: data})
		return
	}
	// the chart: a month (from its first day) or a week (from Monday)
	from, err := time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
	if err != nil {
		from = today
	}
	week := q.Get("period") == "week"
	var days int
	var prev, next time.Time
	if week {
		from = from.AddDate(0, 0, 1-wdnumOf(from))
		days, prev, next = 7, from.AddDate(0, 0, -7), from.AddDate(0, 0, 7)
	} else {
		from = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, time.Local)
		next = from.AddDate(0, 1, 0)
		days, prev = int(next.Sub(from).Hours()/24+0.5), from.AddDate(0, -1, 0)
	}
	last := from.AddDate(0, 0, days-1)
	list, err := s.Company.Employees(r.Context(), company.Filter{DepartmentID: q.Get("dept")})
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	people := make([]company.Person, len(list))
	for i, p := range list {
		people[i] = company.Person{ID: p.ID, Name: p.Name, Position: p.Position, PhotoKey: p.PhotoKey}
	}
	absences, err := s.Company.Absences(r.Context(), from, last)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	rows := company.AbsenceChart(people, absences, from, days, func(t time.Time) bool { return !sched.WorkingDay(t) }, now)
	if q.Get("absent") == "1" {
		rows = slices.DeleteFunc(rows, func(r company.ChartRow) bool { return !r.Absent })
	}
	// whose absences the user changes: their own, their people's (a manager), everyone's (an administrator, an HR
	// officer with the right)
	hr, err := s.Company.HROf(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	mine := map[string]bool{sess.UserID: true}
	if under, err := s.Company.Under(r.Context(), sess.UserID); err == nil {
		for _, p := range under {
			mine[p.ID] = true
		}
	}
	var dates []time.Time
	off := map[string]bool{}
	for t := from; !t.After(last); t = t.AddDate(0, 0, 1) {
		dates = append(dates, t)
		off[t.Format(time.DateOnly)] = !sched.WorkingDay(t)
	}
	_, depts, err := s.Company.Tree(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	keep := url.Values{"from": {from.Format(time.DateOnly)}}
	if week {
		keep.Set("period", "week")
	}
	if d := q.Get("dept"); d != "" {
		keep.Set("dept", d)
	}
	if q.Get("absent") == "1" {
		keep.Set("absent", "1")
	}
	for k, v := range map[string]any{"Rows": rows, "Keep": keep.Encode(), "Dates": dates, "Off": off, "From": from, "Last": last, "Week": week,
		"Prev": prev.Format(time.DateOnly), "Next": next.Format(time.DateOnly), "TodayKey": today.Format(time.DateOnly),
		"Mine": mine, "Admin": tasks.IsAdmin(r.Context()) || hr.Can("absences"), "Depts": depts, "Kinds": company.AbsenceKinds} {
		data[k] = v
	}
	s.render(w, r, "absences", page{TitleKey: "absence.title", Nav: "absences", Data: data})
}

// wdnumOf: 1 — Monday … 7 — Sunday.
func wdnumOf(t time.Time) int { return (int(t.Weekday())+6)%7 + 1 }

// absenceBack: where to return after the form — the page it was sent from.
func absenceBack(r *http.Request) string {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/company/absences") {
		back = "/company/absences"
	}
	return closeAbsenceForm(back)
}

// closeAbsenceForm: the page without the form's absence or the picked day.
func closeAbsenceForm(page string) string {
	u, err := url.Parse(page)
	if err != nil {
		return "/company/absences"
	}
	v := u.Query()
	v.Del("edit")
	v.Del("day")
	v.Del("user")
	u.RawQuery, u.Fragment = v.Encode(), ""
	return u.String()
}

func absenceDates(r *http.Request) (time.Time, time.Time, error) {
	from, err1 := time.ParseInLocation("2006-01-02", r.FormValue("from"), time.Local)
	to, err2 := time.ParseInLocation("2006-01-02", r.FormValue("to"), time.Local)
	if err1 != nil || err2 != nil {
		return from, to, i18n.E("err.bad_date")
	}
	return from, to, nil
}

func (s *Server) absenceAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := absenceBack(r)
	from, to, err := absenceDates(r)
	if err == nil {
		who := r.FormValue("user")
		if who == "" {
			who = sess.UserID
		}
		_ = r.ParseForm()
		err = s.Company.AddAbsence(r.Context(), sess.UserID, who, r.FormValue("kind"), from, to, r.FormValue("comment"), tasks.IsAdmin(r.Context()),
			r.FormValue("acting"), r.Form["rights"])
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// absenceUpdate: POST /company/absences/{id} — the form of a chosen absence.
func (s *Server) absenceUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := absenceBack(r)
	from, to, err := absenceDates(r)
	if err == nil {
		_ = r.ParseForm()
		err = s.Company.UpdateAbsence(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("user"), r.FormValue("kind"), from, to,
			r.FormValue("comment"), tasks.IsAdmin(r.Context()), r.FormValue("acting"), r.Form["rights"])
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("absence changed", "user_id", sess.UserID, "id", r.PathValue("id"))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) absenceDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := absenceBack(r)
	if err := s.Company.DeleteAbsence(r.Context(), sess.UserID, r.PathValue("id"), tasks.IsAdmin(r.Context())); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) birthdaysPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Company.Birthdays(r.Context(), 60)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	s.render(w, r, "birthdays", page{TitleKey: "birthdays.title", Nav: "birthdays", Data: map[string]any{"List": list}})
}
