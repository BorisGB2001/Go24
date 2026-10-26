package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/crm"
)

// CRM → «Права доступа» (administrators): roles, their levels, their people.
func (s *Server) crmRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	roles, err := s.CRM.Roles(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmroles", page{TitleKey: "crmroles.title", Nav: "crm_deals", Data: map[string]any{
		"Roles": roles, "Users": users, "Levels": crm.Levels, "Actions": crm.Actions, "Entities": []string{"deal", "lead", "contact", "company"}}})
}

// crmRoleSave: the role's levels from the form (fields <entity>.<action>), a new role with id "new".
func (s *Server) crmRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	perms := crm.Perms{}
	for e := range crm.Entities {
		perms[e] = map[string]string{}
		for _, a := range crm.Actions {
			perms[e][a] = r.FormValue(e + "." + a)
		}
	}
	if _, err := s.CRM.SaveRole(r.Context(), crm.Role{ID: id, Name: r.FormValue("name"), Perms: perms}); err != nil {
		s.taskErr(w, r, err, "/crm/roles")
		return
	}
	s.Log.Info("crm role saved", "user_id", sess.UserID, "role", id)
	http.Redirect(w, r, "/crm/roles", http.StatusSeeOther)
}

func (s *Server) crmRoleDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.CRM.DeleteRole(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/crm/roles")
		return
	}
	http.Redirect(w, r, "/crm/roles", http.StatusSeeOther)
}

// crmRoleMember gives a person the role (op=add) or takes it away.
func (s *Server) crmRoleMember(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.CRM.SetMember(r.Context(), r.PathValue("id"), r.FormValue("user_id"), r.FormValue("op") != "remove"); err != nil {
		s.taskErr(w, r, err, "/crm/roles")
		return
	}
	http.Redirect(w, r, "/crm/roles", http.StatusSeeOther)
}
