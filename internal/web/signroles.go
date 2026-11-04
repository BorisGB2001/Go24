package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"go24/internal/auth"
	"go24/internal/sign"
	"go24/internal/tasks"
)

// «Подпись → Права доступа»: roles of «Подпись» (sign.Rights) and whom they are given to.

// signPerms are the person's rights of «Подпись»: administrators in admin mode — all of them.
func (s *Server) signPerms(ctx context.Context, sess auth.Session) sign.Perms {
	if sess.AdminActive() {
		return sign.AllPerms()
	}
	p, err := s.Sign.PermsOf(ctx, sess.UserID)
	if err != nil {
		s.Log.Error("sign rights", "user_id", sess.UserID, "err", err)
		return sign.Perms{}
	}
	return p
}

// signLevel: does the level of the right (docs or edit) reach the documents sent by the author — «own» none of
// others', «dept» of the people of the person's departments with the ones below, «all» everyone's?
func (s *Server) signLevel(ctx context.Context, sess auth.Session, right, author string) bool {
	if author == sess.UserID {
		return true
	}
	switch s.signPerms(ctx, sess)[right] {
	case "all":
		return true
	case "dept":
		team, err := s.Sign.Team(ctx, sess.UserID)
		return err == nil && slices.Contains(team, author)
	}
	return false
}

// signEdits: may the person manage the document — remind, change the deadline, recall, send anew, place the blocks
// (its sender, or by the right «edit»)?
func (s *Server) signEdits(ctx context.Context, sess auth.Session, d sign.Doc) bool {
	return s.signLevel(ctx, sess, "edit", d.AuthorID)
}

// signListScope narrows the list of «Подпись» by the right «docs»: all (everyone's), or the people whose documents the
// person sees besides their own (lf.Team).
func (s *Server) signListScope(ctx context.Context, sess auth.Session, lf *sign.ListFilter) (all bool, err error) {
	switch s.signPerms(ctx, sess)["docs"] {
	case "all":
		return true, nil
	case "dept":
		lf.Team, err = s.Sign.Team(ctx, sess.UserID)
	}
	return false, err
}

// signRoles is the page of the roles (administrators).
func (s *Server) signRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	roles, err := s.Sign.Roles(r.Context())
	if err != nil {
		s.fail(w, "sign roles", err)
		return
	}
	members := map[string][]tasks.User{} // for the people picker
	for _, role := range roles {
		for _, m := range role.Members {
			members[role.ID] = append(members[role.ID], tasks.User{ID: m.ID, Name: m.Name})
		}
	}
	s.render(w, r, "signroles", page{TitleKey: "signroles.title", Nav: "sign", Data: map[string]any{
		"Roles": roles, "Members": members, "Rights": sign.Rights}})
}

// signRoleSave: the role (the field id; none — a new one), its levels (a field per right) and members (employees and
// «dept:ID»). The role is a field, not a part of the path: /sign/{id}/… are the documents.
func (s *Server) signRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/sign/rights")
		return
	}
	id := r.FormValue("id")
	perms := sign.Perms{}
	for _, right := range sign.Rights {
		perms[right.Key] = r.FormValue(right.Key)
	}
	id, err := s.Sign.SaveRole(r.Context(), sign.Role{ID: id, Name: r.FormValue("name"), Perms: perms}, r.PostForm["members"])
	if err != nil {
		s.taskErr(w, r, err, "/sign/rights")
		return
	}
	s.Log.Info("sign role saved", "user_id", sess.UserID, "role", id)
	http.Redirect(w, r, "/sign/rights?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#r"+id, http.StatusSeeOther)
}

func (s *Server) signRoleDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.Sign.DeleteRole(r.Context(), r.FormValue("id")); err != nil {
		s.taskErr(w, r, err, "/sign/rights")
		return
	}
	s.Log.Info("sign role deleted", "user_id", sess.UserID, "role", r.FormValue("id"))
	http.Redirect(w, r, "/sign/rights", http.StatusSeeOther)
}
