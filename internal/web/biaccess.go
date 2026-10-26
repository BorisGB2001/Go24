package web

import (
	"net/http"
	"slices"
	"strings"

	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/tasks"
)

// ---------- «BI-аналитика»: drafts, groups of dashboards and «Права доступа» ----------

// biRightsData puts into a page's data what the form of «Права доступа» needs: the lines of the dashboard or the
// group, the employees and departments to give rights to, the groups.
func (s *Server) biRightsData(r *http.Request, data map[string]any, on bi.On) error {
	rights, err := s.BI.Rights(r.Context(), on)
	if err != nil {
		return err
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		return err
	}
	depts, err := s.Drive.Departments(r.Context())
	if err != nil {
		return err
	}
	groups, err := s.BI.Groups(r.Context())
	if err != nil {
		return err
	}
	data["Rights"], data["Departments"], data["Groups"] = rights, depts, groups
	data["Users"] = slices.DeleteFunc(users, func(u tasks.User) bool { return u.Guest })
	data["Accesses"], data["Statuses"] = bi.Accesses, bi.Statuses
	return nil
}

// biRightsForm changes «Права доступа» of a dashboard or a group: opens it to everyone, an employee or a department
// (whom: all | user:ID | department:ID) with an access (view | edit), takes a line away (off: its id) or all of them
// (clear).
func (s *Server) biRightsForm(r *http.Request, by string, on bi.On) error {
	switch kind, id, _ := strings.Cut(r.FormValue("whom"), ":"); {
	case r.FormValue("clear") != "":
		return s.BI.ClearRights(r.Context(), on)
	case r.FormValue("off") != "":
		return s.BI.DeleteRight(r.Context(), on, r.FormValue("off"))
	default:
		return s.BI.SetRight(r.Context(), by, on, kind, id, r.FormValue("access"))
	}
}

// biPublish: the status of a dashboard (draft, published) and its group, by its author or an administrator.
func (s *Server) biPublish(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, ok := s.biManaged(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	back := "/bi/" + d.ID + "?settings=1"
	if err := s.BI.Publish(r.Context(), d.ID, r.FormValue("status"), r.FormValue("group")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#access", http.StatusSeeOther)
}

// biRights: «Права доступа» of a dashboard.
func (s *Server) biRights(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, ok := s.biManaged(w, r, sess, r.PathValue("id"))
	if !ok {
		return
	}
	back := "/bi/" + d.ID + "?settings=1"
	if err := s.biRightsForm(r, sess.UserID, bi.On{Dashboard: d.ID}); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#rights", http.StatusSeeOther)
}

// biGroups: «Группы дашбордов» — the groups, a new one; ?id= — the settings and «Права доступа» of one.
func (s *Server) biGroups(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Admin": sess.Admin, "UserID": sess.UserID}
	if id := r.URL.Query().Get("id"); id != "" {
		g, err := s.BI.Group(r.Context(), id)
		if err == nil && !g.CanManage(biViewer(sess)) {
			err = tasks.ErrForbidden
		}
		if err != nil {
			s.taskErr(w, r, err, "/bi/groups")
			return
		}
		data["G"] = g
		if err := s.biRightsData(r, data, bi.On{Group: g.ID}); err != nil {
			s.fail(w, "bi", err)
			return
		}
	} else {
		groups, err := s.BI.Groups(r.Context())
		if err != nil {
			s.fail(w, "bi", err)
			return
		}
		data["Groups"] = groups
	}
	s.render(w, r, "bigroups", page{TitleKey: "bi.groups", Nav: "bi", Data: data})
}

func (s *Server) biGroupCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.BI.SaveGroup(r.Context(), sess.UserID, "", r.FormValue("title"))
	if err != nil {
		s.taskErr(w, r, err, "/bi/groups")
		return
	}
	http.Redirect(w, r, "/bi/groups?id="+id, http.StatusSeeOther)
}

// biGroupAct: a group renamed, its «Права доступа» changed, the group deleted — by the one who made it or an
// administrator.
func (s *Server) biGroupAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	g, err := s.BI.Group(r.Context(), r.PathValue("gid"))
	if err == nil && !g.CanManage(biViewer(sess)) {
		err = tasks.ErrForbidden
	}
	if err != nil {
		s.taskErr(w, r, err, "/bi/groups")
		return
	}
	back := "/bi/groups?id=" + g.ID
	switch r.PathValue("act") {
	case "rename":
		_, err = s.BI.SaveGroup(r.Context(), sess.UserID, g.ID, r.FormValue("title"))
	case "rights":
		err = s.biRightsForm(r, sess.UserID, bi.On{Group: g.ID})
		back += "#rights"
	case "delete":
		err, back = s.BI.DeleteGroup(r.Context(), g.ID), "/bi/groups"
	default:
		err = bi.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/bi/groups?id="+g.ID)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
