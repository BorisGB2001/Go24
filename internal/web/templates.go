package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Шаблоны задач» and regular tasks ----------

func (s *Server) templatesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Tasks.Templates(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "templates", page{TitleKey: "templates.title", Nav: "templates", Data: map[string]any{"Templates": list}})
}

// formProjects are the projects where the user may set tasks.
func (s *Server) formProjects(r *http.Request, userID string) ([]tasks.Project, []tasks.User, error) {
	projects, err := s.Tasks.ListProjects(r.Context(), userID)
	if err != nil {
		return nil, nil, err
	}
	var mine []tasks.Project
	for _, p := range projects {
		if p.CanCreate() && p.MyRole != "admin" {
			mine = append(mine, p)
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	return mine, users, err
}

func usersByID(users []tasks.User, ids []string) []tasks.User {
	var out []tasks.User
	for _, id := range ids {
		for _, u := range users {
			if u.ID == id {
				out = append(out, u)
			}
		}
	}
	return out
}

// templateForm fills the task form from a template (also to prefill a new task from it).
func templateForm(t tasks.Template, users []tasks.User, projects []tasks.Project) taskForm {
	f := taskForm{Template: true, TemplateID: t.ID, Title: t.Title, Description: t.Description, Responsible: t.ResponsibleID,
		Priority: t.Priority, Control: t.RequiresControl, AllowDeadline: t.AllowChangeDeadline, MatchWorkTime: t.MatchWorkTime, Params: t.Params,
		Accomplices: usersByID(users, t.Accomplices), Auditors: usersByID(users, t.Auditors), Users: users, Projects: projects,
		Project: t.ProjectKey, Checklist: strings.Join(t.Checklist, "\n"), Repeat: t.Repeat, Cancel: "/templates",
		TemplateParent: t.ParentID, TemplateSubs: len(t.Subtasks), ForNewUsers: t.ForNewUsers}
	if t.ParentID != "" {
		f.Cancel = "/templates/" + t.ParentID
	}
	f.DeadlineDays, f.DeadlineHours = t.AfterDays(), t.AfterHours()
	if f.Repeat.Period == "" {
		f.Repeat = tasks.Repeat{Period: "weekly", Every: 1, WeekDays: []int{1}, MonthDay: 1, YearMonth: 1, At: "09:00", StartsOn: time.Now()}
	}
	return f
}

// templateEdit: the form of a new template (?from=KEY copies a task, ?parent=ID makes a subtask template of that
// one) or of an existing one.
func (s *Server) templateEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	projects, users, err := s.formProjects(r, sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/templates")
		return
	}
	var t tasks.Template
	if id := r.PathValue("id"); id != "" {
		if t, err = s.Tasks.Template(r.Context(), sess.UserID, id); err != nil {
			s.taskErr(w, r, err, "/templates")
			return
		}
	} else {
		t = tasks.Template{ResponsibleID: sess.UserID, Priority: 1, ProjectKey: strings.ToUpper(r.URL.Query().Get("project"))}
		if id := r.URL.Query().Get("parent"); id != "" { // «Добавить подзадачу» of a template: in its project
			base, err := s.Tasks.Template(r.Context(), sess.UserID, id)
			if err != nil {
				s.taskErr(w, r, err, "/templates")
				return
			}
			t.ParentID, t.ParentTitle, t.ProjectKey = base.ID, base.Title, base.ProjectKey
		}
		if key := r.URL.Query().Get("from"); key != "" { // «Сохранить как шаблон» in a task card
			if id, err := s.Tasks.TaskIDByKey(r.Context(), key); err == nil {
				if c, err := s.Tasks.GetCard(r.Context(), sess.UserID, id); err == nil {
					t = templateFromCard(c)
				}
			}
		}
	}
	f := templateForm(t, users, projects)
	f.Action = "/templates/new"
	if t.ID != "" {
		f.Action = "/templates/" + t.ID
	}
	title := "templates.new"
	if t.ID != "" {
		title = "templates.edit"
	}
	data := map[string]any{"Form": f, "T": t, "Projects": projects}
	if t.ID != "" && t.ParentID == "" && t.CanEdit() { // «Права доступа» of a base template
		read, full, err := s.Tasks.TemplateAccess(r.Context(), sess.UserID, t.ID)
		if err != nil {
			s.taskErr(w, r, err, "/templates")
			return
		}
		data["Read"], data["Full"] = read, full
	}
	s.render(w, r, "template", page{TitleKey: title, Nav: "templates", Data: data})
}

// templateAccess saves «Права доступа» of a template: people and departments with «Чтение» and «Полный доступ».
func (s *Server) templateAccess(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/templates/" + url.PathEscape(r.PathValue("id"))
	err := r.ParseForm()
	if err == nil {
		err = s.Tasks.SetTemplateAccess(r.Context(), sess.UserID, r.PathValue("id"), r.Form["read"], r.Form["full"])
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func templateFromCard(c tasks.TaskCard) tasks.Template {
	t := tasks.Template{ProjectKey: c.ProjectKey, Title: c.Title, Description: c.Description, ResponsibleID: c.ResponsibleID,
		Priority: c.Priority, RequiresControl: c.RequiresControl, AllowChangeDeadline: c.AllowChangeDeadline, MatchWorkTime: c.MatchWorkTime, Params: c.Params}
	for _, u := range c.Accomplices {
		t.Accomplices = append(t.Accomplices, u.ID)
	}
	for _, u := range c.Auditors {
		t.Auditors = append(t.Auditors, u.ID)
	}
	t.Checklist = c.ChecklistLines()
	if c.Deadline != nil {
		if m := int(c.Deadline.Sub(c.CreatedAt).Minutes()); m > 0 {
			t.DeadlineAfter = &m
		}
	}
	return t
}

// parseTemplate reads the task form as a template. A new task's form has an absolute deadline: it becomes the
// offset from now (the portal does the same when a task is made regular).
func parseTemplate(r *http.Request) (tasks.Template, error) {
	if err := r.ParseForm(); err != nil {
		return tasks.Template{}, i18n.E("err.bad_form")
	}
	num := func(name string, def int) int {
		if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue(name))); err == nil {
			return v
		}
		return def
	}
	prio := num("priority", 1)
	t := tasks.Template{Title: r.FormValue("title"), Description: r.FormValue("description"), ProjectKey: r.FormValue("project"),
		ResponsibleID: r.FormValue("responsible"), Priority: tasks.Priority(prio),
		RequiresControl: r.FormValue("requires_control") == "on", AllowChangeDeadline: r.FormValue("allow_change_deadline") == "on",
		MatchWorkTime: r.FormValue("match_work_time") == "on", Params: formParams(r), Accomplices: r.Form["accomplices"], Auditors: r.Form["auditors"],
		Checklist: strings.Split(r.FormValue("checklist"), "\n"), ParentID: r.FormValue("template_parent"),
		ForNewUsers: r.FormValue("for_new_users") == "on"}
	if r.Form.Has("deadline_days") {
		if m := num("deadline_days", 0)*24*60 + num("deadline_hours", 0)*60; m > 0 {
			t.DeadlineAfter = &m
		}
	} else {
		d, err := parseDeadline(r.FormValue("deadline"))
		if err != nil {
			return t, err
		}
		if d != nil {
			if m := int(time.Until(*d).Minutes()); m > 0 {
				t.DeadlineAfter = &m
			}
		}
	}
	rep := tasks.Repeat{On: r.FormValue("repeat") == "on", Period: r.FormValue("period"), Every: num("every", 1),
		WorkdaysOnly: r.FormValue("workdays_only") == "on", MonthDay: num("month_day", 1), YearMonth: num("year_month", 1),
		At: r.FormValue("at")}
	for _, v := range r.Form["week_days"] {
		if d, err := strconv.Atoi(v); err == nil {
			rep.WeekDays = append(rep.WeekDays, d)
		}
	}
	start, err := parseDate(r.FormValue("starts_on"))
	if err != nil {
		return t, err
	}
	if start != nil {
		rep.StartsOn = *start
	}
	if rep.EndsOn, err = parseDate(r.FormValue("ends_on")); err != nil {
		return t, err
	}
	if n := num("times", 0); n > 0 {
		rep.Times = &n
	}
	t.Repeat = rep
	return t, nil
}

func (s *Server) templateSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t, err := parseTemplate(r)
	if err == nil {
		t.ID = r.PathValue("id")
		_, err = s.Tasks.SaveTemplate(r.Context(), sess.UserID, t)
	}
	if err != nil {
		s.taskErr(w, r, err, "/templates")
		return
	}
	back := "/templates"
	if t.ParentID != "" { // a subtask template: back to its base
		back += "/" + url.PathEscape(t.ParentID)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

func (s *Server) templateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/templates"
	if t, err := s.Tasks.Template(r.Context(), sess.UserID, r.PathValue("id")); err == nil && t.ParentID != "" {
		back += "/" + url.PathEscape(t.ParentID)
	}
	if err := s.Tasks.DeleteTemplate(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) templatePause(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Tasks.PauseTemplate(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("on") == "1"); err != nil {
		s.taskErr(w, r, err, "/templates")
		return
	}
	http.Redirect(w, r, "/templates", http.StatusSeeOther)
}

// templateRun makes a task from the template right now.
func (s *Server) templateRun(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t, err := s.Tasks.Template(r.Context(), sess.UserID, r.PathValue("id"))
	var key string
	if err == nil {
		key, err = s.Tasks.CreateFromTemplate(r.Context(), sess.UserID, t, time.Now())
	}
	if err != nil && key != "" { // the task is made, a subtask is not (its project is closed to the user now)
		s.Log.Warn("subtasks of a template", "template", r.PathValue("id"), "err", err)
		err = nil
	}
	if err != nil {
		s.taskErr(w, r, err, "/templates")
		return
	}
	http.Redirect(w, r, "/t/"+key, http.StatusSeeOther)
}
