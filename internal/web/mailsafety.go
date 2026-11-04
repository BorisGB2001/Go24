package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// «Сообщить», as in Outlook: «Сообщить о фишинге» (the letter goes to «Удалённые») and «Сообщить о нежелательной
// почте» (to «Нежелательная почта»); with the company's reporting address set by an administrator («Настройки почты →
// Нежелательная почта»), the letter goes there as an attachment, and the administrators see what was reported.
//
//	POST /api/v1/mail/report        {letters: [ID], kind: phishing|junk} → {letters}
//	GET  /api/v1/mail/report/to     {to}
//	PUT  /api/v1/mail/report/to     {to} (administrators)
func (s *Server) mailSafetyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/report", s.requireUser(s.mailReport))
	mux.HandleFunc("POST /mail/settings/report", s.requireUser(s.mailReportToSave))
	mux.HandleFunc("POST /api/v1/mail/report", s.api("mail:write", s.apiMailReport))
	mux.HandleFunc("GET /api/v1/mail/report/to", s.api("mail:read", s.apiMailReportTo))
	mux.HandleFunc("PUT /api/v1/mail/report/to", s.api("mail:write", s.apiMailReportTo))
}

// mailReportLetters reports the letters and mails the copies in the background.
func (s *Server) mailReportLetters(r *http.Request, userID, name string, ids []string, kind string) (int, error) {
	lang := s.lang(r)
	note := func(m mail.Message) mail.ReportNote {
		return mail.ReportNote{Subject: s.I18n.T(lang, "mail.report.subject."+kind, m.Subject),
			Text: s.I18n.T(lang, "mail.report.text."+kind, name, m.From(), m.FromAddr, m.Subject, m.SentAt.In(time.Local).Format("02.01.2006 15:04"))}
	}
	n, send, err := s.Mail.ReportLetters(r.Context(), userID, ids, kind, note)
	if err != nil {
		return 0, err
	}
	s.Log.Info("mail reported", "user_id", userID, "kind", kind, "letters", n)
	s.Mail.Changed(r.Context(), userID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := send(ctx); err != nil {
			s.Log.Warn("mail report copy", "user_id", userID, "err", err)
		}
	}()
	return n, nil
}

func (s *Server) mailReport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	back, kind := mailBack(r), r.FormValue("kind")
	n, err := s.mailReportLetters(r, sess.UserID, sess.Name, letterIDs(r.Form["letter"]), kind)
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	if i := strings.Index(back, "&letter="); i > 0 { // the letter is gone from the folder
		back = back[:i]
	}
	s.mailDone(w, r, back, s.tr(r, "mail.report.done."+kind), map[string]any{"letters": n})
}

// mailReporting is the reporting section of the junk tab.
type mailReporting struct {
	To      string
	Reports []mail.Report
}

func (s *Server) mailReportingData(r *http.Request) (*mailReporting, error) {
	to, err := s.Mail.ReportTo(r.Context())
	if err != nil {
		return nil, err
	}
	list, err := s.Mail.Reports(r.Context(), 50)
	return &mailReporting{To: to, Reports: list}, err
}

func (s *Server) mailReportToSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.AdminActive() {
		s.mailErr(w, r, i18n.E("err.forbidden"), settingsURL("junk"))
		return
	}
	if err := s.Mail.SetReportTo(r.Context(), sess.UserID, r.FormValue("to")); err != nil {
		s.mailErr(w, r, err, settingsURL("junk"))
		return
	}
	http.Redirect(w, r, settingsURL("junk", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) apiMailReport(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Letters []string `json:"letters"`
		Kind    string   `json:"kind"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var name string
	_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id = $1`, p.UserID).Scan(&name)
	n, err := s.mailReportLetters(r, p.UserID, name, in.Letters, in.Kind)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"letters": n})
	return nil
}

// apiMailReportTo: the reporting address; setting it is for administrators.
func (s *Server) apiMailReportTo(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if r.Method == http.MethodPut {
		if !s.apiAdmin(r.Context(), p.UserID) {
			return i18n.E("err.forbidden")
		}
		var in struct {
			To string `json:"to"`
		}
		if err := readJSON(r, &in); err != nil {
			return err
		}
		if err := s.Mail.SetReportTo(r.Context(), p.UserID, in.To); err != nil {
			return err
		}
	}
	to, err := s.Mail.ReportTo(r.Context())
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"to": to})
	return nil
}
