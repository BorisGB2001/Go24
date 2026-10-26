package web

import (
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// inviteTTL is how long a sign-up link works.
const inviteTTL = 7 * 24 * time.Hour

// invitePage: «Пригласить сотрудника» (administrators and HR officers with the right) — the form and the invitations sent.
func (s *Server) invitePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	list, err := s.Company.Invites(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	s.render(w, r, "invite", page{TitleKey: "invite.title", Nav: "invite", Data: map[string]any{
		"Invites": list, "Ready": s.Authentik != nil && s.AuthentikPublic != "", "Now": time.Now(), "New": r.URL.Query().Get("new")}})
}

// inviteSend makes the sign-up link in Authentik and keeps it; the page shows it to be passed on.
func (s *Server) inviteSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	email, name, err := s.Company.CheckInvite(r.Context(), r.FormValue("email"), r.FormValue("name"))
	if err != nil {
		s.taskErr(w, r, err, "/company/invite")
		return
	}
	inv, err := s.Authentik.Invite(r.Context(), s.AuthentikPublic, email, name, inviteTTL)
	if err != nil {
		s.Log.Error("authentik invite", "err", err)
		http.Redirect(w, r, "/company/invite?msg="+url.QueryEscape(s.tr(r, "invite.failed")), http.StatusSeeOther)
		return
	}
	if err := s.Company.SaveInvite(r.Context(), sess.UserID, email, name, inv.Link, inv.Expires); err != nil {
		s.fail(w, "save invite", err)
		return
	}
	s.Log.Info("employee invited", "user_id", sess.UserID)
	http.Redirect(w, r, "/company/invite?new="+url.QueryEscape(email), http.StatusSeeOther)
}

// canInvite: administrators and HR officers an administrator let invite employees (GO24DEV-495); others get 403.
// Guests of collabs are invited by their own rules (collabs.go).
func (s *Server) canInvite(w http.ResponseWriter, r *http.Request, sess auth.Session) bool {
	ok, err := s.Company.CanInvite(r.Context(), sess.UserID, sess.Admin && !tasks.IsGuestCtx(r.Context()))
	if err != nil {
		s.fail(w, "can invite", err)
		return false
	}
	if !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
	}
	return ok
}
