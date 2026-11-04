package web

import (
	"net/http"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
)

// mailTips are «Подсказки по почте» of the letter being written (compose.js): from and each recipient as addr.
func (s *Server) mailTips(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	t, err := s.Mail.MailTips(r.Context(), sess.UserID, r.FormValue("from"), r.Form["addr"], time.Now())
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// apiMailTips: POST /api/v1/mail/mailtips {"from": "me@example.com", "addresses": ["Name <a@example.com>", …]}.
func (s *Server) apiMailTips(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		From      string   `json:"from"`
		Addresses []string `json:"addresses"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	t, err := s.Mail.MailTips(r.Context(), p.UserID, in.From, in.Addresses, time.Now())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, t)
	return nil
}
