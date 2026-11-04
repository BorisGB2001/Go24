package web

import (
	"net/http"
	"net/url"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/mail"
)

// «Восстановить удалённые элементы», as in Outlook: the letters of a mailbox deleted for good in the last
// mail.RecoverDays — «Восстановить» to the folder they were deleted from, «Удалить навсегда».
//   GET  /api/v1/mail/boxes/{id}/recoverable?q=  [{id, folder, role, from, from_name, subject, sent_at, deleted_at, until, size}]
//   POST /api/v1/mail/recoverable/restore        {ids: [ID]} → {letters}
//   POST /api/v1/mail/recoverable/delete         {ids: [ID]} → {letters}
func (s *Server) mailRecoverRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /mail/recover", s.requireUser(s.mailRecoverPage))
	mux.HandleFunc("POST /mail/recover/restore", s.requireUser(s.mailRecoverDo))
	mux.HandleFunc("POST /mail/recover/delete", s.requireUser(s.mailRecoverDo))
	mux.HandleFunc("GET /api/v1/mail/boxes/{id}/recoverable", s.api("mail:read", s.apiMailRecoverable))
	mux.HandleFunc("POST /api/v1/mail/recoverable/restore", s.api("mail:write", s.apiMailRecoverDo))
	mux.HandleFunc("POST /api/v1/mail/recoverable/delete", s.api("mail:write", s.apiMailRecoverDo))
}

func recoverURL(box, q string, extra ...string) string {
	v := url.Values{"box": {box}}
	if q != "" {
		v.Set("q", q)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Set(extra[i], extra[i+1])
	}
	return "/mail/recover?" + v.Encode()
}

func (s *Server) mailRecoverPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	boxes, err := s.Mail.Mailboxes(ctx, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/mail")
		return
	}
	q := r.URL.Query()
	box := q.Get("box")
	if f, err := s.Mail.Folder(ctx, sess.UserID, q.Get("folder")); err == nil { // the link in «Удалённые»
		box = f.MailboxID
	}
	known := false
	for _, b := range boxes {
		known = known || b.ID == box
	}
	if !known && len(boxes) > 0 {
		box = boxes[0].ID
	}
	var list []mail.Recoverable
	if box != "" {
		if list, err = s.Mail.RecoverableOf(ctx, sess.UserID, box, q.Get("q")); err != nil {
			s.taskErr(w, r, err, "/mail")
			return
		}
	}
	trash := ""
	if box != "" {
		if fs, err := s.Mail.Folders(ctx, sess.UserID, box); err == nil {
			for _, f := range fs {
				if f.Role == "trash" && trash == "" {
					trash = f.ID
				}
			}
		}
	}
	s.render(w, r, "mailrecover", page{TitleKey: "mail.recover.title", Nav: "mail", Data: map[string]any{
		"Boxes": boxes, "Box": box, "Q": q.Get("q"), "List": list, "Days": mail.RecoverDays, "Trash": trash}})
}

// mailRecoverDo: «Восстановить» (/restore) or «Удалить навсегда» (/delete) the letters chosen.
func (s *Server) mailRecoverDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	box, q := r.FormValue("box"), r.FormValue("q")
	ids := r.Form["id"]
	if len(ids) == 0 {
		http.Redirect(w, r, recoverURL(box, q, "msg", s.tr(r, "mail.recover.none_chosen")), http.StatusSeeOther)
		return
	}
	restore := r.URL.Path == "/mail/recover/restore"
	var n int
	var err error
	if restore {
		n, err = s.Mail.Recover(r.Context(), sess.UserID, ids)
	} else {
		n, err = s.Mail.ForgetRecoverable(r.Context(), sess.UserID, ids)
	}
	if err != nil && n == 0 {
		s.mailErr(w, r, err, recoverURL(box, q))
		return
	}
	s.Log.Info("mail recoverable", "user_id", sess.UserID, "restore", restore, "letters", n)
	key := "mail.recover.restored"
	if !restore {
		key = "mail.recover.deleted"
	}
	msg := s.tr(r, key, n)
	if err != nil {
		msg += " " + s.errText(r, err)
	}
	http.Redirect(w, r, recoverURL(box, q, "msg", msg), http.StatusSeeOther)
}

func (s *Server) apiMailRecoverable(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.Mail.RecoverableOf(r.Context(), p.UserID, r.PathValue("id"), r.URL.Query().Get("q"))
	if err != nil {
		return err
	}
	out := make([]map[string]any, len(list))
	for i, l := range list {
		out[i] = map[string]any{"id": l.ID, "folder": l.Folder, "role": l.Role, "from": l.FromAddr, "from_name": l.FromName,
			"subject": l.Subject, "sent_at": l.SentAt, "deleted_at": l.DeletedAt, "until": l.Until(), "size": l.Size}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiMailRecoverDo(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	var n int
	var err error
	if r.URL.Path == "/api/v1/mail/recoverable/restore" {
		n, err = s.Mail.Recover(r.Context(), p.UserID, in.IDs)
	} else {
		n, err = s.Mail.ForgetRecoverable(r.Context(), p.UserID, in.IDs)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"letters": n})
	return nil
}
