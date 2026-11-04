package web

import (
	"context"
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/tasks"
)

// «Генератор документов → Права доступа»: roles of the document generator (crm.DocRights) and whom
// they are given to.

// docPerms are the person's rights of the document generator: administrators — all of them.
func (s *Server) docPerms(ctx context.Context, sess auth.Session) crm.DocPerms {
	if sess.Admin {
		return crm.AllDocPerms()
	}
	p, err := s.CRM.DocPermsOf(ctx, sess.UserID)
	if err != nil {
		s.Log.Error("docgen rights", "user_id", sess.UserID, "err", err)
		return crm.DocPerms{}
	}
	return p
}

// docRight: the person has the right (templates, view, modify, settings) — else crm.ErrForbidden.
func (s *Server) docRight(ctx context.Context, sess auth.Session, right string) error {
	if !s.docPerms(ctx, sess).Can(right) {
		return crm.ErrForbidden
	}
	return nil
}

// docAllowed: the user may do the action with the card of the documents and has the right of the generator.
func (s *Server) docAllowed(r *http.Request, sess auth.Session, kind, id, action, right string) error {
	if err := s.docRight(r.Context(), sess, right); err != nil {
		return err
	}
	return s.docCheck(r, sess, kind, id, action)
}

// docRightOr403 answers 403 to those without the right.
func (s *Server) docRightOr403(w http.ResponseWriter, r *http.Request, sess auth.Session, right string) bool {
	if err := s.docRight(r.Context(), sess, right); err != nil {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return false
	}
	return true
}

// docRoles is the page of the roles (those who change the settings.
func (s *Server) docRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.docRightOr403(w, r, sess, "settings") {
		return
	}
	roles, err := s.CRM.DocRoles(r.Context())
	if err != nil {
		s.fail(w, "docgen roles", err)
		return
	}
	members := map[string][]tasks.User{} // for the people picker
	for _, role := range roles {
		for _, m := range role.Members {
			members[role.ID] = append(members[role.ID], tasks.User{ID: m.ID, Name: m.Name})
		}
	}
	s.render(w, r, "docroles", page{TitleKey: "docroles.title", Nav: "crm_deals", Data: map[string]any{
		"Roles": roles, "Members": members, "Rights": crm.DocRights}})
}

// docRoleSave: the role (the field id; none — a new one), its levels (a field per right) and members (employees and
// «dept:ID»).
func (s *Server) docRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.docRightOr403(w, r, sess, "settings") {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/crm/documents/rights")
		return
	}
	perms := crm.DocPerms{}
	for _, right := range crm.DocRights {
		perms[right.Key] = r.FormValue(right.Key)
	}
	members := r.PostForm["members"]
	if members == nil {
		members = []string{}
	}
	id, err := s.CRM.SaveDocRole(r.Context(), crm.DocRole{ID: r.FormValue("id"), Name: r.FormValue("name"), Perms: perms}, members)
	if err != nil {
		s.taskErr(w, r, err, "/crm/documents/rights")
		return
	}
	s.Log.Info("docgen role saved", "user_id", sess.UserID, "role", id)
	http.Redirect(w, r, "/crm/documents/rights?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#r"+id, http.StatusSeeOther)
}

func (s *Server) docRoleDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.docRightOr403(w, r, sess, "settings") {
		return
	}
	if err := s.CRM.DeleteDocRole(r.Context(), r.FormValue("id")); err != nil {
		s.taskErr(w, r, err, "/crm/documents/rights")
		return
	}
	s.Log.Info("docgen role deleted", "user_id", sess.UserID, "role", r.FormValue("id"))
	http.Redirect(w, r, "/crm/documents/rights", http.StatusSeeOther)
}
