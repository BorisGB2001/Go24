package web

import (
	"net/http"
	"slices"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
)

// «Права доступа» of working time: an administrator sets for the heads of departments, the deputies
// and every employee whose working time they see and change, and who changes the settings and the work schedules.

// worktimeSettings: may the user change the settings of working time and the work schedules.
func (s *Server) worktimeSettings(r *http.Request, sess auth.Session) bool {
	ok, err := s.Company.WorktimeSettings(r.Context(), sess.UserID, sess.Admin)
	return err == nil && ok
}

func (s *Server) worktimeRightsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	rights, err := s.Company.WorktimeRights(r.Context())
	if err != nil {
		s.fail(w, "worktime rights", err)
		return
	}
	var flash string
	if r.URL.Query().Get("saved") == "1" {
		flash = s.tr(r, "portal.saved")
	}
	scopes := map[string][]string{}
	for _, a := range company.WorktimeActions {
		scopes[a] = company.WorktimeScopes(a)
	}
	s.render(w, r, "worktimerights", page{TitleKey: "wtrights.title", Nav: "worktime", Flash: flash, Data: map[string]any{
		"Rights": rights, "Roles": company.StructRoles, "Actions": company.WorktimeActions, "Scopes": scopes}})
}

func (s *Server) worktimeRightsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	in := company.StructRights{}
	for _, role := range company.StructRoles {
		in[role] = map[string]string{}
		for _, action := range company.WorktimeActions {
			in[role][action] = r.FormValue(role + "." + action)
		}
	}
	if err := s.Company.SetWorktimeRights(r.Context(), in); err != nil {
		s.taskErr(w, r, err, "/settings/worktime/rights")
		return
	}
	s.audit(r, audit.Event{Kind: "worktime_rights_saved", UserID: sess.UserID})
	http.Redirect(w, r, "/settings/worktime/rights?saved=1", http.StatusSeeOther)
}

// GET|PUT /api/v1/worktime/rights — {"head":{"view":"own","edit":"own"},"deputy":{…},"employee":{"settings":"all"}}:
// the rights of working time (administrators change them; scopes "own" and "all", for "settings" — "all" only).
func (s *Server) apiWorktimeRights(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPut {
		if !s.apiAdmin(r.Context(), p.UserID) {
			return i18n.E("err.forbidden")
		}
		in := company.StructRights{}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		for _, role := range company.StructRoles {
			for action, scope := range in[role] {
				if !slices.Contains(company.WorktimeActions, action) || !slices.Contains(company.WorktimeScopes(action), scope) {
					return i18n.E("err.bad_form")
				}
			}
		}
		if err := s.Company.SetWorktimeRights(r.Context(), in); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "worktime_rights_saved", UserID: p.UserID})
	}
	rights, err := s.Company.WorktimeRights(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rights)
	return nil
}
