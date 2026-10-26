package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- invitations of guests into a collab: /join/{token} ----------
//
// The page of an invitation link: one signed in joins at once; anyone else signs up in Authentik (the guests' flow:
// name, e-mail, login, password) or signs in. The link is kept in a cookie meanwhile, and the first login after it
// puts the person into the collab — a person new to go24 as a guest.

const joinCookie = "go24_join"

// joinToken is the invitation the browser came by (the cookie set on its page).
func (s *Server) joinToken(r *http.Request) string {
	if c, err := r.Cookie(joinCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) joinPage(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Sessions.Get(r); err == nil {
		s.requireUser(s.joinSignedIn)(w, r)
		return
	}
	inv, err := s.Tasks.GuestInviteByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, tasks.ErrNotFound) {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	s.render(w, r, "join", page{TitleKey: "join.page", Data: map[string]any{
		"Invite": inv, "Gone": err != nil, "Signup": s.Authentik != nil && s.AuthentikPublic != ""}})
}

// joinSignedIn: the invitation for one signed in — «Присоединиться», or the collab itself when they are in it.
func (s *Server) joinSignedIn(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	inv, err := s.Tasks.GuestInviteByToken(r.Context(), r.PathValue("token"))
	if errors.Is(err, tasks.ErrNotFound) {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	if err == nil {
		if _, perr := s.Tasks.CollabByKey(r.Context(), sess.UserID, inv.ProjectKey); perr == nil {
			http.Redirect(w, r, "/p/"+inv.ProjectKey, http.StatusFound)
			return
		}
	}
	s.render(w, r, "join", page{TitleKey: "join.page", Data: map[string]any{"Invite": inv, "Gone": err != nil, "SignedIn": true}})
}

// joinSignup keeps the invitation in the browser and sends the person to sign up in Authentik (or, with login=1 or
// when Authentik makes no sign-up links, to sign in).
func (s *Server) joinSignup(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := s.Tasks.GuestInviteByToken(r.Context(), token)
	if err != nil {
		http.Redirect(w, r, "/join/"+url.PathEscape(token), http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: joinCookie, Value: token, Path: "/", MaxAge: 24 * 3600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	if r.URL.Query().Get("login") == "1" || s.Authentik == nil || s.AuthentikPublic == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	link, err := s.Authentik.InviteGuest(r.Context(), s.AuthentikPublic, inv.Email, inv.Name, 24*time.Hour)
	if err != nil {
		s.Log.Error("authentik guest invite", "err", err)
		s.render(w, r, "join", page{TitleKey: "join.page", Flash: s.tr(r, "join.signup_failed"), Data: map[string]any{"Invite": inv}})
		return
	}
	s.Log.Info("guest signs up", "collab", inv.ProjectKey)
	http.Redirect(w, r, link.Link, http.StatusFound)
}

// joinAfterLogin: the first login after an invitation link — into the collab.
func (s *Server) joinAfterLogin(w http.ResponseWriter, r *http.Request, token, userID string) {
	http.SetCookie(w, &http.Cookie{Name: joinCookie, Value: "", Path: "/", MaxAge: -1})
	key, err := s.Tasks.AcceptGuestInvite(r.Context(), token, userID)
	if err != nil {
		s.Log.Info("join after login", "user_id", userID, "err", err)
		to := s.landing(r, userID) // the first item of the menu may have a query of its own
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		http.Redirect(w, r, to+sep+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusFound)
		return
	}
	s.Log.Info("joined collab", "user_id", userID, "collab", key)
	s.Matrix.Wake() // into the collab's chat
	http.Redirect(w, r, "/p/"+key, http.StatusFound)
}

// joinAccept: «Присоединиться» of one signed in.
func (s *Server) joinAccept(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	token := r.PathValue("token")
	key, err := s.Tasks.AcceptGuestInvite(r.Context(), token, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/join/"+url.PathEscape(token))
		return
	}
	s.Log.Info("joined collab", "user_id", sess.UserID, "collab", key)
	s.Matrix.Wake()
	http.Redirect(w, r, "/p/"+key, http.StatusSeeOther)
}

// maxGuestInvites: guests invited by one form at once.
const maxGuestInvites = 50

// guestInviteCreate makes an invitation into the collab: a link, or personal ones by e-mail (sent from the inviter's
// mailbox when they have one in go24) or by phone (the link to pass on in a message) — for several people at once,
// one a line or by commas; the contacts that are not right are named, the rest are invited.
func (s *Server) guestInviteCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	kind := r.FormValue("kind")
	contacts, name := []string{""}, r.FormValue("name")
	if kind != "link" {
		if contacts = tasks.SplitContacts(r.FormValue("contact")); len(contacts) == 0 {
			contacts = []string{""} // the store names the error
		}
		if len(contacts) > maxGuestInvites {
			s.taskErr(w, r, i18n.E("err.guest_invite_many", maxGuestInvites), back)
			return
		}
		if len(contacts) > 1 {
			name = "" // one name is for one person
		}
	}
	var made, sent, bad []string
	for _, c := range contacts {
		inv, err := s.Tasks.CreateGuestInvite(r.Context(), p, sess.UserID, kind, c, name, time.Duration(days)*24*time.Hour)
		var ie *i18n.Error
		if err != nil && len(contacts) > 1 && errors.As(err, &ie) && strings.HasPrefix(ie.Key, "err.guest_invite_") {
			bad = append(bad, c)
			continue
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		made = append(made, inv.Email+inv.Phone)
		if kind == "email" && r.FormValue("send") == "1" && s.sendGuestInvite(r, sess.UserID, sess.Name, p, inv) {
			sent = append(sent, inv.Email)
		}
	}
	s.Log.Info("guest invite", "user_id", sess.UserID, "collab", p.Key, "kind", kind, "n", len(made))
	msg := s.tr(r, "guests.created")
	switch {
	case kind == "link":
	case len(sent) > 0:
		msg = s.tr(r, "guests.email_sent", strings.Join(sent, ", "))
	default:
		msg = s.tr(r, "guests.email_link")
	}
	if len(bad) > 0 {
		msg = s.tr(r, "guests.invited_n", len(made)) + " " + s.tr(r, "guests.bad_contacts", strings.Join(bad, ", "))
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg)+"#guests", http.StatusSeeOther)
}

// guestInviteResend: «Пригласить повторно» — a personal invitation not used yet works another week and goes out
// again (by e-mail from the inviter's mailbox; for a phone — the link to pass on, as before).
func (s *Server) guestInviteResend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	var inv tasks.GuestInvite
	if err == nil {
		inv, err = s.Tasks.RenewGuestInvite(r.Context(), p, sess.UserID, r.PathValue("id"), tasks.GuestInviteTTL)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.tr(r, "guests.renewed", inv.Expires.In(time.Local).Format("02.01.2006"))
	if inv.Kind == "email" && s.sendGuestInvite(r, sess.UserID, sess.Name, p, inv) {
		msg = s.tr(r, "guests.email_sent", inv.Email)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg)+"#guests", http.StatusSeeOther)
}

// sendGuestInvite sends the personal invitation from the inviter's first mailbox in go24 (none — the inviter passes
// the link on themselves).
func (s *Server) sendGuestInvite(r *http.Request, userID, userName string, p tasks.Project, inv tasks.GuestInvite) bool {
	if s.Mail == nil {
		return false
	}
	boxes, err := s.Mail.Mailboxes(r.Context(), userID)
	if err != nil || len(boxes) == 0 {
		return false
	}
	body := s.tr(r, "guests.mail_body", userName, p.Name, s.joinURL(inv), inv.Expires.In(time.Local).Format("02.01.2006"))
	if err := s.Mail.Send(r.Context(), userID, boxes[0].ID, inv.Email, s.tr(r, "guests.mail_subject", p.Name), body); err != nil {
		s.Log.Warn("guest invite mail", "err", err)
		return false
	}
	return true
}

// joinURL is the address of the invitation to pass on.
func (s *Server) joinURL(inv tasks.GuestInvite) string { return s.BaseURL + "/join/" + inv.Token }

func (s *Server) guestInviteRevoke(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.CollabByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.RevokeGuestInvite(r.Context(), p, sess.UserID, r.PathValue("id"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "guests.revoked"))+"#guests", http.StatusSeeOther)
}
