package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/telephony"
)

// ---------- «Группы очередей»: employees a call waits for in a queue ----------

// groupSave adds or changes a queue group (administrators): its name, employees, how calls are given out, how long
// one employee rings and the client waits, the music of the waiting.
func (s *Server) groupSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	g := telephony.Group{ID: r.PathValue("id"), Name: r.FormValue("name"), Users: r.Form["users"], Strategy: r.FormValue("strategy"),
		DropMusic: r.FormValue("music_drop") == "1"}
	g.Ring, _ = strconv.Atoi(r.FormValue("ring"))
	g.Wait, _ = strconv.Atoi(r.FormValue("wait"))
	if f, _, err := r.FormFile("music"); err == nil {
		g.Music, err = io.ReadAll(io.LimitReader(f, 16<<20))
		f.Close()
		if err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if len(g.Music) == 0 {
			g.Music = nil
		}
	}
	if _, err := s.Telephony.SaveGroup(r.Context(), sess.UserID, g); err != nil {
		if errors.Is(err, telephony.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.taskErr(w, r, err, "/settings/telephony#groups")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#groups", http.StatusSeeOther)
}

// groupDelete removes a queue group (administrators).
func (s *Server) groupDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		s.taskErr(w, r, i18n.E("err.telephony_off"), "/settings")
		return
	}
	if err := s.Telephony.DeleteGroup(r.Context(), r.PathValue("id")); err != nil && !errors.Is(err, telephony.ErrNotFound) {
		s.taskErr(w, r, err, "/settings/telephony#groups")
		return
	}
	s.Telephony.Kick()
	http.Redirect(w, r, "/settings/telephony#groups", http.StatusSeeOther)
}

// groupMusic gives the music of a group to listen to (administrators).
func (s *Server) groupMusic(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.telAdmin(r.Context(), sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Telephony == nil {
		http.NotFound(w, r)
		return
	}
	g, err := s.Telephony.GroupMusic(r.Context(), r.PathValue("id"))
	if errors.Is(err, telephony.ErrNotFound) || (err == nil && g == nil) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, "telephony music", err)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Write(g)
}
