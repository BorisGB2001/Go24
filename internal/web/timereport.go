package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/tasks"
)

// «Затраченное время» (Задачи → Затраченное время), as the report: the time each person logged on tasks
// in a month or a chosen period — tasks, time, tasks over «Плановые трудозатраты» — narrowed to a project and a
// department; a person's line opens their tasks with the time. One sees oneself, a department head — the people of
// the departments they head (or are a deputy of) and their sub-departments, the administrator (in admin mode) — everyone. ?format=csv — the same for Excel.

func (s *Server) timeReportPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	now := time.Now()
	from, to := monthOf(now)
	if v, err := parseDate(r.FormValue("from")); err == nil && v != nil {
		from = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local)
	}
	if v, err := parseDate(r.FormValue("to")); err == nil && v != nil {
		to = time.Date(v.Year(), v.Month(), v.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1) // the last day is included
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		from, to = monthOf(now)
	}

	_, all, err := s.tree(r)
	if err != nil {
		s.fail(w, "time report", err)
		return
	}
	team := []string{sess.UserID}
	var depts []*company.Department // the departments one may narrow to: all for the administrator, else the headed ones
	if tasks.IsAdmin(ctx) {
		users, err := s.Tasks.ListUsers(ctx)
		if err != nil {
			s.fail(w, "time report", err)
			return
		}
		team = team[:0]
		for _, u := range users {
			team = append(team, u.ID)
		}
		depts = all
	} else {
		var down func(d *company.Department)
		down = func(d *company.Department) {
			depts = append(depts, d)
			for _, c := range d.Children {
				down(c)
			}
		}
		for _, d := range all {
			if !slices.Contains(depts, d) && (d.HeadID == sess.UserID ||
				slices.ContainsFunc(d.Deputies, func(p company.Person) bool { return p.ID == sess.UserID })) {
				down(d)
			}
		}
		for _, d := range depts {
			people, err := s.Company.DeptPeople(ctx, d.ID, false)
			if err != nil {
				s.fail(w, "time report", err)
				return
			}
			for _, id := range people {
				if !slices.Contains(team, id) {
					team = append(team, id)
				}
			}
		}
	}
	dept := r.FormValue("dept")
	if len(team) > 1 && slices.ContainsFunc(depts, func(d *company.Department) bool { return d.ID == dept }) {
		people, err := s.Company.DeptPeople(ctx, dept, true)
		if err != nil {
			s.fail(w, "time report", err)
			return
		}
		team = slices.DeleteFunc(team, func(id string) bool { return !slices.Contains(people, id) })
	} else {
		dept = ""
	}
	if len(team) <= 1 && dept == "" {
		depts = nil
	}
	projects, err := s.Tasks.ListProjects(ctx, sess.UserID)
	if err != nil {
		s.fail(w, "time report", err)
		return
	}
	project := r.FormValue("project")
	if project != "" && !slices.ContainsFunc(projects, func(p tasks.Project) bool { return p.ID == project }) {
		project = ""
	}

	rows, err := s.Tasks.TimeReport(ctx, team, from, to, project)
	if err != nil {
		s.fail(w, "time report", err)
		return
	}
	var who *tasks.TimeRow
	var list []tasks.TimeTask
	if u := r.FormValue("user"); u != "" {
		if !slices.Contains(team, u) {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
		for i := range rows {
			if rows[i].UserID == u {
				who = &rows[i]
			}
		}
		if who != nil {
			if list, err = s.Tasks.TimeReportTasks(ctx, u, from, to, project); err != nil {
				s.fail(w, "time report", err)
				return
			}
		}
	}
	total := 0 // a task of two people would count twice, so only the time is summed
	for _, row := range rows {
		total += row.Seconds
	}

	q := url.Values{"from": {from.Format(time.DateOnly)}, "to": {to.AddDate(0, 0, -1).Format(time.DateOnly)}}
	if project != "" {
		q.Set("project", project)
	}
	if dept != "" {
		q.Set("dept", dept)
	}
	if r.FormValue("format") == "csv" {
		s.timeReportCSV(w, r, rows, who, list)
		return
	}
	month := func(t time.Time) string {
		f, e := monthOf(t)
		m := url.Values{"from": {f.Format(time.DateOnly)}, "to": {e.AddDate(0, 0, -1).Format(time.DateOnly)}}
		for _, k := range []string{"project", "dept", "user"} {
			if v := r.FormValue(k); v != "" {
				m.Set(k, v)
			}
		}
		return "/tasks/time?" + m.Encode()
	}
	csvQuery := url.Values{"format": {"csv"}}
	for k, v := range q {
		csvQuery[k] = v
	}
	if who != nil {
		csvQuery.Set("user", who.UserID)
	}
	s.render(w, r, "timereport", page{TitleKey: "ttr.title", Nav: "tasktime", Data: map[string]any{
		"Rows": rows, "Total": total, "Who": who, "List": list, "Team": len(team) > 1 || dept != "",
		"Projects": projects, "Project": project, "Depts": depts, "Dept": dept,
		"From": q.Get("from"), "To": q.Get("to"), "Query": q.Encode(), "CSV": "/tasks/time?" + csvQuery.Encode(),
		"Prev": month(from.AddDate(0, -1, 0)), "Next": month(from.AddDate(0, 1, 0))}})
}

// timeReportCSV writes the people of the report, or the tasks of the opened person, for Excel.
func (s *Server) timeReportCSV(w http.ResponseWriter, r *http.Request, rows []tasks.TimeRow, who *tasks.TimeRow, list []tasks.TimeTask) {
	tr := func(k string) string { return s.tr(r, k) }
	hm := func(secs int) string { return fmt.Sprintf("%d:%02d", secs/3600, secs%3600/60) }
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=time-"+time.Now().Format("2006-01-02")+".csv")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	if who != nil {
		_ = cw.Write([]string{tr("tasks.csv.key"), tr("tasks.csv.title"), tr("ttr.spent"), tr("ttr.task_total"), tr("ttr.planned")})
		for _, t := range list {
			plan := ""
			if t.EstimateMinutes != nil && *t.EstimateMinutes > 0 {
				plan = hm(*t.EstimateMinutes * 60)
			}
			_ = cw.Write([]string{t.Key(), t.Title, hm(t.Seconds), hm(t.TotalSeconds), plan})
		}
	} else {
		_ = cw.Write([]string{tr("eff.employee"), tr("ttr.tasks"), tr("ttr.spent"), tr("ttr.over")})
		for _, row := range rows {
			_ = cw.Write([]string{row.Name, fmt.Sprint(row.Tasks), hm(row.Seconds), fmt.Sprint(row.Over)})
		}
	}
	cw.Flush()
}
