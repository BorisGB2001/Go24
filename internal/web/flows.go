package web

import (
	"net/http"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Flows (Задачи → Потоки): the list with each flow's counters, a flow's page with its tasks and
// the short form to put a task into it (also by link: /flows/{id}), the flow's settings, «Взять в работу».

func (s *Server) flowsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Tasks.Flows(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "flows", page{TitleKey: "flows.title", Nav: "flows", Data: map[string]any{"Flows": list}})
}

func (s *Server) flowPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f, err := s.Tasks.Flow(r.Context(), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{FlowID: f.ID, Viewer: sess.UserID})
	if err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	var tpl tasks.Template
	if f.TemplateID != "" {
		tpl, _ = s.Tasks.Template(r.Context(), f.OwnerID, f.TemplateID)
	}
	s.render(w, r, "flow", page{TitleKey: "flows.title", Nav: "flows", Data: map[string]any{
		"F": f, "Tasks": list, "Template": tpl, "Link": s.BaseURL + "/flows/" + f.ID}})
}

// flowTask puts a task into the flow from its page.
func (s *Server) flowTask(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	key, err := s.Tasks.CreateFlowTask(r.Context(), sess.UserID, id, tasks.FlowTask{Title: r.FormValue("title"),
		Description: r.FormValue("description"), Checklist: strings.Split(r.FormValue("checklist"), "\n"),
		HighPriority: r.FormValue("high") == "on"})
	if err != nil {
		s.taskErr(w, r, err, "/flows/"+id)
		return
	}
	s.Log.Info("flow task", "flow_id", id, "task", key, "user_id", sess.UserID)
	http.Redirect(w, r, "/t/"+key, http.StatusSeeOther)
}

func (s *Server) flowEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	f := tasks.Flow{Distribution: tasks.FlowManually, PlannedMinutes: 1440, CreatorsAll: true, MatchWorkTime: true, Active: true,
		OwnerID: sess.UserID, CanManage: true}
	if id := r.PathValue("id"); id != "" {
		var err error
		if f, err = s.Tasks.Flow(r.Context(), sess.UserID, id); err != nil {
			s.taskErr(w, r, err, "/flows")
			return
		}
		if !f.CanManage {
			http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
			return
		}
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	var mine []tasks.Project
	for _, p := range projects {
		if (p.CanManage() && !p.Archived) || p.ID == f.ProjectID {
			mine = append(mine, p)
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	templates, err := s.Tasks.Templates(r.Context(), f.OwnerID)
	if err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	title := "flows.new"
	if f.ID != "" {
		title = "flows.edit"
	}
	s.render(w, r, "flowedit", page{TitleKey: title, Nav: "flows", Data: map[string]any{"F": f, "Projects": mine, "Users": users,
		"Templates": templates, "PlannedHours": strconv.FormatFloat(float64(f.PlannedMinutes)/60, 'f', -1, 64)}})
}

func (s *Server) flowSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/flows/new"
	if id := r.PathValue("id"); id != "" {
		back = "/flows/" + id + "/edit"
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	hours, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(r.FormValue("planned_hours")), ",", "."), 64)
	if err != nil || hours <= 0 {
		s.taskErr(w, r, i18n.E("err.flow_planned"), back)
		return
	}
	id, err := s.Tasks.SaveFlow(r.Context(), sess.UserID, tasks.FlowInput{ID: r.PathValue("id"),
		Name: r.FormValue("name"), Description: r.FormValue("description"), ProjectKey: r.FormValue("project"),
		OwnerID: r.FormValue("owner"), ModeratorID: r.FormValue("moderator"), Distribution: r.FormValue("distribution"),
		PlannedMinutes: int(hours * 60), TemplateID: r.FormValue("template"), CreatorsAll: r.FormValue("creators_all") == "on",
		MatchWorkTime: r.FormValue("match_work_time") == "on", AllowChangeDeadline: r.FormValue("allow_change_deadline") == "on",
		RequiresControl: r.FormValue("requires_control") == "on", Active: r.FormValue("active") == "on",
		Members: r.Form["members"], Creators: r.Form["creators"]})
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("flow saved", "flow_id", id, "user_id", sess.UserID)
	http.Redirect(w, r, "/flows/"+id, http.StatusSeeOther)
}

func (s *Server) flowActive(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Tasks.SetFlowActive(r.Context(), sess.UserID, id, r.FormValue("on") == "1"); err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	http.Redirect(w, r, "/flows", http.StatusSeeOther)
}

func (s *Server) flowDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	if err := s.Tasks.DeleteFlow(r.Context(), sess.UserID, id); err != nil {
		s.taskErr(w, r, err, "/flows")
		return
	}
	s.Log.Info("flow deleted", "flow_id", id, "user_id", sess.UserID)
	http.Redirect(w, r, "/flows", http.StatusSeeOther)
}

// taskTake: «Взять в работу» — a task waiting in a «himself» flow.
func (s *Server) taskTake(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error { return s.Tasks.TakeFlowTask(r.Context(), sess.UserID, id) })
}
