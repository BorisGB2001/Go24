package web

import (
	"errors"
	"net/http"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// Joining a group: «Вступить» into an open group, a request to a closed one, its moderators accept or
// reject requests.

// groupOutsider shows an open or closed group to an employee who is not in it: its card with «Вступить» or
// «Отправить запрос». It reports whether it did.
func (s *Server) groupOutsider(w http.ResponseWriter, r *http.Request, sess auth.Session, p tasks.Project, err error) bool {
	if !errors.Is(err, tasks.ErrForbidden) {
		return false
	}
	g, err := s.Tasks.GroupForOutsider(r.Context(), p, sess.UserID)
	if err != nil {
		return false
	}
	s.render(w, r, "groupcard", page{Title: p.Name, Nav: "projects", Data: g})
	return true
}

// groupJoin: «Вступить» (open) or «Отправить запрос» (closed, with a message).
func (s *Server) groupJoin(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if errors.Is(err, tasks.ErrForbidden) || (err == nil && p.MyRole == "admin") {
		var joined bool
		if joined, err = s.Tasks.JoinGroup(r.Context(), p, sess.UserID, r.FormValue("message")); err == nil && joined {
			s.Log.Info("group joined", "user_id", sess.UserID, "project", p.Key)
			s.Matrix.Wake() // the new member joins the project room
		}
	} else if err == nil {
		err = tasks.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// groupJoinCancel: «Отменить запрос».
func (s *Server) groupJoinCancel(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if errors.Is(err, tasks.ErrForbidden) || err == nil {
		err = s.Tasks.CancelJoinRequest(r.Context(), p, sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// groupRequest: a moderator accepts (accept=1) or rejects a request to join.
func (s *Server) groupRequest(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	accept := r.FormValue("accept") == "1"
	if err == nil {
		err = s.Tasks.AnswerJoinRequest(r.Context(), p, sess.UserID, r.PathValue("uid"), accept)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("group join request", "user_id", sess.UserID, "project", p.Key, "member", r.PathValue("uid"), "accept", accept)
	if accept {
		s.Matrix.Wake()
	}
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}
