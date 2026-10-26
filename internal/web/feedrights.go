package web

import (
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// feedSettings: «Настройки ленты» of an administrator — who may write to all employees and who may
// make a post important (nobody chosen — everyone).
func (s *Server) feedSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.AdminActive() {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/feed")
		return
	}
	ctx := r.Context()
	rights, err := s.Feed.GetRights(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	users, err := s.Tasks.ListUsers(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	pick := func(a feed.Audience) []tasks.User { // the chosen as the picker's choice (a department is «dept:ID»)
		names, err2 := s.Feed.Names(ctx, a)
		if err2 != nil {
			err = err2
		}
		var out []tasks.User
		for _, rc := range names {
			u := tasks.User{ID: rc.ID, Name: rc.Name}
			if rc.Dept {
				u.ID = "dept:" + rc.ID
			}
			out = append(out, u)
		}
		return out
	}
	data := map[string]any{"Users": users, "All": pick(rights.All), "Important": pick(rights.Important)}
	if err != nil {
		s.taskErr(w, r, err, "/feed")
		return
	}
	s.render(w, r, "feedsettings", page{TitleKey: "feed.settings", Nav: "feed", Data: data})
}

func (s *Server) feedSettingsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/feed/settings"
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	audience := func(name string) feed.Audience {
		var a feed.Audience
		if r.FormValue(name+"_who") != "chosen" {
			return a
		}
		for _, v := range r.Form[name] {
			if d, ok := strings.CutPrefix(v, "dept:"); ok {
				a.Depts = append(a.Depts, d)
			} else if v != "" {
				a.Users = append(a.Users, v)
			}
		}
		return a
	}
	if err := s.Feed.SaveRights(r.Context(), sess.UserID, feed.Rights{All: audience("all"), Important: audience("important")}); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
