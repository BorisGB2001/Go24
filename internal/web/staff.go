package web

import (
	"net/http"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
)

// staffDay is a day of «Кадровые изменения» with its changes.
type staffDay struct {
	Day  time.Time
	List []company.StaffChange
}

// staffChanges is «Кадровые изменения»: who was taken on, moved, dismissed, returned — by days, latest first;
// ?dept= (with sub-departments), ?kind=, ?from=&to=.
func (s *Server) staffChanges(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	f := company.StaffFilter{DepartmentID: q.Get("dept"), Kind: q.Get("kind")}
	f.From, _ = time.ParseInLocation(time.DateOnly, q.Get("from"), time.Local)
	f.To, _ = time.ParseInLocation(time.DateOnly, q.Get("to"), time.Local)
	list, err := s.Company.StaffChanges(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	var days []staffDay
	for _, c := range list {
		d := c.At.In(time.Local)
		d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
		if len(days) == 0 || !days[len(days)-1].Day.Equal(d) {
			days = append(days, staffDay{Day: d})
		}
		days[len(days)-1].List = append(days[len(days)-1].List, c)
	}
	s.render(w, r, "staffchanges", page{TitleKey: "staff.title", Nav: "staffchanges", Data: map[string]any{
		"Days": days, "Filter": f, "Departments": depts, "Kinds": company.StaffKinds, "Admin": sess.Admin}})
}
