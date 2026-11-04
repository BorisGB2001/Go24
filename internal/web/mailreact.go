package web

import (
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/mail"
)

// ---------- «Почта»: reactions to colleagues' letters, as Outlook's ----------

// Under each letter of the conversation between colleagues (mail.Reactions): the reactions chosen with their counts
// (who — in the hint) and «Реакция» with 👍 ❤️ 🎉 😆 😮 😢; one reaction of a person, the same one again takes it away.

func (s *Server) mailReactRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/letters/{letter}/react", s.requireUser(s.mailReactPost))
	mux.HandleFunc("GET /api/v1/mail/messages/{id}/reactions", s.api("mail:read", s.apiMailReactions))
	mux.HandleFunc("POST /api/v1/mail/messages/{id}/reactions", s.api("mail:write", s.apiMailReact))
}

// mailReactData fills «Reacts» of the reading pane: the reactions of each letter of the conversation.
func (s *Server) mailReactData(r *http.Request, userID string, thread []mail.Message, data map[string]any) error {
	rs, err := s.Mail.ReactionsOf(r.Context(), userID, thread)
	if err != nil {
		return err
	}
	data["Reacts"], data["ReactKinds"], data["ReactEmoji"] = rs, mail.ReactionKinds, mail.ReactionEmoji
	return nil
}

// mailReactPost sets or takes away the user's reaction (the same as theirs — takes it away).
func (s *Server) mailReactPost(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("letter")
	back := "/mail?" + url.Values{"folder": {r.FormValue("folder")}, "letter": {r.FormValue("open")}}.Encode()
	if r.FormValue("open") == "" {
		back = "/mail?" + url.Values{"folder": {r.FormValue("folder")}, "letter": {id}}.Encode()
	}
	kind := r.FormValue("kind")
	now, err := s.Mail.LetterReactions(r.Context(), sess.UserID, id)
	if err == nil {
		if kind == now.Mine {
			kind = ""
		}
		err = s.Mail.React(r.Context(), sess.UserID, id, kind)
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	if fromForm(r) {
		rs, err := s.Mail.LetterReactions(r.Context(), sess.UserID, id)
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
		writeJSON(w, http.StatusOK, rs)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// apiMailReactions: GET /api/v1/mail/messages/{id}/reactions — {open, mine, list: [{kind, emoji, count, who}]}.
func (s *Server) apiMailReactions(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	rs, err := s.Mail.LetterReactions(r.Context(), p.UserID, r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, rs)
	return nil
}

// apiMailReact: POST /api/v1/mail/messages/{id}/reactions {reaction} — the key owner's reaction (like, heart, party,
// laugh, wow, sad; "" — none); the reactions as GET gives them.
func (s *Server) apiMailReact(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Reaction string `json:"reaction"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if err := s.Mail.React(r.Context(), p.UserID, r.PathValue("id"), in.Reaction); err != nil {
		return err
	}
	return s.apiMailReactions(w, r, p)
}
