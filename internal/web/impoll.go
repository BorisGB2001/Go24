package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/matrixsync"
)

// imPoll starts a poll in the chat (or its thread): the question, the answers, «Несколько вариантов», «Итоги после
// завершения».
func (s *Server) imPoll(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	m, err := s.Matrix.CreatePoll(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("thread"), r.FormValue("question"),
		r.Form["answer"], r.FormValue("multi") == "1", r.FormValue("hidden") == "1")
	s.pollAnswer(w, r, m, err, http.StatusCreated)
}

// imPollAction votes (vote: the answers chosen, none — the vote is taken back) or ends one's own poll (end); the
// answer is the poll as it is now.
func (s *Server) imPollAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_ = r.ParseForm()
	var m matrixsync.Message
	var err error
	switch r.PathValue("action") {
	case "vote":
		m, err = s.Matrix.Vote(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.Form["answer"])
	case "end":
		m, err = s.Matrix.EndPoll(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), s.tr(r, "im.poll_ended"))
	default:
		http.NotFound(w, r)
		return
	}
	s.pollAnswer(w, r, m, err, http.StatusOK)
}

func (s *Server) pollAnswer(w http.ResponseWriter, r *http.Request, m matrixsync.Message, err error, status int) {
	var html string
	if err == nil {
		html, err = s.imFragment(r, "immsg", m)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, status, map[string]string{"id": m.ID, "html": html})
}
