package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/tasks"
)

// «График отсутствий» in Outlook and on a phone «Подключить к Outlook» (GO24DEV-944): the page of the
// subscription link and the feed itself (no sign-in: the secret link is the key).

// absenceSync: GET /company/absences/sync — the link (of a department with ?dept=) and how to subscribe.
func (s *Server) absenceSync(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		s.taskErr(w, r, tasks.ErrForbidden, "/company/absences")
		return
	}
	token, err := s.Company.AbsenceFeedToken(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "absence feed", err)
		return
	}
	_, depts, err := s.Company.Tree(r.Context())
	if err != nil {
		s.fail(w, "absence feed", err)
		return
	}
	dept := r.URL.Query().Get("dept")
	if !slices.ContainsFunc(depts, func(d *company.Department) bool { return d.ID == dept }) {
		dept = ""
	}
	link := ""
	if token != "" {
		link = s.absURL(r, "/company/absences/feed/"+token+".ics")
		if dept != "" {
			link += "?dept=" + url.QueryEscape(dept)
		}
	}
	webcal := strings.Replace(strings.Replace(link, "https://", "webcal://", 1), "http://", "webcal://", 1)
	s.render(w, r, "absencesync", page{TitleKey: "abssync.title", Nav: "absences", Data: map[string]any{
		"Link": link, "Webcal": webcal, "Depts": depts, "Dept": dept}})
}

// absenceSyncSave: POST /company/absences/sync — issues the link (a new one: the old one stops working).
func (s *Server) absenceSyncSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/company/absences/sync"
	if d := r.FormValue("dept"); d != "" {
		back += "?dept=" + url.QueryEscape(d)
	}
	if tasks.IsGuestCtx(r.Context()) {
		s.taskErr(w, r, tasks.ErrForbidden, back)
		return
	}
	if _, err := s.Company.NewAbsenceFeed(r.Context(), sess.UserID); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// absenceFeed: GET /company/absences/feed/{token}.ics — the absences from three months ago to a year ahead, of the
// employees of the department (with its sub-departments) given by ?dept= or of all.
func (s *Server) absenceFeed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if _, err := s.Company.AbsenceFeedUser(ctx, strings.TrimSuffix(r.PathValue("file"), ".ics")); err != nil {
		if !errors.Is(err, company.ErrNotFound) {
			s.Log.Error("absence feed", "err", err)
		}
		http.NotFound(w, r)
		return
	}
	tr := func(key string, args ...any) string { return s.tr(r, key, args...) }
	title := "go24 · " + tr("absence.title")
	dept := r.URL.Query().Get("dept")
	if dept != "" {
		_, depts, err := s.Company.Tree(ctx)
		if err != nil {
			s.fail(w, "absence feed", err)
			return
		}
		i := slices.IndexFunc(depts, func(d *company.Department) bool { return d.ID == dept })
		if i < 0 { // the department was removed
			http.NotFound(w, r)
			return
		}
		title += " · " + depts[i].Name
	}
	people, err := s.Company.Employees(ctx, company.Filter{DepartmentID: dept})
	if err != nil {
		s.fail(w, "absence feed", err)
		return
	}
	in := make(map[string]bool, len(people))
	for _, p := range people {
		in[p.ID] = true
	}
	now := time.Now()
	list, err := s.Company.Absences(ctx, now.AddDate(0, -3, 0), now.AddDate(1, 0, 0))
	if err != nil {
		s.fail(w, "absence feed", err)
		return
	}
	list = slices.DeleteFunc(list, func(a company.Absence) bool { return !in[a.UserID] })
	kind := func(k string) string { return company.AbsenceKindName(k, tr) }
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write([]byte(company.AbsencesICS(list, title, kind, tr("acting.who"), now)))
}
