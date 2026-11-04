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
	pipes, err := s.CRM.Pipelines(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	if len(pipes) < 2 { // the deals of the only pipeline are just the deals
		pipes = nil
	}
	s.render(w, r, "crmroles", page{TitleKey: "crmroles.title", Nav: "crm_deals", Data: map[string]any{
		"Roles": roles, "Users": users, "Levels": crm.Levels, "Actions": crm.Actions, "Entities": []string{"deal", "lead", "contact", "company"},
		"Pipelines": pipes}})
}

// crmRoleSave: the role's levels from the form (fields <entity>.<action>, the deals of a pipeline —
// deal:<pipeline id>.<action>, "" — as for all the deals), a new role with id "new".
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
	keys := []string{}
	for e := range crm.Entities {
		keys = append(keys, e)
	}
	if pipes, err := s.CRM.Pipelines(r.Context()); err == nil {
		for _, p := range pipes {
			keys = append(keys, "deal:"+p.ID)
		}
	}
	for _, k := range keys {
		perms[k] = map[string]string{}
		for _, a := range crm.Actions {
			if v := r.FormValue(k + "." + a); v != "" {
				perms[k][a] = v
			}
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

// crmRoleMember gives a person or a department («dept:<id>») the role (op=add) or takes it away.
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
