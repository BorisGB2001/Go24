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

// «Права доступа» of the company structure: an administrator sets for the heads of departments, the
// deputies and every employee what they may do in the structure — nothing, in their own departments or everywhere.

func (s *Server) structRightsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	rights, err := s.Company.StructRights(r.Context())
	if err != nil {
		s.fail(w, "structure rights", err)
		return
	}
	var flash string
	if r.URL.Query().Get("saved") == "1" {
		flash = s.tr(r, "portal.saved")
	}
	s.render(w, r, "structrights", page{TitleKey: "structrights.title", Nav: "structure", Flash: flash, Data: map[string]any{
		"Rights": rights, "Roles": company.StructRoles, "Actions": company.StructActions, "Scopes": company.StructScopes}})
}

func (s *Server) structRightsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	in := company.StructRights{}
	for _, role := range company.StructRoles {
		in[role] = map[string]string{}
		for _, action := range company.StructActions {
			in[role][action] = r.FormValue(role + "." + action)
		}
	}
	if err := s.Company.SetStructRights(r.Context(), in); err != nil {
		s.taskErr(w, r, err, "/company/structure/rights")
		return
	}
	s.audit(r, audit.Event{Kind: "structure_rights_saved", UserID: sess.UserID})
	http.Redirect(w, r, "/company/structure/rights?saved=1", http.StatusSeeOther)
}

// structCheck: the action in each of the departments, or ErrForbidden.
func (s *Server) structCheck(r *http.Request, userID string, admin bool, action string, depts ...string) error {
	a, err := s.Company.StructAccessOf(r.Context(), userID, admin)
	if err != nil {
		return err
	}
	for _, d := range depts {
		if !a.Can(action, d) {
			return company.ErrForbidden
		}
	}
	return nil
}

// structAnyOf: the action in one of the departments (none — yes: a person of no department).
func (s *Server) structAnyOf(r *http.Request, userID, action string, depts []string) bool {
	if len(depts) == 0 {
		return true
	}
	a, err := s.Company.StructAccessOf(r.Context(), userID, false)
	return err == nil && slices.ContainsFunc(depts, func(d string) bool { return a.Can(action, d) })
}

// changedDepts are the departments a person goes into or out of.
func changedDepts(was, now []string) []string {
	var out []string
	for _, d := range was {
		if !slices.Contains(now, d) {
			out = append(out, d)
		}
	}
	for _, d := range now {
		if d != "" && !slices.Contains(was, d) && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out
}

// GET|PUT /api/v1/departments/rights — {"head":{"create":"own",…},"deputy":{…},"employee":{…}}: the rights
// (administrators change them; scopes "own" and "all", none — left out or "").
func (s *Server) apiStructRights(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPut {
		if !s.apiAdmin(r.Context(), p.UserID) {
			return i18n.E("err.forbidden")
		}
		in := company.StructRights{}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		for _, role := range company.StructRoles {
			for _, scope := range in[role] {
				if !slices.Contains(company.StructScopes, scope) {
					return i18n.E("err.bad_form")
				}
			}
		}
		if err := s.Company.SetStructRights(r.Context(), in); err != nil {
			return err
		}
		s.audit(r, audit.Event{Kind: "structure_rights_saved", UserID: p.UserID})
	}
	rights, err := s.Company.StructRights(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rights)
	return nil
}

// structMoveCheck: a department goes under another parent only where the person may change departments too.
func (s *Server) structMoveCheck(r *http.Request, sess auth.Session, id, parent string) error {
	if sess.Admin {
		return nil
	}
	_, flat, err := s.tree(r)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(flat, func(d *company.Department) bool { return d.ID == id }); i >= 0 && flat[i].ParentID == parent {
		return nil
	}
	return s.structCheck(r, sess.UserID, false, "edit", parent)
}
