package web

import (
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// «Общий чат»: the settings an administrator gives the company's general chat (matrixsync/general.go).

// generalChatSettings: GET/POST /settings/general-chat — who writes in the general chat and the welcome of the new.
func (s *Server) generalChatSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.Matrix == nil {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		g := matrixsync.General{Who: r.FormValue("who"), NoHello: r.FormValue("hello") != "1"}
		if err := s.Matrix.SetGeneralSettings(ctx, sess.UserID, g); err != nil {
			s.taskErr(w, r, err, "/settings/general-chat")
			return
		}
		s.Log.Info("general chat settings", "user_id", sess.UserID, "who", g.Who, "no_hello", g.NoHello)
		http.Redirect(w, r, "/settings/general-chat?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	g, err := s.Matrix.GeneralSettings(ctx)
	if err != nil {
		s.fail(w, "general chat settings", err)
		return
	}
	room, err := s.Matrix.GeneralRoom(ctx)
	if err != nil {
		s.fail(w, "general chat room", err)
		return
	}
	s.render(w, r, "imgeneral", page{TitleKey: "imgeneral.title", Nav: "settings", Data: map[string]any{"Set": g, "Room": room}})
}

type apiGeneral struct {
	Room  string `json:"room"`
	URL   string `json:"url,omitempty"`
	Who   string `json:"who"`
	Hello bool   `json:"hello"`
}

// GET /api/v1/im/general — the general chat (its room, "" until go24 makes it) and its settings.
func (s *Server) apiGeneral(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	g, err := s.Matrix.GeneralSettings(r.Context())
	if err != nil {
		return err
	}
	room, err := s.Matrix.GeneralRoom(r.Context())
	if err != nil {
		return err
	}
	out := apiGeneral{Room: room, Who: g.Who, Hello: !g.NoHello}
	if room != "" {
		out.URL = strings.TrimRight(s.BaseURL, "/") + imRoomURL(room)
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// PATCH /api/v1/im/general {"who": "all" | "admins", "hello"} — an administrator changes the settings.
func (s *Server) apiGeneralUpdate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if err := s.needMatrix(); err != nil {
		return err
	}
	if !s.apiAdmin(r.Context(), p.UserID) {
		return tasks.ErrForbidden
	}
	g, err := s.Matrix.GeneralSettings(r.Context())
	if err != nil {
		return err
	}
	var in struct {
		Who   *string `json:"who"`
		Hello *bool   `json:"hello"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if in.Who != nil {
		g.Who = *in.Who
	}
	if in.Hello != nil {
		g.NoHello = !*in.Hello
	}
	if err := s.Matrix.SetGeneralSettings(r.Context(), p.UserID, g); err != nil {
		return err
	}
	s.Log.Info("api general chat settings", "user_id", p.UserID, "who", g.Who, "no_hello", g.NoHello)
	return s.apiGeneral(w, r, p)
}
