package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- scrum (the portal scrum groups): planning, sprints, epics, statistics ----------

type scrumRow struct {
	tasks.Task
	Epic    *tasks.Epic
	CanMove bool
}

type scrumSprint struct {
	tasks.Sprint
	Rows []scrumRow
}

func (s *Server) scrumProject(w http.ResponseWriter, r *http.Request, sess auth.Session) (tasks.Project, bool) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return p, false
	}
	if !p.Scrum() {
		http.Redirect(w, r, "/p/"+p.Key, http.StatusFound)
		return p, false
	}
	return p, true
}

func (s *Server) rows(r *http.Request, userID string, list []tasks.Task, epics map[string]*tasks.Epic) ([]scrumRow, error) {
	roles, err := s.Tasks.RolesFor(r.Context(), userID, list)
	if err != nil {
		return nil, err
	}
	out := make([]scrumRow, len(list))
	for i, t := range list {
		rl := roles[t.ID]
		out[i] = scrumRow{Task: t, Epic: epics[t.EpicID], CanMove: rl.Worker() || rl.Manager}
	}
	return out, nil
}

func (s *Server) scrumPlan(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	plan, err := s.Tasks.ScrumPlanFor(r.Context(), sess.UserID, p)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	epics := map[string]*tasks.Epic{}
	for i := range plan.Epics {
		epics[plan.Epics[i].ID] = &plan.Epics[i]
	}
	backlog, err := s.rows(r, sess.UserID, plan.Backlog, epics)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	sprints := make([]scrumSprint, len(plan.Sprints))
	for i, sp := range plan.Sprints {
		sprints[i].Sprint = sp
		if sprints[i].Rows, err = s.rows(r, sess.UserID, sp.Tasks, epics); err != nil {
			s.taskErr(w, r, err, "/p/"+p.Key)
			return
		}
	}
	s.render(w, r, "scrum", page{Title: p.Name, Nav: "projects", Data: map[string]any{
		"Project": p, "Backlog": backlog, "Points": plan.Points, "Sprints": sprints, "Epics": plan.Epics,
		"Colors": tasks.StageColors, "Meet": s.scrumUpcoming(r, sess, p), "NextName": s.tr(r, "sprint.default_name", len(plan.Sprints)+1+s.completedCount(r, p)),
	}})
}

func (s *Server) completedCount(r *http.Request, p tasks.Project) int {
	done, err := s.Tasks.CompletedSprints(r.Context(), p)
	if err != nil {
		return 0
	}
	return len(done)
}

// scrumRowHTML renders one backlog/sprint row for a script.
func (s *Server) scrumRowHTML(r *http.Request, userID string, p tasks.Project, t tasks.Task) (string, error) {
	epicList, err := s.Tasks.Epics(r.Context(), p.ID)
	if err != nil {
		return "", err
	}
	epics := map[string]*tasks.Epic{}
	for i := range epicList {
		epics[epicList[i].ID] = &epicList[i]
	}
	rows, err := s.rows(r, userID, []tasks.Task{t}, epics)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = s.pages[s.lang(r)]["scrum"].ExecuteTemplate(&buf, "srow", map[string]any{"Row": rows[0], "Epics": epicList, "Project": p})
	return buf.String(), err
}

// taskSprint moves a task between the backlog and sprints (drag & drop; JSON answer with the row HTML).
func (s *Server) taskSprint(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.scrumTaskJSON(w, r, sess, r.PathValue("key"), func(id string) error {
		return s.Tasks.MoveToSprint(r.Context(), sess.UserID, id, r.FormValue("sprint"))
	})
}

// taskPoints sets story points ("" clears).
func (s *Server) taskPoints(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.scrumTaskJSON(w, r, sess, r.PathValue("key"), func(id string) error {
		v := strings.TrimSpace(strings.ReplaceAll(r.FormValue("points"), ",", "."))
		if v == "" {
			return s.Tasks.SetStoryPoints(r.Context(), sess.UserID, id, nil)
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return i18n.E("err.story_points")
		}
		return s.Tasks.SetStoryPoints(r.Context(), sess.UserID, id, &f)
	})
}

func (s *Server) taskEpic(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.scrumTaskJSON(w, r, sess, r.PathValue("key"), func(id string) error {
		return s.Tasks.SetEpic(r.Context(), sess.UserID, id, r.FormValue("epic"))
	})
}

// scrumTaskJSON runs a change on a task and answers a script with the updated row, or redirects a form back.
func (s *Server) scrumTaskJSON(w http.ResponseWriter, r *http.Request, sess auth.Session, key string, fn func(id string) error) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err == nil {
		err = fn(id)
	}
	if !wantsJSON(r) {
		back := r.FormValue("back")
		if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
			back = "/t/" + key
		}
		if err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	var t tasks.Task
	var p tasks.Project
	if err == nil {
		t, err = s.Tasks.TaskByID(r.Context(), id)
	}
	if err == nil {
		p, err = s.Tasks.ProjectByKey(r.Context(), sess.UserID, t.ProjectKey)
	}
	var row string
	if err == nil {
		row, err = s.scrumRowHTML(r, sess.UserID, p, t)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sprint": t.SprintID, "row": row})
}

// sprintsAction: op=create|update|delete|start|complete.
func (s *Server) sprintsAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	back := "/p/" + p.Key + "/scrum"
	id := r.FormValue("id")
	var err error
	switch r.FormValue("op") {
	case "create":
		err = s.Tasks.CreateSprint(r.Context(), p, r.FormValue("name"))
	case "update":
		var start, end *time.Time
		if start, err = parseDate(r.FormValue("date_start")); err == nil {
			if end, err = parseDate(r.FormValue("date_end")); err == nil {
				err = s.Tasks.UpdateSprint(r.Context(), p, id, r.FormValue("name"), r.FormValue("goal"), start, end)
			}
		}
	case "delete":
		err = s.Tasks.DeleteSprint(r.Context(), p, id)
	case "start":
		if err = s.Tasks.StartSprint(r.Context(), p, id); err == nil {
			back = "/p/" + p.Key + "/board"
		}
	case "complete":
		if err = s.Tasks.CompleteSprint(r.Context(), p, id, r.FormValue("move_to")); err == nil {
			back = "/p/" + p.Key + "/sprints"
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key+"/scrum")
		return
	}
	s.Log.Info("sprint "+r.FormValue("op"), "user_id", sess.UserID, "project", p.Key, "sprint", id)
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) epicsAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	var err error
	switch r.FormValue("op") {
	case "create":
		err = s.Tasks.CreateEpic(r.Context(), sess.UserID, p, r.FormValue("name"), r.FormValue("color"), r.FormValue("description"))
	case "update":
		err = s.Tasks.UpdateEpic(r.Context(), p, r.FormValue("id"), r.FormValue("name"), r.FormValue("color"), r.FormValue("description"))
	case "delete":
		err = s.Tasks.DeleteEpic(r.Context(), p, r.FormValue("id"))
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key+"/scrum")
		return
	}
	http.Redirect(w, r, "/p/"+p.Key+"/scrum#epics", http.StatusSeeOther)
}

// burnChart is the burndown as SVG geometry; colors come from CSS.
type burnChart struct {
	W, H        int
	Ideal, Real string // SVG polyline points
	Labels      []burnLabel
	Max         float64
	Empty       bool
}

type burnLabel struct {
	X    int
	Text string
}

func buildBurn(points []tasks.BurnPoint) burnChart {
	c := burnChart{W: 720, H: 220}
	if len(points) < 2 {
		c.Empty = true
		return c
	}
	for _, p := range points {
		c.Max = max(c.Max, p.Ideal, p.Remaining)
	}
	if c.Max == 0 {
		c.Max = 1
	}
	padL, padB, padT := 36, 24, 10
	w, h := c.W-padL-10, c.H-padB-padT
	x := func(i int) int { return padL + i*w/(len(points)-1) }
	y := func(v float64) int { return padT + h - int(v*float64(h)/c.Max) }
	var ideal, real []string
	step := max(1, len(points)/10)
	for i, p := range points {
		ideal = append(ideal, fmt.Sprintf("%d,%d", x(i), y(p.Ideal)))
		if !p.Future {
			real = append(real, fmt.Sprintf("%d,%d", x(i), y(p.Remaining)))
		}
		if i%step == 0 || i == len(points)-1 {
			c.Labels = append(c.Labels, burnLabel{X: x(i), Text: p.Day.Format("02.01")})
		}
	}
	c.Ideal, c.Real = strings.Join(ideal, " "), strings.Join(real, " ")
	return c
}

// sprintsDone lists completed sprints with statistics; the active sprint's burndown is on top.
func (s *Server) sprintsDone(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	done, err := s.Tasks.CompletedSprints(r.Context(), p)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	active, err := s.Tasks.ActiveSprint(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	type withChart struct {
		tasks.Sprint
		Chart burnChart
	}
	var list []withChart
	var activeChart *withChart
	if active != nil {
		pts, err := s.Tasks.Burndown(r.Context(), active.ID)
		if err != nil {
			s.taskErr(w, r, err, "/p/"+p.Key)
			return
		}
		activeChart = &withChart{Sprint: *active, Chart: buildBurn(pts)}
	}
	for _, sp := range done {
		pts, err := s.Tasks.Burndown(r.Context(), sp.ID)
		if err != nil {
			s.taskErr(w, r, err, "/p/"+p.Key)
			return
		}
		list = append(list, withChart{Sprint: sp, Chart: buildBurn(pts)})
	}
	s.render(w, r, "sprints", page{Title: p.Name, Nav: "projects", Data: map[string]any{
		"Project": p, "Done": list, "Active": activeChart, "Velocity": buildVelocity(done),
	}})
}

// velocityChart is «Скорость команды»: planned and done story points of the last completed sprints, oldest first,
// and the average done.
type velocityChart struct {
	W, H    int
	Bars    []velocityBar
	Max     float64
	Average float64
	AvgY    int
}

type velocityBar struct {
	X, W          int
	PlanY, DoneY  int // tops of the bars
	PlanH, DoneH  int
	LabelX        int
	Name          string
	Planned, Done float64
}

const velocitySprints = 10

func buildVelocity(done []tasks.Sprint) *velocityChart {
	if len(done) == 0 {
		return nil
	}
	list := done[:min(len(done), velocitySprints)] // newest first
	c := &velocityChart{W: 720, H: 220}
	sum := 0.0
	for _, sp := range list {
		c.Max = max(c.Max, sp.Points, sp.Done)
		sum += sp.Done
	}
	c.Average = sum / float64(len(list))
	if c.Max == 0 {
		c.Max = 1
	}
	padL, padB, padT := 36, 24, 10
	w, h := c.W-padL-10, c.H-padB-padT
	slot := w / len(list)
	y := func(v float64) int { return padT + h - int(v*float64(h)/c.Max) }
	for i := range list {
		sp := list[len(list)-1-i]
		b := velocityBar{X: padL + i*slot + slot/6, W: slot * 2 / 3, PlanY: y(sp.Points), DoneY: y(sp.Done),
			Name: sp.Name, Planned: sp.Points, Done: sp.Done}
		b.PlanH, b.DoneH, b.LabelX = padT+h-b.PlanY, padT+h-b.DoneY, b.X+b.W/2
		c.Bars = append(c.Bars, b)
	}
	c.AvgY = y(c.Average)
	return c
}

// scrumTeamPage: «Команда и DoD» — the product owner, the scrum master, the team, and the task types with their
// Definition of Done.
func (s *Server) scrumTeamPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	if err := s.Tasks.EnsureScrumTypes(r.Context(), p, s.tr(r, "dod.type_task"), s.tr(r, "dod.type_bug")); err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	team, err := s.Tasks.ScrumTeamOf(r.Context(), p)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	types, err := s.Tasks.ScrumTypes(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key)
		return
	}
	s.render(w, r, "scrumteam", page{Title: p.Name, Nav: "projects", Data: map[string]any{
		"Project": p, "Team": team, "Types": types,
	}})
}

// scrumTeamAction: op=team|type|type_delete.
func (s *Server) scrumTeamAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, ok := s.scrumProject(w, r, sess)
	if !ok {
		return
	}
	back := "/p/" + p.Key + "/dod"
	var err error
	switch r.FormValue("op") {
	case "team":
		err = s.Tasks.SetScrumTeam(r.Context(), p, r.FormValue("owner"), r.FormValue("master"))
	case "type":
		err = s.Tasks.SaveScrumType(r.Context(), p, r.FormValue("id"), r.FormValue("name"), r.FormValue("required") == "on",
			strings.Split(r.FormValue("items"), "\n"))
		back += "#dod"
	case "type_delete":
		err = s.Tasks.DeleteScrumType(r.Context(), p, r.FormValue("id"))
		back += "#dod"
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/p/"+p.Key+"/dod")
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// taskDod sets the task's type and ticks its Definition of Done; «Завершить» completes the task after that.
func (s *Server) taskDod(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/t/"+r.PathValue("key"))
		return
	}
	s.taskPost(w, r, "#dod", func(id string) error {
		if err := s.Tasks.SetTaskDod(r.Context(), sess.UserID, id, r.FormValue("type"), r.Form["item"]); err != nil {
			return err
		}
		if r.FormValue("complete") == "" {
			return nil
		}
		return s.Tasks.ApplyAction(r.Context(), sess.UserID, id, tasks.ActionComplete)
	})
}
