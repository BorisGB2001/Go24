package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// inviteTTL is how long a sign-up link works.
const inviteTTL = 7 * 24 * time.Hour

// invitePage: «Пригласить сотрудников» (administrators and HR officers with the right) — the form and the invitations sent.
func (s *Server) invitePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	list, err := s.Company.Invites(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/company")
		return
	}
	_, depts, err := s.tree(r)
	if err != nil {
		s.fail(w, "invite departments", err)
		return
	}
	s.render(w, r, "invite", page{TitleKey: "invite.title", Nav: "invite", Data: map[string]any{
		"Invites": list, "Depts": depts, "Dept": r.URL.Query().Get("dept"), "Ready": s.Authentik != nil && s.AuthentikPublic != "",
		"Now": time.Now(), "New": r.URL.Query().Get("new"), "Mail": s.inviterMailbox(r.Context(), sess.UserID) != ""}})
}

// inviteSend invites everyone of the form (a line each; the old single e-mail and name too): the sign-up link of
// Authentik, kept and sent by e-mail; the page lists them.
func (s *Server) inviteSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	people, err := s.invitees(r.Context(), r.FormValue("people"), r.FormValue("email"), r.FormValue("name"))
	if err == nil {
		err = s.Company.CheckDepartment(r.Context(), r.FormValue("department"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/company/invite")
		return
	}
	sent, mailed, err := s.invite(r, sess.UserID, sess.Name, r.FormValue("department"), people)
	if err != nil {
		s.inviteFailed(w, r, err)
		return
	}
	s.Log.Info("employees invited", "user_id", sess.UserID, "count", len(sent))
	q := url.Values{"msg": {s.tr(r, "invite.sent", len(sent), mailed)}}
	if len(sent) == 1 {
		q.Set("new", sent[0].Email)
	}
	http.Redirect(w, r, "/company/invite?"+q.Encode(), http.StatusSeeOther)
}

// inviteResend: «Пригласить повторно» — a new link for another week (the old one stops working), sent again.
func (s *Server) inviteResend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	inv, mailed, err := s.reinvite(r, sess.UserID, sess.Name, r.PathValue("id"))
	if err != nil {
		if _, ok := err.(*i18n.Error); ok || err == company.ErrNotFound {
			s.taskErr(w, r, err, "/company/invite")
		} else {
			s.inviteFailed(w, r, err)
		}
		return
	}
	msg := s.tr(r, "invite.renewed", inv.Email, inv.Expires.In(time.Local).Format("02.01.2006"))
	if mailed {
		msg = s.tr(r, "invite.mailed", inv.Email)
	}
	http.Redirect(w, r, "/company/invite?"+url.Values{"msg": {msg}, "new": {inv.Email}}.Encode(), http.StatusSeeOther)
}

// employeeInviteCancel: «Отменить» — the link stops working.
func (s *Server) employeeInviteCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.canInvite(w, r, sess) {
		return
	}
	inv, err := s.uninvite(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		if _, ok := err.(*i18n.Error); ok || err == company.ErrNotFound {
			s.taskErr(w, r, err, "/company/invite")
		} else {
			s.inviteFailed(w, r, err)
		}
		return
	}
	http.Redirect(w, r, "/company/invite?msg="+url.QueryEscape(s.tr(r, "invite.revoked", inv.Email)), http.StatusSeeOther)
}

func (s *Server) inviteFailed(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("authentik invite", "err", err)
	http.Redirect(w, r, "/company/invite?msg="+url.QueryEscape(s.tr(r, "invite.failed")), http.StatusSeeOther)
}

// invitees: the lines of the form, or the single e-mail and name.
func (s *Server) invitees(ctx context.Context, text, email, name string) ([]company.Invitee, error) {
	if text == "" && email != "" {
		email, name, err := s.Company.CheckInvite(ctx, email, name)
		if err != nil {
			return nil, err
		}
		return []company.Invitee{{Email: email, Name: name}}, nil
	}
	return s.Company.ParseInvitees(ctx, text)
}

// invite makes, keeps and mails the invitations; mailed counts those sent by e-mail. Stopped by Authentik midway, the
// ones made stay.
func (s *Server) invite(r *http.Request, byID, byName, dept string, people []company.Invitee) (sent []company.Invite, mailed int, err error) {
	ctx := r.Context()
	if s.Authentik == nil || s.AuthentikPublic == "" {
		return nil, 0, i18n.E("invite.not_ready")
	}
	for _, p := range people {
		inv, err := s.Authentik.Invite(ctx, s.AuthentikPublic, p.Email, p.Name, inviteTTL)
		if err != nil {
			return sent, mailed, err
		}
		id, err := s.Company.SaveInvite(ctx, byID, p.Email, p.Name, dept, inv.Link, inv.ID, inv.Expires)
		if err != nil {
			return sent, mailed, err
		}
		one := company.Invite{ID: id, Email: p.Email, Name: p.Name, Link: inv.Link, AuthentikID: inv.ID, Expires: inv.Expires,
			DepartmentID: dept, InvitedByID: byID, InvitedBy: byName}
		if s.mailInvite(r, byID, byName, &one) {
			mailed++
		}
		sent = append(sent, one)
	}
	return sent, mailed, nil
}

// reinvite: the old Authentik link goes, a new one is kept and mailed.
func (s *Server) reinvite(r *http.Request, byID, byName, id string) (company.Invite, bool, error) {
	ctx := r.Context()
	inv, err := s.Company.Pending(ctx, id)
	if err != nil {
		return inv, false, err
	}
	if s.Authentik == nil || s.AuthentikPublic == "" {
		return inv, false, i18n.E("invite.not_ready")
	}
	if inv.AuthentikID != "" {
		if err := s.Authentik.DeleteInvitation(ctx, inv.AuthentikID); err != nil {
			return inv, false, err
		}
	}
	made, err := s.Authentik.Invite(ctx, s.AuthentikPublic, inv.Email, inv.Name, inviteTTL)
	if err != nil {
		return inv, false, err
	}
	if err := s.Company.RenewInvite(ctx, id, made.Link, made.ID, made.Expires); err != nil {
		return inv, false, err
	}
	inv.Link, inv.AuthentikID, inv.Expires, inv.Mailed = made.Link, made.ID, made.Expires, false
	s.Log.Info("employee invited again", "user_id", byID, "id", id)
	return inv, s.mailInvite(r, byID, byName, &inv), nil
}

// uninvite cancels the invitation in Authentik and in go24.
func (s *Server) uninvite(ctx context.Context, byID, id string) (company.Invite, error) {
	inv, err := s.Company.Pending(ctx, id)
	if err != nil {
		return inv, err
	}
	if inv.AuthentikID != "" && s.Authentik != nil {
		if err := s.Authentik.DeleteInvitation(ctx, inv.AuthentikID); err != nil {
			return inv, err
		}
	}
	if err := s.Company.RevokeInvite(ctx, id); err != nil {
		return inv, err
	}
	s.Log.Info("employee invitation cancelled", "user_id", byID, "id", id)
	now := time.Now()
	inv.Revoked = &now
	return inv, nil
}

// inviterMailbox: the first mailbox of the inviter in go24 ("" — none: they pass the link on themselves).
func (s *Server) inviterMailbox(ctx context.Context, userID string) string {
	if s.Mail == nil {
		return ""
	}
	boxes, err := s.Mail.Mailboxes(ctx, userID)
	if err != nil || len(boxes) == 0 {
		return ""
	}
	return boxes[0].ID
}

// mailInvite sends the invitation from the inviter's mailbox, as collabs do for guests.
func (s *Server) mailInvite(r *http.Request, byID, byName string, inv *company.Invite) bool {
	ctx := r.Context()
	box := s.inviterMailbox(ctx, byID)
	if box == "" {
		return false
	}
	body := s.tr(r, "invite.mail_body", inv.Name, byName, inv.Link, inv.Expires.In(time.Local).Format("02.01.2006"))
	if err := s.Mail.Send(ctx, byID, box, inv.Email, s.tr(r, "invite.mail_subject", byName), body); err != nil {
		s.Log.Warn("employee invite mail", "err", err)
		return false
	}
	if err := s.Company.SetInviteMailed(ctx, inv.ID); err != nil {
		s.Log.Warn("employee invite mailed", "err", err)
	}
	inv.Mailed = true
	return true
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
