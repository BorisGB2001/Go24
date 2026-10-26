package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Рабочие отчёты»: an employee sends the manager a report of a day, week or month; the manager marks
// it and both comment on it. When reports are due is set in «Настройки → Рабочее время».

// reportItems are the tasks and meetings of the person in [from, to].
func (s *Server) reportItems(ctx context.Context, userID string, from, to time.Time) (company.ReportItems, error) {
	var items company.ReportItems
	var err error
	if items.Tasks, err = s.Company.PeriodTasks(ctx, userID, from, to); err != nil {
		return items, err
	}
	if s.Calendar == nil {
		return items, nil
	}
	events, err := s.Calendar.Range(ctx, userID, from, to.AddDate(0, 0, 1))
	if err != nil {
		return items, err
	}
	for _, e := range events { // one's own meetings and those one goes to
		if e.Kind == "event" && (e.OwnerID == userID && e.MyStatus != "declined" || e.MyStatus == "accepted") {
			items.Events = append(items.Events, company.ReportEvent{ID: e.ID, Title: e.Title, Start: e.Start, End: e.End, AllDay: e.AllDay})
		}
	}
	return items, nil
}

// myPeople are the ids of those whose reports the viewer checks: the people under them, everyone for an administrator.
func (s *Server) myPeople(ctx context.Context, sess auth.Session) ([]string, error) {
	var ids []string
	if sess.Admin {
		all, err := s.Company.ActiveIDs(ctx)
		if err != nil {
			return nil, err
		}
		ids = all
	} else {
		people, err := s.Company.Under(ctx, sess.UserID)
		if err != nil {
			return nil, err
		}
		for _, p := range people {
			ids = append(ids, p.ID)
		}
	}
	return slices.DeleteFunc(ids, func(id string) bool { return id == sess.UserID }), nil
}

// workReports: GET /worktime/reports?tab=team|mine&month=2026-10&user=ID&unchecked=1.
func (s *Server) workReports(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	people, err := s.myPeople(r.Context(), sess)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	tab := q.Get("tab")
	if tab != "mine" && len(people) > 0 {
		tab = "team"
	} else {
		tab = "mine"
	}
	now := time.Now()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	if m, err := time.ParseInLocation("2006-01", q.Get("month"), time.Local); err == nil {
		month = m
	}
	f := company.ReportFilter{From: month, To: month.AddDate(0, 1, 0), People: []string{sess.UserID}}
	user := q.Get("user")
	if tab == "team" {
		f.People = people
		if user != "" && slices.Contains(people, user) {
			f.People = []string{user}
		}
		f.Unchecked = q.Get("unchecked") != ""
		if f.Unchecked { // what waits for a mark, of any month
			f.From, f.To = time.Time{}, time.Time{}
		}
	}
	list, err := s.Company.Reports(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	var names []company.Person
	if tab == "team" {
		under, err := s.Company.Under(r.Context(), sess.UserID)
		if err != nil {
			s.taskErr(w, r, err, "/worktime")
			return
		}
		names = under
		if sess.Admin {
			names = nil
			emps, err := s.Company.Employees(r.Context(), company.Filter{})
			if err != nil {
				s.taskErr(w, r, err, "/worktime")
				return
			}
			for _, e := range emps {
				if e.ID != sess.UserID {
					names = append(names, company.Person{ID: e.ID, Name: e.Name})
				}
			}
		}
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	next, err := s.Company.NextReport(r.Context(), sess.UserID, sched, now)
	if err != nil {
		s.taskErr(w, r, err, "/worktime")
		return
	}
	s.render(w, r, "workreports", page{TitleKey: "wreport.title", Nav: "workreports", Data: map[string]any{
		"Tab": tab, "List": list, "Team": len(people) > 0, "People": names, "User": user, "Unchecked": f.Unchecked,
		"Next": next, "Month": month, "PrevMonth": month.AddDate(0, -1, 0).Format("2006-01"), "NextMonth": month.AddDate(0, 1, 0).Format("2006-01"),
	}})
}

// newWorkReport: GET /worktime/reports/new — the form of the next report with the period's time, tasks and meetings.
func (s *Server) newWorkReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	now := time.Now()
	p, err := s.Company.NextReport(r.Context(), sess.UserID, sched, now)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	var items company.ReportItems
	worked := 0
	if !p.Empty() {
		if items, err = s.reportItems(r.Context(), sess.UserID, p.From, p.To); err != nil {
			s.taskErr(w, r, err, "/worktime/reports")
			return
		}
		days, err := s.Company.Workdays(r.Context(), sess.UserID, p.From, p.To.AddDate(0, 0, 1))
		if err != nil {
			s.taskErr(w, r, err, "/worktime/reports")
			return
		}
		for _, d := range days {
			worked += d.Worked(now)
		}
	}
	managers, err := s.Company.Managers(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	s.render(w, r, "workreport", page{TitleKey: "wreport.new", Nav: "workreports", Data: map[string]any{
		"New": true, "Period": p, "Items": items, "Worked": worked, "Managers": managers,
	}})
}

// sendWorkReport: POST /worktime/reports — the next period is computed again, its tasks and meetings are taken now.
func (s *Server) sendWorkReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports/new")
		return
	}
	now := time.Now()
	p, err := s.Company.NextReport(r.Context(), sess.UserID, sched, now)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports/new")
		return
	}
	if p.Empty() {
		s.taskErr(w, r, i18n.E("err.work_report_period"), "/worktime/reports")
		return
	}
	items, err := s.reportItems(r.Context(), sess.UserID, p.From, p.To)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports/new")
		return
	}
	id, err := s.Company.SendReport(r.Context(), sess.UserID, p, r.FormValue("body"), items, now)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports/new")
		return
	}
	s.Log.Info("work report sent", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, "/worktime/reports/"+strconv.FormatInt(id, 10)+"?msg="+url.QueryEscape(s.tr(r, "wreport.sent")), http.StatusSeeOther)
}

func reportID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return 0, company.ErrNotFound
	}
	return id, nil
}

// workReport: GET /worktime/reports/{id} — the report, its mark and comments.
func (s *Server) workReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := reportID(r)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	rep, err := s.Company.Report(r.Context(), sess.UserID, id, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	s.render(w, r, "workreport", page{TitleKey: "wreport.one", Nav: "workreports", Data: map[string]any{
		"Report": rep, "CanMark": rep.UserID != sess.UserID, "Marks": company.ReportMarks, "Items": rep.Items, "Worked": rep.Worked,
		"Period": company.ReportPeriod{From: rep.From, To: rep.To},
	}})
}

// markWorkReport: POST /worktime/reports/{id}/mark — mark=good|bad|none, comment.
func (s *Server) markWorkReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := reportID(r)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	back := "/worktime/reports/" + r.PathValue("id")
	if err := s.Company.MarkReport(r.Context(), sess.UserID, id, r.FormValue("mark"), r.FormValue("comment"), sess.Admin, time.Now()); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("work report marked", "user_id", sess.UserID, "id", id, "mark", r.FormValue("mark"))
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// commentWorkReport: POST /worktime/reports/{id}/comments — body.
func (s *Server) commentWorkReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := reportID(r)
	if err != nil {
		s.taskErr(w, r, err, "/worktime/reports")
		return
	}
	back := "/worktime/reports/" + r.PathValue("id")
	if err := s.Company.CommentReport(r.Context(), sess.UserID, id, r.FormValue("body"), sess.Admin, time.Now()); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#comments", http.StatusSeeOther)
}

// saveReportRules: POST /settings/worktime/reports — the company's rule (period, weekday, monthday) and departments'
// own (dept_id[], dept_period[], dept_weekday[], dept_monthday[]; an empty department is skipped). Administrators only.
func (s *Server) saveReportRules(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/worktime")
		return
	}
	num := func(v string) int {
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0
		}
		return n
	}
	rr := company.ReportRules{ReportRule: company.ReportRule{Period: r.FormValue("period"), Weekday: num(r.FormValue("weekday")),
		Monthday: num(r.FormValue("monthday"))}, Depts: map[string]company.ReportRule{}}
	ids, periods, wds, mds := r.Form["dept_id"], r.Form["dept_period"], r.Form["dept_weekday"], r.Form["dept_monthday"]
	at := func(list []string, i int) string {
		if i < len(list) {
			return list[i]
		}
		return ""
	}
	for i, id := range ids {
		if id == "" {
			continue
		}
		rr.Depts[id] = company.ReportRule{Period: at(periods, i), Weekday: num(at(wds, i)), Monthday: num(at(mds, i))}
	}
	if err := s.Company.SetReportRules(r.Context(), sess.UserID, rr); err != nil {
		s.taskErr(w, r, err, "/settings/worktime")
		return
	}
	s.Log.Info("work report rules saved", "user_id", sess.UserID)
	http.Redirect(w, r, "/settings/worktime?msg="+url.QueryEscape(s.tr(r, "worktime.saved"))+"#reports", http.StatusSeeOther)
}
