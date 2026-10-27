package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// ---------- «Почта»: a mailbox connected by signing in to Google, Microsoft, Яндекс ----------

// mailOAuthPath is where the service sends the user back: the address go24's application is registered with.
const mailOAuthPath = "/mail/oauth"

func (s *Server) mailOAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+mailOAuthPath, s.requireUser(s.mailOAuthStart))
	mux.HandleFunc("GET "+mailOAuthPath, s.requireUser(s.mailOAuthBack))
	mux.HandleFunc("GET /api/v1/mail/oauth", s.api("mail:read", s.apiMailOAuth))
}

// mailOAuthState is who signs in where; it goes to the service and back signed.
type mailOAuthState struct {
	User, Service string
	Exp           int64
}

func (s *Server) mailOAuthStateOf(st mailOAuthState) string {
	raw, _ := json.Marshal(st)
	p := base64.RawURLEncoding.EncodeToString(raw)
	return p + "." + s.Sessions.Stamp("mail-oauth", p)
}

func (s *Server) mailOAuthStateFrom(state, userID string) (mailOAuthState, bool) {
	var st mailOAuthState
	p, stamp, ok := strings.Cut(state, ".")
	if !ok || !s.Sessions.Stamped(stamp, "mail-oauth", p) {
		return st, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil || json.Unmarshal(raw, &st) != nil || st.User != userID || time.Now().Unix() > st.Exp {
		return st, false
	}
	return st, true
}

// mailOAuthStart is «Подключить» Gmail, Outlook or Яндекс (and «Войти снова»): the user signs in at the service.
func (s *Server) mailOAuthStart(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	st := mailOAuthState{User: sess.UserID, Service: r.FormValue("service"), Exp: time.Now().Add(15 * time.Minute).Unix()}
	to, err := s.Mail.OAuthURL(st.Service, s.absURL(r, mailOAuthPath), s.mailOAuthStateOf(st))
	if err != nil {
		s.mailErr(w, r, err, "/mail")
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// mailOAuthBack is the way back from the service: the account signed in to becomes a mailbox (or signs in again).
func (s *Server) mailOAuthBack(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	st, ok := s.mailOAuthStateFrom(r.FormValue("state"), sess.UserID)
	if !ok || r.FormValue("code") == "" { // also «Отмена» at the service: ?error=access_denied
		s.mailErr(w, r, i18n.E("err.mail_oauth_signin"), "/mail")
		return
	}
	id, err := s.Mail.AddOAuth(r.Context(), sess.UserID, st.Service, s.absURL(r, mailOAuthPath), r.FormValue("code"))
	if err != nil {
		s.Log.Warn("mailbox not signed in", "user_id", sess.UserID, "service", st.Service, "err", err)
		s.mailErr(w, r, err, "/mail")
		return
	}
	s.Log.Info("mailbox signed in", "user_id", sess.UserID, "service", st.Service, "mailbox", id)
	if err := s.Mail.Sync(r.Context(), id); err != nil {
		s.Log.Warn("first mail sync", "err", err)
	}
	http.Redirect(w, r, "/mail?box="+url.QueryEscape(id), http.StatusSeeOther)
}

// apiMailOAuth: the services a mailbox can be connected by signing in to (the signing in itself is in the browser).
func (s *Server) apiMailOAuth(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	writeJSON(w, http.StatusOK, map[string]any{"services": append([]string{}, s.Mail.OAuthEnabled()...),
		"redirect": s.absURL(r, mailOAuthPath)})
	return nil
}
