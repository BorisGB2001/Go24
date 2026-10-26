package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Group actions of the task list (the portal «групповые действия»): the ticked tasks get one change each; a task the
// person may not change is skipped, and the page says how many were changed.

// bulkOps are the actions of the list's panel, in its order.
var bulkOps = []string{"start", "complete", "deadline", "responsible", "accomplice", "auditor", "tag", "favorite", "unfavorite", "mute", "unmute", "delete"}

func (s *Server) taskBulk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/tasks"
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	say := func(msg string) { http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(msg), http.StatusSeeOther) }
	if err := r.ParseForm(); err != nil {
		say(s.errText(r, i18n.E("err.bad_form")))
		return
	}
	op, keys := r.FormValue("op"), r.Form["task"]
	apply, err := s.bulkAction(r, sess, op)
	if err == nil && len(keys) == 0 {
		err = i18n.E("err.bulk_none")
	}
	if err != nil {
		say(s.errText(r, err))
		return
	}
	done, failed := 0, ""
	for _, key := range keys {
		id, err := s.Tasks.TaskIDByKey(r.Context(), key)
		if err == nil {
			err = apply(id)
		}
		if err != nil {
			if failed == "" {
				failed = key + ": " + s.errText(r, err)
			}
			continue
		}
		done++
	}
	if done > 0 {
		s.Matrix.Wake()
	}
	s.Log.Info("bulk action", "user_id", sess.UserID, "op", op, "tasks", len(keys), "done", done)
	msg := s.tr(r, "bulk.done", done, len(keys))
	if failed != "" {
		msg += " · " + failed
	}
	say(msg)
}

// errText is an error as the person reads it (an unexpected one is logged and named only in general).
func (s *Server) errText(r *http.Request, err error) string {
	var ie *i18n.Error
	if errors.As(err, &ie) {
		return s.I18n.Err(s.lang(r), ie)
	}
	s.Log.Error("bulk action", "err", err)
	return s.tr(r, "bulk.failed")
}

// bulkAction is what one op does to one task.
func (s *Server) bulkAction(r *http.Request, sess auth.Session, op string) (func(id string) error, error) {
	ctx, me, user := r.Context(), sess.UserID, r.FormValue("user_id")
	mark := func(m string, on bool) func(string) error {
		return func(id string) error { return s.Tasks.SetMark(ctx, me, id, m, on) }
	}
	switch op {
	case "start", "complete":
		return func(id string) error { return s.Tasks.ApplyAction(ctx, me, id, tasks.Action(op)) }, nil
	case "deadline":
		d, err := parseDeadline(r.FormValue("deadline"))
		if err != nil {
			return nil, err
		}
		return func(id string) error { return s.Tasks.SetDeadline(ctx, me, id, d) }, nil
	case "responsible":
		if user == "" {
			return nil, i18n.E("err.bad_form")
		}
		return func(id string) error { return s.Tasks.SetResponsible(ctx, me, id, user) }, nil
	case "accomplice", "auditor":
		if user == "" {
			return nil, i18n.E("err.bad_form")
		}
		return func(id string) error { return s.Tasks.SetParticipant(ctx, me, id, user, op, true) }, nil
	case "tag":
		tag := r.FormValue("tag")
		return func(id string) error { return s.Tasks.SetTag(ctx, me, id, tag, true) }, nil
	case "favorite", "unfavorite":
		return mark("favorite", op == "favorite"), nil
	case "mute", "unmute":
		return mark("mute", op == "mute"), nil
	case "delete": // to the trash
		return func(id string) error { return s.Tasks.DeleteTask(ctx, me, id) }, nil
	}
	return nil, i18n.E("err.bad_form")
}

// bulkBar is the panel under a task list: the page it returns to, people to choose, the actions.
type bulkBar struct {
	Back  string
	Users []tasks.User
	Ops   []string
}

func newBulkBar(r *http.Request, users []tasks.User) bulkBar {
	return bulkBar{Back: r.URL.RequestURI(), Users: users, Ops: bulkOps}
}
