package web

import (
	"net/http"
	"net/url"

	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/hooks"
)

// «Вебхуки» (administrators): go24's events sent to n8n and other services.
func (s *Server) webhooksPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.Hooks.List(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "webhooks", page{TitleKey: "hooks.title", Nav: "webhooks", Data: map[string]any{"Hooks": list, "Events": hooks.Events, "Base": s.BaseURL}})
}

// webhookSave: op=save (a new one with id «new»), ping, delete.
func (s *Server) webhookSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/settings/webhooks", http.StatusSeeOther)
		return
	}
	id := r.PathValue("id")
	var err error
	msg := ""
	switch r.FormValue("op") {
	case "ping":
		err = s.Hooks.Ping(r.Context(), id)
		msg = s.tr(r, "hooks.ping_queued")
	case "delete":
		var name string
		_ = s.DB.QueryRow(r.Context(), `SELECT name FROM webhooks WHERE id::text = $1`, id).Scan(&name)
		if err = s.Hooks.Delete(r.Context(), id); err == nil {
			s.audit(r, audit.Event{Kind: "hook_deleted", UserID: sess.UserID, Target: name})
		}
	default:
		if id == "new" {
			id = ""
		}
		_, err = s.Hooks.Save(r.Context(), sess.UserID, hooks.Webhook{ID: id, Name: r.FormValue("name"), URL: r.FormValue("url"),
			Events: r.Form["events"], Active: r.FormValue("active") == "on"})
		if err == nil {
			s.audit(r, audit.Event{Kind: "hook_saved", UserID: sess.UserID, Target: r.FormValue("name") + " · " + r.FormValue("url")})
		}
	}
	if err != nil {
		msg = s.errText(r, err)
	}
	back := "/settings/webhooks"
	if msg != "" {
		back += "?msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
