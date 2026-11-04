package web

import (
	"net/http"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
)

// ---------- «Почта»: «Напоминания» (nudges), as Outlook's ----------

// Over the inbox list (mail.Nudges): old letters waiting for an answer — «Получено 4 дн. назад. Ответить?» with
// «Ответить», «Отправлено 5 дн. назад. Нет ответа» with «Напомнить» (a reply to all the letter's recipients); «Закрыть»
// puts a nudge away for good. «Показывать напоминания о письмах» — in «Обработка сообщений».

func (s *Server) mailNudgeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mail/nudges/{id}/off", s.requireUser(s.mailNudgeOff))
	mux.HandleFunc("GET /api/v1/mail/nudges", s.api("mail:read", s.apiMailNudges))
	mux.HandleFunc("DELETE /api/v1/mail/nudges/{id}", s.api("mail:write", s.apiMailNudgeOff))
}

// mailNudgeOff is «Закрыть» of a nudge.
func (s *Server) mailNudgeOff(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/mail") {
		back = "/mail"
	}
	if err := s.Mail.DismissNudge(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// apiMailNudges: GET /api/v1/mail/nudges — {nudges: [{id, folder_id, mailbox_id, kind: received|sent, who, subject,
// sent_at, days}]}, the key owner's letters waiting for an answer (whether or not the page shows them).
func (s *Server) apiMailNudges(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ns, err := s.Mail.Nudges(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []map[string]any{}
	for _, n := range ns {
		kind := "received"
		if n.Sent {
			kind = "sent"
		}
		out = append(out, map[string]any{"id": n.ID, "folder_id": n.FolderID, "mailbox_id": n.MailboxID, "kind": kind, "who": n.Who,
			"subject": n.Subject, "sent_at": n.At, "days": n.Days})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nudges": out})
	return nil
}

// apiMailNudgeOff: DELETE /api/v1/mail/nudges/{id} — the letter is not nudged again.
func (s *Server) apiMailNudgeOff(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.Mail.DismissNudge(r.Context(), p.UserID, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
