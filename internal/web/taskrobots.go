package web

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"go24/internal/auth"
	"go24/internal/tasks"
)

// «Роботы» of a project kanban (its owner and managers): the stages with their robots task automation.
func (s *Server) taskRobots(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	if !p.CanManage() {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	stages, err := s.Tasks.Stages(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	robots, err := s.Tasks.Robots(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	byStage := map[string][]tasks.Robot{}
	for _, rb := range robots {
		byStage[rb.StageID] = append(byStage[rb.StageID], rb)
	}
	triggers, err := s.Tasks.Triggers(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	trByStage := map[string][]tasks.Trigger{}
	for _, tr := range triggers {
		trByStage[tr.StageID] = append(trByStage[tr.StageID], tr)
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	s.render(w, r, "taskrobots", page{TitleKey: "robots.title", Nav: "projects", Data: map[string]any{
		"Project": p, "Action": "/p/" + p.Key + "/robots", "Stages": stages, "Robots": byStage, "Kinds": tasks.RobotKinds, "Statuses": tasks.RobotStatuses,
		"Users": users, "Triggers": trByStage, "TriggerKinds": tasks.TriggerKinds, "TriggerStatuses": tasks.TriggerStatuses}})
}

// taskRobotsDo adds, turns on or off, or removes a robot of the project; adds or removes a trigger.
func (s *Server) taskRobotsDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	back := "/p/" + p.Key + "/robots"
	switch r.FormValue("op") {
	case "add":
		days, _ := strconv.Atoi(r.FormValue("days"))
		delay, _ := strconv.Atoi(r.FormValue("delay"))
		to := r.FormValue("to")
		if k := r.FormValue("kind"); k == "responsible" || k == "accomplice" || k == "auditor" {
			to = r.FormValue("person")
		}
		err = s.Tasks.AddRobot(r.Context(), sess.UserID, p, r.FormValue("stage"), r.FormValue("kind"), tasks.RobotParams{
			Text: r.FormValue("text"), Details: r.FormValue("details"), To: to, Days: days, Status: tasks.Action(r.FormValue("status")),
			Stage: r.FormValue("to_stage"), Sub: r.FormValue("sub") != "", Delay: delay, Unit: r.FormValue("unit")})
	case "on", "off":
		err = s.Tasks.SetRobotActive(r.Context(), p, r.FormValue("id"), r.FormValue("op") == "on")
	case "delete":
		err = s.Tasks.DeleteRobot(r.Context(), p, r.FormValue("id"))
	case "trigger":
		err = s.Tasks.AddTrigger(r.Context(), sess.UserID, p, r.FormValue("stage"), r.FormValue("kind"), tasks.Status(r.FormValue("status")))
	case "trigger_delete":
		err = s.Tasks.DeleteTrigger(r.Context(), p, r.FormValue("id"))
	}
	if err != nil {
		back += "?msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// statusColors are the column colors of «Мои задачи» robots.
var statusColors = map[tasks.Status]string{tasks.StatusPending: "9aa5b1", tasks.StatusInProgress: "2fc6f6",
	tasks.StatusAwaitingControl: "ffa900", tasks.StatusCompleted: "7bd500", tasks.StatusDeferred: "c3c9d0"}

// «Роботы» of «Мои задачи»: the person's own robots on the task statuses, for the tasks they set.
func (s *Server) myTaskRobots(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	robots, err := s.Tasks.StatusRobots(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	byStatus := map[string][]tasks.Robot{}
	for _, rb := range robots {
		byStatus[string(rb.Status)] = append(byStatus[string(rb.Status)], rb)
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	var cols []tasks.Stage // the template's stages: a column per status
	for _, st := range tasks.RobotColumns {
		cols = append(cols, tasks.Stage{ID: string(st), Title: s.tr(r, "status."+string(st)), Color: statusColors[st]})
	}
	kinds := slices.DeleteFunc(slices.Clone(tasks.RobotKinds), func(k string) bool { return k == "stage" })
	s.render(w, r, "taskrobots", page{TitleKey: "robots.title", Nav: "taskrobots", Data: map[string]any{
		"Action": "/tasks/robots", "Stages": cols, "Robots": byStatus, "Kinds": kinds, "Statuses": tasks.RobotStatuses,
		"Users": users}})
}

// myTaskRobotsDo adds, turns on or off, or removes a robot of «Мои задачи».
func (s *Server) myTaskRobotsDo(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if tasks.IsGuestCtx(r.Context()) {
		http.NotFound(w, r)
		return
	}
	var err error
	back := "/tasks/robots"
	switch r.FormValue("op") {
	case "add":
		days, _ := strconv.Atoi(r.FormValue("days"))
		delay, _ := strconv.Atoi(r.FormValue("delay"))
		to := r.FormValue("to")
		if k := r.FormValue("kind"); k == "responsible" || k == "accomplice" || k == "auditor" {
			to = r.FormValue("person")
		}
		err = s.Tasks.AddStatusRobot(r.Context(), sess.UserID, tasks.Status(r.FormValue("stage")), r.FormValue("kind"), tasks.RobotParams{
			Text: r.FormValue("text"), Details: r.FormValue("details"), To: to, Days: days, Status: tasks.Action(r.FormValue("status")),
			Sub: r.FormValue("sub") != "", Delay: delay, Unit: r.FormValue("unit")})
	case "on", "off":
		err = s.Tasks.SetStatusRobotActive(r.Context(), sess.UserID, r.FormValue("id"), r.FormValue("op") == "on")
	case "delete":
		err = s.Tasks.DeleteStatusRobot(r.Context(), sess.UserID, r.FormValue("id"))
	}
	if err != nil {
		back += "?msg=" + url.QueryEscape(s.errText(r, err))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
