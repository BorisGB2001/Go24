package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// honorPage is «Доска почёта» of the company: who is on the board now (and to come) and why, ?archive=1 —
// whose days are over, ?dept= (with sub-departments); ?add=ID opens the form for the person (from their profile).
func (s *Server) honorPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, depts, err := s.tree(r)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	f := company.HonorFilter{DepartmentID: q.Get("dept"), Archive: q.Get("archive") == "1"}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	list, err := s.Company.Honors(r.Context(), today, f)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	can, err := s.Company.CanHonor(r.Context(), sess.UserID, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	data := map[string]any{"List": list, "Filter": f, "Departments": depts, "Can": can, "Today": today, "Add": q.Get("add"),
		"ReasonMax": company.HonorReasonMax}
	if can {
		if data["Users"], err = s.Company.Employees(r.Context(), company.Filter{}); err != nil {
			s.taskErr(w, r, err, "/company")
			return
		}
	}
	s.render(w, r, "honor", page{TitleKey: "honor.title", Nav: "honor", Data: data})
}

// canHonor: the person keeps the board (or the error page is shown).
func (s *Server) canHonor(w http.ResponseWriter, r *http.Request, sess auth.Session, back string) bool {
	can, err := s.Company.CanHonor(r.Context(), sess.UserID, sess.Admin)
	if err == nil && !can {
		err = company.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, back)
	}
	return err == nil
}

// honorAdd: POST /company/honor (user, reason, from, to — empty: until taken off).
func (s *Server) honorAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canHonor(w, r, sess, "/company/honor") {
		return
	}
	from, err := time.ParseInLocation(time.DateOnly, r.FormValue("from"), time.Local)
	if err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/company/honor")
		return
	}
	var to *time.Time
	if v := r.FormValue("to"); v != "" {
		t, err := time.ParseInLocation(time.DateOnly, v, time.Local)
		if err != nil {
			s.taskErr(w, r, i18n.E("err.bad_form"), "/company/honor")
			return
		}
		to = &t
	}
	id, err := s.Company.AddHonor(r.Context(), sess.UserID, r.FormValue("user"), r.FormValue("reason"), from, to)
	if err != nil {
		s.taskErr(w, r, err, "/company/honor")
		return
	}
	s.Log.Info("honor added", "user_id", sess.UserID, "id", id, "for", r.FormValue("user"))
	http.Redirect(w, r, "/company/honor", http.StatusSeeOther)
}

// honorDelete: POST /company/honor/{id}/delete — takes the person off the board.
func (s *Server) honorDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/honor"
	if r.FormValue("archive") == "1" {
		back += "?archive=1"
	}
	if !s.canHonor(w, r, sess, back) {
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.Company.DeleteHonor(r.Context(), id); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("honor deleted", "user_id", sess.UserID, "id", id)
	http.Redirect(w, r, back, http.StatusSeeOther)
}
