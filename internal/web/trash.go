package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
)

// Task trash (Корзина): deleted tasks with who and when deleted them; restore, delete for good; administrators set
// how many days the trash keeps them.

func (s *Server) trashPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Tasks.Trash(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	days, err := s.Tasks.TrashDays(r.Context())
	if err != nil {
		s.fail(w, "trash", err)
		return
	}
	s.render(w, r, "trash", page{TitleKey: "trash.title", Nav: "trash", Data: map[string]any{
		"Items": list, "Days": days, "CanSet": sess.Admin}})
}

// taskDelete moves the task to the trash from its card.
func (s *Server) taskDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	key := r.PathValue("key")
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err == nil {
		err = s.Tasks.DeleteTask(r.Context(), sess.UserID, id)
	}
	if err != nil {
		s.taskErr(w, r, err, "/t/"+key)
		return
	}
	s.Log.Info("task deleted", "task", key, "user_id", sess.UserID)
	back := "/my" // the card goes away; a row menu of a list returns to its list
	if b, _, _ := strings.Cut(r.FormValue("back"), "#"); localPath(b) {
		back = b
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.tr(r, "trash.deleted", key)), http.StatusSeeOther)
}

func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Tasks.RestoreTask(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/tasks/trash")
		return
	}
	t, err := s.Tasks.TaskByID(r.Context(), id)
	if err != nil {
		http.Redirect(w, r, "/tasks/trash", http.StatusSeeOther)
		return
	}
	s.Log.Info("task restored", "task", t.Key(), "user_id", sess.UserID)
	http.Redirect(w, r, "/t/"+t.Key(), http.StatusSeeOther)
}

func (s *Server) trashPurge(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Tasks.PurgeTask(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/tasks/trash")
		return
	}
	s.Log.Info("task purged", "task_id", id, "user_id", sess.UserID)
	http.Redirect(w, r, "/tasks/trash", http.StatusSeeOther)
}

func (s *Server) trashSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days")))
	if err != nil {
		s.taskErr(w, r, i18n.E("err.trash_days"), "/tasks/trash")
		return
	}
	if err := s.Tasks.SetTrashDays(r.Context(), sess.UserID, days); err != nil {
		s.taskErr(w, r, err, "/tasks/trash")
		return
	}
	s.Log.Info("trash days saved", "days", days, "user_id", sess.UserID)
	http.Redirect(w, r, "/tasks/trash?msg="+url.QueryEscape(s.tr(r, "trash.saved")), http.StatusSeeOther)
}
