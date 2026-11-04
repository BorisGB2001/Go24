package web

import (
	"net/http"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
)

// «Назначить политику», as in Outlook: the retention policy of a folder (its menu) and of letters (the «…» of the
// reading pane); the worker moves the letters that are old enough (mail.RetentionDue).
//
//	GET /api/v1/mail/folders/{id}/policy   {policy}
//	PUT /api/v1/mail/folders/{id}/policy   {policy} ("" — none)
//	GET /api/v1/mail/letters/{id}/policy   {policy, own, folder, expires}
//	PUT /api/v1/mail/letters/{id}/policy   {policy} ("" — the folder's)
func (s *Server) mailRetentionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/folders/{id}/policy", s.requireUser(s.mailFolderPolicy))
	mux.HandleFunc("POST /mail/letters/policy", s.requireUser(s.mailLetterPolicy))
	mux.HandleFunc("GET /api/v1/mail/folders/{id}/policy", s.api("mail:read", s.apiMailFolderPolicy))
	mux.HandleFunc("PUT /api/v1/mail/folders/{id}/policy", s.api("mail:write", s.apiMailFolderPolicy))
	mux.HandleFunc("GET /api/v1/mail/letters/{id}/policy", s.api("mail:read", s.apiMailLetterPolicy))
	mux.HandleFunc("PUT /api/v1/mail/letters/{id}/policy", s.api("mail:write", s.apiMailLetterPolicy))
}

// policyName is how a policy reads ("" — the empty one of the form: none, the folder's).
func (s *Server) policyName(r *http.Request, key, empty string) string {
	if key == "" {
		return s.tr(r, empty)
	}
	return s.tr(r, "mail.pol."+key)
}

func (s *Server) mailFolderPolicy(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, key := r.PathValue("id"), r.FormValue("policy")
	if err := s.Mail.SetFolderPolicy(r.Context(), sess.UserID, id, key); err != nil {
		s.mailErr(w, r, err, mailURL(id, ""))
		return
	}
	s.mailDone(w, r, mailURL(id, ""), s.tr(r, "mail.pol.done", s.policyName(r, key, "mail.pol.none")), nil)
}

func (s *Server) mailLetterPolicy(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	back, key := mailBack(r), r.FormValue("policy")
	n, err := s.Mail.SetLetterPolicy(r.Context(), sess.UserID, letterIDs(r.Form["letter"]), key)
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.mailDone(w, r, back, s.tr(r, "mail.pol.done", s.policyName(r, key, "mail.pol.folder")), map[string]any{"letters": n})
}

func (s *Server) apiMailFolderPolicy(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if r.Method == http.MethodPut {
		var in struct {
			Policy string `json:"policy"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Mail.SetFolderPolicy(r.Context(), p.UserID, id, in.Policy); err != nil {
			return err
		}
	}
	f, err := s.Mail.Folder(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": f.Policy})
	return nil
}

func (s *Server) apiMailLetterPolicy(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if r.Method == http.MethodPut {
		var in struct {
			Policy string `json:"policy"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if _, err := s.Mail.SetLetterPolicy(r.Context(), p.UserID, []string{id}, in.Policy); err != nil {
			return err
		}
	}
	ret, err := s.Mail.LetterRetention(r.Context(), p.UserID, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": ret.Policy(), "own": ret.Own, "folder": ret.Folder, "expires": ret.Expires})
	return nil
}
