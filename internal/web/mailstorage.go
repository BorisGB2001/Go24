package web

import (
	"net/http"
	"strconv"

	"go24/internal/auth"
	"go24/internal/mail"
)

// mailStorageData is «Хранилище» of the mail settings: one mailbox (box, else the first) counted on its server; what
// the server said wrong is shown on the tab.
func (s *Server) mailStorageData(r *http.Request, userID string, d *mailSettingsData) {
	d.CleanMonths = mail.CleanMonths
	if len(d.Boxes) == 0 {
		return
	}
	d.StorageBox = d.Boxes[0].ID
	for _, b := range d.Boxes {
		if b.ID == r.URL.Query().Get("box") {
			d.StorageBox = b.ID
		}
	}
	st, err := s.Mail.Storage(r.Context(), userID, d.StorageBox)
	if err != nil {
		d.StorageErr = s.errText(r, err)
		return
	}
	d.Storage = &st
}

// mailClean: «Очистить» a folder in «Хранилище» — months (0 — all the letters).
func (s *Server) mailClean(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := settingsURL("storage")
	f, err := s.Mail.Folder(r.Context(), sess.UserID, id)
	if err == nil {
		back = settingsURL("storage", "box", f.MailboxID)
	}
	months, _ := strconv.Atoi(r.FormValue("months"))
	n, err := s.Mail.Clean(r.Context(), sess.UserID, id, months)
	if err != nil && n == 0 {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail folder cleaned", "user_id", sess.UserID, "folder", id, "months", months, "letters", n)
	http.Redirect(w, r, settingsURL("storage", "box", f.MailboxID, "msg", s.tr(r, "mail.cleaned", n)), http.StatusSeeOther)
}
