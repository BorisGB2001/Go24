package web

import (
	"io"
	"net/http"

	"go24/internal/auth"
	"go24/internal/mail"
)

// ---------- «Почта → Настройки → Безопасность»: S/MIME ----------

// As Outlook's «Центр управления безопасностью → Защита электронной почты»: the user's certificates (a .pfx/.p12
// file with its password, or PEM; the newest valid one of an address signs), «Подписывать» and «Шифровать» new
// letters by default. The letter's own «Подписать» and «Зашифровать» are in the editor's «Параметры» (mail/smime.go).

func (s *Server) mailSMIMERoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/settings/security", s.requireUser(s.mailSecuritySave))
	mux.HandleFunc("POST /mail/certs", s.requireUser(s.mailCertAdd))
	mux.HandleFunc("POST /mail/certs/{id}/delete", s.requireUser(s.mailCertDelete))
}

// mailSMIMEData fills the tab.
func (s *Server) mailSMIMEData(r *http.Request, userID string, d *mailSettingsData) (err error) {
	if d.Certs, err = s.Mail.Certs(r.Context(), userID); err != nil {
		return err
	}
	d.Security, err = s.Mail.Security(r.Context(), userID)
	return err
}

func (s *Server) mailSecuritySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.SaveSecurity(r.Context(), sess.UserID, mail.Security{Sign: r.FormValue("sign") != "", Encrypt: r.FormValue("encrypt") != ""}); err != nil {
		s.mailErr(w, r, err, settingsURL("security"))
		return
	}
	http.Redirect(w, r, settingsURL("security", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailCertAdd takes the certificate file with its password.
func (s *Server) mailCertAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	f, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, settingsURL("security", "msg", s.tr(r, "err.mail_smime_file")), http.StatusSeeOther)
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
	c, err := s.Mail.AddCert(r.Context(), sess.UserID, data, r.FormValue("password"))
	if err != nil {
		s.mailErr(w, r, err, settingsURL("security"))
		return
	}
	s.Log.Info("mail certificate added", "user_id", sess.UserID, "email", c.Email, "sha1", c.SHA1)
	http.Redirect(w, r, settingsURL("security", "msg", s.tr(r, "mail.smime.added", c.Email)), http.StatusSeeOther)
}

func (s *Server) mailCertDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Mail.DeleteCert(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, settingsURL("security"))
		return
	}
	http.Redirect(w, r, settingsURL("security", "msg", s.tr(r, "mail.smime.deleted")), http.StatusSeeOther)
}
