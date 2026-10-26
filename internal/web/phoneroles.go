package web

import (
	"context"
	"crypto/subtle"
	"io"
	"net/http"
	"net/url"
	"slices"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// «Телефония → Права доступа»: roles of telephony (telephony.Rights) and whom they are given to.

// telPerms are the person's rights of telephony: administrators in admin mode — all of them.
func (s *Server) telPerms(ctx context.Context, sess auth.Session) telephony.Perms {
	if sess.AdminActive() || s.Telephony == nil {
		return telephony.AllPerms()
	}
	p, err := s.Telephony.PermsOf(ctx, sess.UserID)
	if err != nil {
		s.Log.Error("telephony rights", "user_id", sess.UserID, "err", err)
		return telephony.Perms{}
	}
	return p
}

// telAdmin: may the person change telephony's settings — an administrator or a role with «Настройки телефонии».
func (s *Server) telAdmin(ctx context.Context, sess auth.Session) bool {
	return sess.Admin || (s.Telephony != nil && s.telPerms(ctx, sess).Has("settings", "all"))
}

// telScope narrows a filter of calls by the level of the right ("own", "dept", "all"): the employee asked for
// (user) if the level shows them, else whom it shows — one person (only) or a list (users; nil — everyone).
func (s *Server) telScope(ctx context.Context, sess auth.Session, level, user string) (only string, users []string, err error) {
	switch level {
	case "all":
		return user, nil, nil
	case "dept":
		team, err := s.Telephony.Team(ctx, sess.UserID)
		if err != nil {
			return "", nil, err
		}
		if user != "" && slices.Contains(team, user) {
			return user, nil, nil
		}
		return "", team, nil
	}
	return sess.UserID, nil, nil
}

// phoneSees: does the right «calls» show the person the call?
func (s *Server) phoneSees(ctx context.Context, sess auth.Session, c telephony.Call) bool {
	if c.FromUser.UserID == sess.UserID || c.ToUser.UserID == sess.UserID {
		return s.telPerms(ctx, sess).Has("calls", "own")
	}
	switch s.telPerms(ctx, sess)["calls"] {
	case "all":
		return true
	case "dept":
		team, err := s.Telephony.Team(ctx, sess.UserID)
		return err == nil && (c.FromUser.UserID != "" && slices.Contains(team, c.FromUser.UserID) ||
			c.ToUser.UserID != "" && slices.Contains(team, c.ToUser.UserID))
	}
	return false
}

// telUsers are the employees of the filter by employee: all of them, or the ones of the list (not nil).
func (s *Server) telUsers(ctx context.Context, only []string) ([]tasks.User, error) {
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil || only == nil {
		return users, err
	}
	return slices.DeleteFunc(users, func(u tasks.User) bool { return !slices.Contains(only, u.ID) }), nil
}

// telRoles is the page of the roles (administrators).
func (s *Server) telRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	roles, err := s.Telephony.Roles(r.Context())
	if err != nil {
		s.fail(w, "telephony roles", err)
		return
	}
	members := map[string][]tasks.User{} // for the people picker
	for _, role := range roles {
		for _, m := range role.Members {
			members[role.ID] = append(members[role.ID], tasks.User{ID: m.ID, Name: m.Name})
		}
	}
	s.render(w, r, "telroles", page{TitleKey: "telroles.title", Nav: "telephony", Data: map[string]any{
		"Roles": roles, "Members": members, "Rights": telephony.Rights}})
}

// telRoleSave: the role's levels (a field per right) and members (employees and «dept:ID»), a new role with id "new";
// the dialplan is made anew — the right of outgoing calls lives in it.
func (s *Server) telRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/telephony/rights")
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	perms := telephony.Perms{}
	for _, right := range telephony.Rights {
		perms[right.Key] = r.FormValue(right.Key)
	}
	id, err := s.Telephony.SaveRole(r.Context(), telephony.Role{ID: id, Name: r.FormValue("name"), Perms: perms}, r.PostForm["members"])
	if err != nil {
		s.taskErr(w, r, err, "/telephony/rights")
		return
	}
	s.Telephony.Kick()
	s.Log.Info("telephony role saved", "user_id", sess.UserID, "role", id)
	http.Redirect(w, r, "/telephony/rights?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#r"+id, http.StatusSeeOther)
}

func (s *Server) telRoleDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteRole(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/telephony/rights")
		return
	}
	s.Telephony.Kick()
	s.Log.Info("telephony role deleted", "user_id", sess.UserID, "role", r.PathValue("id"))
	http.Redirect(w, r, "/telephony/rights", http.StatusSeeOther)
}

// phoneOutOK: Asterisk asks whether an employee who may call CRM's clients only may call the number (go24-out-crm; the
// key is ASTERISK_SECRET) — "1" when the number is a contact's, a company's or a lead's.
func (s *Server) phoneOutOK(w http.ResponseWriter, r *http.Request) {
	if s.Telephony == nil || s.Telephony.Key == "" || subtle.ConstantTimeCompare([]byte(r.FormValue("key")), []byte(s.Telephony.Key)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ok := ""
	if s.CRM != nil {
		entity, _, _, err := s.phoneClient(r.Context(), r.FormValue("to"))
		if err != nil {
			s.Log.Error("telephony outgoing to crm", "err", err)
		}
		if entity != "" {
			ok = "1"
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	io.WriteString(w, ok)
}
