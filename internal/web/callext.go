package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- External services of calls: Zoom, Яндекс Телемост, VK Звонки, Telegram, Max ----------

// extErr is an error of the services as the person sees it.
func extErr(err error) error {
	switch {
	case errors.Is(err, calls.ErrNoService):
		return i18n.E("call.ext_unavailable")
	case errors.Is(err, calls.ErrBadLink):
		return i18n.E("call.ext_bad_link")
	}
	return err
}

// callServiceSave: the administrators switch a service on or off and give its keys (a secret left empty stays).
func (s *Server) callServiceSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if s.External == nil {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	c := calls.ServiceConfig{Service: r.PathValue("service"), Enabled: r.FormValue("enabled") == "1",
		AccountID: r.FormValue("account_id"), ClientID: r.FormValue("client_id"), GroupID: r.FormValue("group_id")}
	if err := s.External.SaveConfig(r.Context(), sess.UserID, c, r.FormValue("secret"), r.FormValue("clear") == "1"); err != nil {
		s.taskErr(w, r, extErr(err), "/settings/calls")
		return
	}
	s.Log.Info("call service", "user_id", sess.UserID, "service", c.Service, "enabled", c.Enabled)
	http.Redirect(w, r, "/settings/calls?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#svc-"+c.Service, http.StatusSeeOther)
}

// callLinksPage is «Мои видеосервисы»: the employee's own links of the services the administrators let use.
func (s *Server) callLinksPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if s.External == nil || tasks.IsGuestCtx(r.Context()) {
		s.taskErr(w, r, i18n.E("err.calls_off"), "/my")
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/call-links")
			return
		}
		for _, svc := range calls.Services {
			if !r.PostForm.Has(svc) { // the services shown are the ones sent
				continue
			}
			if err := s.External.SetLink(ctx, sess.UserID, svc, r.PostForm.Get(svc)); err != nil {
				s.taskErr(w, r, extErr(err), "/settings/call-links")
				return
			}
		}
		http.Redirect(w, r, "/settings/call-links?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	all, err := s.External.Configs(ctx)
	if err != nil {
		s.fail(w, "call services", err)
		return
	}
	links, err := s.External.Links(ctx, sess.UserID)
	if err != nil {
		s.fail(w, "call links", err)
		return
	}
	var rows []map[string]any
	for _, c := range all {
		if c.Enabled {
			rows = append(rows, map[string]any{"Service": c.Service, "API": c.Ready(), "Link": links[c.Service]})
		}
	}
	s.render(w, r, "calllinks", page{TitleKey: "call.ext_my", Nav: "calllinks", Data: map[string]any{"Rows": rows}})
}

// callExternal: GET — the services the employee may make a meeting with; POST service=… makes one (by the
// service's API, or the employee's own link). With a call's place (task=, room=) the invitation goes there: a
// comment of the task, a message of the chat; without — only the link comes back (it goes into a form: the place of
// a meeting of the calendar, a CRM deal's activity).
func (s *Server) callExternal(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	if s.External == nil || tasks.IsGuestCtx(ctx) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"services": []any{}})
			return
		}
		s.jsonErr(w, r, i18n.E("call.ext_unavailable"))
		return
	}
	if r.Method == http.MethodGet {
		list, err := s.External.Available(ctx, sess.UserID)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		out := []map[string]any{}
		for _, a := range list {
			out = append(out, map[string]any{"service": a.Service, "name": s.tr(r, "call.svc."+a.Service), "api": a.API})
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]any{"services": out})
		return
	}
	q := r.URL.Query()
	place := q.Get("task") != "" || q.Get("room") != ""
	title := strings.TrimSpace(r.FormValue("title"))
	var announce func(string) error
	if place {
		_, t, _, a, err := s.callRoom(r, sess)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		title, announce = t, a
	}
	if title == "" {
		title = s.tr(r, "call.title")
	}
	service := r.FormValue("service")
	m, err := s.External.Meet(ctx, sess.UserID, sess.Email, service, title)
	if err != nil {
		if !errors.Is(err, calls.ErrNoService) {
			s.Log.Error("call service meeting", "service", service, "err", err)
			err = i18n.E("call.ext_failed", s.tr(r, "call.svc."+service))
		}
		s.jsonErr(w, r, extErr(err))
		return
	}
	text := s.tr(r, "call.ext_invite", sess.Name, s.tr(r, "call.svc."+service), m.URL)
	if announce != nil {
		if err := announce(text); err != nil {
			s.jsonErr(w, r, err)
			return
		}
	}
	s.Log.Info("call service meeting", "user_id", sess.UserID, "service", service, "own", m.Own)
	writeJSON(w, http.StatusOK, map[string]any{"url": m.URL, "text": text, "posted": announce != nil})
}
