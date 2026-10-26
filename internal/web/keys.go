package web

import (
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// ---------- API keys page ----------

func (s *Server) keysPage(w http.ResponseWriter, r *http.Request, sess auth.Session, newToken string, keyErr error) {
	list, err := s.Keys.List(r.Context(), sess.UserID)
	if err != nil {
		s.fail(w, "list keys", err)
		return
	}
	flash := ""
	if keyErr != nil {
		flash = s.I18n.Err(s.lang(r), keyErr)
	}
	s.render(w, r, "keys", page{TitleKey: "title.api_keys", Nav: "keys", Flash: flash, Data: map[string]any{
		"Keys": list, "NewToken": newToken, "Scopes": apikeys.AllScopes, "BaseURL": s.BaseURL,
	}})
}

func (s *Server) keys(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.keysPage(w, r, sess, "", nil)
}

func (s *Server) createKey(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.keysPage(w, r, sess, "", i18n.E("err.bad_form"))
		return
	}
	days, _ := strconv.Atoi(r.FormValue("days"))
	token, err := s.Keys.Create(r.Context(), sess.UserID, r.FormValue("name"), r.Form["scopes"],
		time.Duration(days)*24*time.Hour)
	if err != nil {
		s.keysPage(w, r, sess, "", err)
		return
	}
	s.audit(r, audit.Event{Kind: "key_created", UserID: sess.UserID, Target: r.FormValue("name")})
	// The token is shown once, right in this response; it is never stored or redirected through a URL.
	w.Header().Set("Cache-Control", "no-store")
	s.keysPage(w, r, sess, token, nil)
}

func (s *Server) revokeKey(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM api_keys WHERE id::text = $1 AND user_id = $2 AND revoked_at IS NULL`,
		r.PathValue("id"), sess.UserID).Scan(&name)
	if err := s.Keys.Revoke(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.fail(w, "revoke key", err)
		return
	}
	if name != "" {
		s.audit(r, audit.Event{Kind: "key_revoked", UserID: sess.UserID, Target: name})
	}
	http.Redirect(w, r, "/settings/keys", http.StatusSeeOther)
}
