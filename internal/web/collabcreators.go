package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// collabCtx: the request's context with the mark of an administrator of the company (admin mode or not), for the
// rule «only administrators make collabs».
func collabCtx(ctx context.Context, sess auth.Session) context.Context {
	if sess.Admin {
		return tasks.AsAdministrator(ctx)
	}
	return ctx
}

// collabCreatorsSettings: «Коллабы» of the company's settings (administrators) — collabs on or off and
// who makes them: every employee, the chosen employees and departments, or administrators only.
func (s *Server) collabCreatorsSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	ctx := r.Context()
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			s.taskErr(w, r, err, "/settings/collabs")
			return
		}
		c := tasks.CollabCreators{Off: r.FormValue("enabled") != "1", Who: r.FormValue("who")}
		for _, v := range r.Form["creators"] {
			if d, ok := strings.CutPrefix(v, "dept:"); ok {
				c.Depts = append(c.Depts, d)
			} else if v != "" {
				c.Users = append(c.Users, v)
			}
		}
		if err := s.Tasks.SetCollabCreators(ctx, sess.UserID, c); err != nil {
			s.taskErr(w, r, err, "/settings/collabs")
			return
		}
		s.Log.Info("collab creators", "user_id", sess.UserID, "off", c.Off, "who", c.Who)
		http.Redirect(w, r, "/settings/collabs?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
		return
	}
	c, err := s.Tasks.CollabCreators(ctx)
	if err != nil {
		s.fail(w, "collab creators", err)
		return
	}
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil {
		s.fail(w, "collab creators users", err)
		return
	}
	sel, err := s.Tasks.DeptChoices(ctx, c.Depts)
	if err != nil {
		s.fail(w, "collab creators depts", err)
		return
	}
	for _, id := range c.Users {
		sel = append(sel, tasks.User{ID: id})
	}
	s.render(w, r, "collabcreators", page{TitleKey: "collab.creators.title", Nav: "settings", Data: map[string]any{
		"Set": c, "Users": users, "Sel": sel}})
}
