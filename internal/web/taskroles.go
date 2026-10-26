package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// Задачи → «Права доступа» (administrators): roles, their levels, their people.
func (s *Server) taskRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	roles, err := s.Tasks.AccessRoles(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	s.render(w, r, "taskroles", page{TitleKey: "taskroles.title", Nav: "tasks", Data: map[string]any{
		"Roles": roles, "Users": users, "Perms": tasks.PermLevels}})
}

// taskRoleSave: the role's levels from the form (a field per right), a new role with id "new".
func (s *Server) taskRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	perms := tasks.AccessPerms{}
	for _, pl := range tasks.PermLevels {
		perms[pl.Key] = r.FormValue(pl.Key)
	}
	if _, err := s.Tasks.SaveAccessRole(r.Context(), tasks.AccessRole{ID: id, Name: r.FormValue("name"), Perms: perms}); err != nil {
		s.taskErr(w, r, err, "/tasks/roles")
		return
	}
	s.Log.Info("task role saved", "user_id", sess.UserID, "role", id)
	http.Redirect(w, r, "/tasks/roles", http.StatusSeeOther)
}

func (s *Server) taskRoleDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Tasks.DeleteAccessRole(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/tasks/roles")
		return
	}
	http.Redirect(w, r, "/tasks/roles", http.StatusSeeOther)
}

// taskRoleMember gives a person the role (op=add) or takes it away.
func (s *Server) taskRoleMember(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Tasks.SetAccessMember(r.Context(), r.PathValue("id"), r.FormValue("user_id"), r.FormValue("op") != "remove"); err != nil {
		s.taskErr(w, r, err, "/tasks/roles")
		return
	}
	http.Redirect(w, r, "/tasks/roles", http.StatusSeeOther)
}

// taskPerm: has the person the right (export, import) of their roles?
func (s *Server) taskPerm(r *http.Request, sess auth.Session, key string) bool {
	p, err := s.Tasks.PermsOf(r.Context(), sess.UserID)
	return err == nil && p.Has(key, "all")
}
