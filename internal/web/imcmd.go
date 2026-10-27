package web

import (
	"net/http"

	"go24/internal/auth"
)

// imCommands are the commands of the chat's bots the person may use, for the list shown on «/» in the field.
func (s *Server) imCommands(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Matrix.ChatCommands(r.Context(), sess.UserID, r.URL.Query().Get("room"), s.lang(r))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// imButton: a button of a bot's message (an item of its menu with menu=1) that runs its command was pressed.
func (s *Server) imButton(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Matrix.PressButton(r.Context(), sess.UserID, r.FormValue("room"), r.FormValue("event"), r.FormValue("command"),
		r.FormValue("params"), r.FormValue("menu") == "1"); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
