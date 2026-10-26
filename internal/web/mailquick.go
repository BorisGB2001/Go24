package web

import (
	"net/http"
	"net/url"
	"strconv"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// ---------- «Почта»: «Быстрые действия» ----------

// As Outlook's Quick Steps: the menu «Быстрые действия» of the toolbar and the reading pane does one of the user's
// sets of steps on the letters (do=quick:ID); /mail/settings?tab=quick&edit=ID|new makes and changes them. A quick
// step that answers opens the reply (with its template) as a draft, then does the rest.

// mailQuickSteps are the user's quick steps; Outlook's «Готово» and «Ответить и удалить» are made the first time.
func (s *Server) mailQuickSteps(r *http.Request, userID string) ([]mail.QuickStep, error) {
	return s.Mail.QuickSteps(r.Context(), userID,
		mail.QuickStep{Name: s.tr(r, "mail.qs.done"), Steps: []mail.Step{{Do: "read"}, {Do: "done"}, {Do: "archive"}}},
		mail.QuickStep{Name: s.tr(r, "mail.qs.reply_delete"), Steps: []mail.Step{{Do: "reply"}, {Do: "delete"}}})
}

// mailQuickData fills the tab of the quick steps: the list, the one being edited (edit=ID|new).
func (s *Server) mailQuickData(r *http.Request, userID string, d *mailSettingsData) error {
	var err error
	if d.QuickSteps, err = s.mailQuickSteps(r, userID); err != nil {
		return err
	}
	d.StepActions = mail.StepActions
	for i := 0; i < mail.MaxSteps; i++ {
		d.MaxSteps = append(d.MaxSteps, i)
	}
	switch id := r.URL.Query().Get("edit"); id {
	case "":
	case "new":
		d.QuickStep = &mail.QuickStep{}
	default:
		q, err := s.Mail.QuickStepOf(r.Context(), userID, id)
		if err != nil {
			return err
		}
		d.QuickStep = &q
	}
	return nil
}

// quickOfForm reads a quick step of the form: do_N — the action of step N, and what it takes — folder_N, cat_N,
// to_N, tpl_N.
func quickOfForm(r *http.Request) mail.QuickStep {
	q := mail.QuickStep{ID: r.PathValue("id"), Name: r.FormValue("name")}
	for i := 0; i < mail.MaxSteps; i++ {
		n := strconv.Itoa(i)
		st := mail.Step{Do: r.FormValue("do_" + n)}
		switch st.Do {
		case "move":
			st.Arg = r.FormValue("folder_" + n)
		case "category":
			st.Arg = r.FormValue("cat_" + n)
		case "forward":
			st.Arg = r.FormValue("to_" + n)
		case "reply", "replyall":
			st.Arg = r.FormValue("tpl_" + n)
		}
		q.Steps = append(q.Steps, st)
	}
	return q
}

// mailQuickSave makes or changes a quick step of the user.
func (s *Server) mailQuickSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := quickOfForm(r)
	if _, err := s.Mail.SaveQuickStep(r.Context(), sess.UserID, q); err != nil {
		edit := q.ID
		if edit == "" {
			edit = "new"
		}
		s.mailErr(w, r, err, settingsURL("quick", "edit", edit))
		return
	}
	http.Redirect(w, r, settingsURL("quick", "msg", s.tr(r, "common.saved")), http.StatusSeeOther)
}

// mailQuickChange: do=delete, up, down for a quick step of the user.
func (s *Server) mailQuickChange(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx, id := r.Context(), r.PathValue("id")
	var err error
	switch r.FormValue("do") {
	case "delete":
		err = s.Mail.DeleteQuickStep(ctx, sess.UserID, id)
	case "up":
		err = s.Mail.MoveQuickStep(ctx, sess.UserID, id, -1)
	case "down":
		err = s.Mail.MoveQuickStep(ctx, sess.UserID, id, 1)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.mailErr(w, r, err, settingsURL("quick"))
		return
	}
	http.Redirect(w, r, settingsURL("quick"), http.StatusSeeOther)
}

// mailQuickDo does a quick step on letters (the toolbar's do=quick:ID); one that answers opens the reply to the
// newest letter as a draft.
func (s *Server) mailQuickDo(w http.ResponseWriter, r *http.Request, sess auth.Session, ids []string, id, back string) {
	ctx := r.Context()
	q, err := s.Mail.QuickStepOf(ctx, sess.UserID, id)
	if err == nil && len(ids) == 0 {
		err = mail.ErrNotFound
	}
	if err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	folder := ""
	if u, err := url.Parse(back); err == nil {
		folder = u.Query().Get("folder")
	}
	draft := ""
	if st, ok := q.Reply(); ok {
		mode := map[string]string{"reply": "reply", "replyall": "all"}[st.Do]
		c, err := s.mailCompose(r, sess, url.Values{"letter": {ids[len(ids)-1]}, "mode": {mode}, "tpl": {st.Arg}, "folder": {folder}})
		if err == nil {
			c.Draft.HTML = c.HTML
			draft, err = s.Mail.SaveDraft(ctx, sess.UserID, c.Draft)
		}
		if err != nil {
			s.mailErr(w, r, err, back)
			return
		}
	}
	if err := s.Mail.DoQuickStep(ctx, sess.UserID, ids, q); err != nil {
		s.mailErr(w, r, err, back)
		return
	}
	s.Log.Info("mail quick step", "user_id", sess.UserID, "quick_step", q.ID, "letters", len(ids))
	switch {
	case draft != "":
		back = mailURL(folder, "", "new", "1", "draft", draft)
	case q.Files():
		back = withoutLetter(back)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
