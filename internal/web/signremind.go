package web

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"go24/internal/auth"
	"go24/internal/sign"
)

// ---------- «Подпись»: the signers are reminded by themselves ----------

// SignDue reminds the signers whose turn has lasted (the worker, every minute): the employee by a notification, the
// client by a letter with the link from the sender's mailbox. Only on working days, 9 to 20; the representative of an
// HR sending gets one reminder for all of its copies. Before that, the documents past their deadline are closed.
func (s *Server) SignDue(ctx context.Context, now time.Time) error {
	now = now.Local()
	if err := s.signExpire(ctx, now); err != nil {
		return err
	}
	if wd := now.Weekday(); wd == time.Saturday || wd == time.Sunday || now.Hour() < 9 || now.Hour() >= 20 {
		return nil
	}
	list, err := s.Sign.RemindDue(ctx, now, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local))
	if err != nil {
		return err
	}
	lang := s.I18n.Match("")
	batches := map[string]bool{}
	for _, m := range list {
		d, err := s.Sign.Get(ctx, m.DocID)
		if err != nil {
			return err
		}
		p := d.Party(m.PartyID)
		if p == nil {
			continue
		}
		if d.BatchID != "" && p.UserID == m.BatchSigner {
			if !batches[d.BatchID] {
				batches[d.BatchID] = true
				d.ID, d.Title = "batch/"+d.BatchID, m.BatchTitle
				s.signTell(ctx, lang, d, p, "", true)
			}
			continue
		}
		if !s.signTell(ctx, lang, d, p, "", true) {
			s.Log.Warn("sign remind not mailed", "doc", d.ID)
		}
	}
	return nil
}

// signExpire closes the documents not signed by their deadline: the sender and the observers are told.
func (s *Server) signExpire(ctx context.Context, now time.Time) error {
	ids, err := s.Sign.Expire(ctx, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		d, err := s.Sign.Get(ctx, id)
		if err != nil {
			return err
		}
		s.Log.Info("sign expired", "doc", d.ID)
		if _, err := s.DB.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'sign_expired', $2, $3)`,
			d.AuthorID, d.Title, "/sign/"+d.ID); err != nil {
			s.Log.Warn("sign notify", "err", err)
		}
		s.signTellObservers(ctx, d, "sign_expired", d.Title, "")
	}
	return nil
}

// formDeadline is «Подписать до» of a form.
func formDeadline(r *http.Request) (*time.Time, error) {
	return sign.ParseDeadline(r.FormValue("deadline"), time.Now())
}

// signDeadline: the sender changes the deadline of the document being signed (empty — none).
func (s *Server) signDeadline(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	if err == nil && !s.signEdits(r.Context(), sess, d) {
		err = sign.ErrNotFound
	}
	var deadline *time.Time
	if err == nil {
		deadline, err = formDeadline(r)
	}
	if err == nil {
		err = s.Sign.SetDeadline(r.Context(), d.ID, deadline)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("sign deadline", "user_id", sess.UserID, "doc", d.ID)
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// formRemind is «Напоминать подписанту» of a form, every day when not given.
func formRemind(r *http.Request) int {
	if v := r.FormValue("remind"); v != "" {
		return atoi(v)
	}
	return 1
}

// signRemind: the sender changes how often the signer is reminded.
func (s *Server) signRemind(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/" + r.PathValue("id")
	d, err := s.signOf(r, sess)
	if err == nil && !s.signEdits(r.Context(), sess, d) {
		err = sign.ErrNotFound
	}
	if err == nil {
		err = s.Sign.SetRemind(r.Context(), d.ID, formRemind(r))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// signTemplateDelete removes a template of sending (its author, or by the right «templates»).
func (s *Server) signTemplateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign?f=templates"
	t, err := s.Sign.Template(r.Context(), r.PathValue("id"))
	if err == nil && t.AuthorID != sess.UserID && !s.signPerms(r.Context(), sess).Has("templates", "all") {
		err = sign.ErrNotFound
	}
	if err == nil {
		err = s.Sign.DeleteTemplate(r.Context(), t.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
