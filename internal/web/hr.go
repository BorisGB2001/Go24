package web

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
)

// «Сотрудник кадров» and vacations: the role (an administrator gives it), «в этом году не напоминать», the report of
// vacations by departments (GO24DEV-492).

// userHR: POST /u/{id}/hr — an administrator makes the employee an HR officer with rights (hr=1, rights=absences|invite)
// or takes the role away.
func (s *Server) userHR(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	on := r.FormValue("hr") == "1"
	if err := s.Company.SetHR(r.Context(), id, on, r.Form["rights"]); err != nil {
		s.taskErr(w, r, err, "/u/"+id)
		return
	}
	s.Log.Info("employee hr role", "user_id", sess.UserID, "target", id, "hr", on, "rights", r.Form["rights"])
	http.Redirect(w, r, "/u/"+id, http.StatusSeeOther)
}

// vacationRemindOff: POST /company/vacation-remind-off — «в этом году не напоминать» of the vacation plan.
func (s *Server) vacationRemindOff(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Company.VacationRemindOff(r.Context(), sess.UserID, time.Now().Year()); err != nil {
		s.fail(w, "vacation remind off", err)
		return
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// reportScope: whose vacations the user sees in the report — everyone's (an administrator, an HR officer; nil), their
// people's and their own (a manager); ok false — nobody's (not a manager).
func (s *Server) reportScope(r *http.Request, sess auth.Session) (only map[string]bool, ok bool, err error) {
	if sess.Admin {
		return nil, true, nil
	}
	if h, err := s.Company.HROf(r.Context(), sess.UserID); err != nil || h.On {
		return nil, err == nil, err
	}
	under, err := s.Company.Under(r.Context(), sess.UserID)
	if err != nil || len(under) == 0 {
		return nil, false, err
	}
	only = map[string]bool{sess.UserID: true}
	for _, p := range under {
		only[p.ID] = true
	}
	return only, true, nil
}

// vacationReport: GET /company/absences/report?year=2026[&dept=ID][&format=csv] — the vacations of the year by
// departments: who, when, how many days, who has no plan; CSV for Excel.
func (s *Server) vacationReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	only, ok, err := s.reportScope(r, sess)
	if err != nil {
		s.fail(w, "vacation report scope", err)
		return
	}
	if !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	year, err := strconv.Atoi(q.Get("year"))
	if err != nil || year < 2000 || year > 2100 {
		year = time.Now().Year()
	}
	report, err := s.Company.VacationReport(r.Context(), year, only)
	if err != nil {
		s.taskErr(w, r, err, "/company/absences")
		return
	}
	if d := q.Get("dept"); d != "" {
		report = deptBranch(report, d)
	}
	people, days, noPlan := map[string]bool{}, 0, map[string]bool{}
	for _, d := range report {
		for _, l := range d.Lines {
			if !people[l.ID] {
				people[l.ID], days = true, days+l.Days
			}
			if len(l.Vacations) == 0 {
				noPlan[l.ID] = true
			}
		}
	}
	if q.Get("format") == "csv" {
		s.vacationCSV(w, r, year, report)
		return
	}
	_, depts, err := s.Company.Tree(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company/absences")
		return
	}
	s.render(w, r, "vacations", page{TitleKey: "vacation.report", Nav: "absences", Data: map[string]any{
		"Report": report, "Year": year, "Dept": q.Get("dept"), "Depts": depts, "People": len(people), "Days": days, "NoPlan": len(noPlan),
		"Keep": fmt.Sprintf("year=%d&dept=%s", year, q.Get("dept")),
	}})
}

// deptBranch keeps the department and its sub-departments (the report is in the order of the tree, by depth).
func deptBranch(report []company.VacationDept, id string) []company.VacationDept {
	var out []company.VacationDept
	depth := -1
	for _, d := range report {
		switch {
		case d.ID == id:
			depth = d.Depth
		case depth >= 0 && (d.ID == "" || d.Depth <= depth):
			depth = -1
		}
		if depth >= 0 {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) vacationCSV(w http.ResponseWriter, r *http.Request, year int, report []company.VacationDept) {
	tr := func(k string) string { return s.tr(r, k) }
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=vacations-%d.csv", year))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write([]string{tr("vacation.col.dept"), tr("absence.who"), tr("profile.position"), tr("vacation.col.dates"), tr("vacation.col.days")})
	for _, d := range report {
		dept := d.Name
		if d.ID == "" {
			dept = tr("profile.no_department")
		}
		for _, l := range d.Lines {
			var dates []string
			for _, a := range l.Vacations {
				dates = append(dates, a.From.Format("02.01.2006")+" — "+a.To.Format("02.01.2006"))
			}
			when := strings.Join(dates, ", ")
			if when == "" {
				when = tr("vacation.no_plan")
			}
			_ = cw.Write([]string{dept, l.Name, l.Position, when, strconv.Itoa(l.Days)})
		}
	}
	cw.Flush()
}
