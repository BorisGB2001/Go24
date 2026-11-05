package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// REST compatible with the portal, tasks, step 4 (GO24DEV-1014): the scrum, as tasks.api.scrum.* — the backlog, epics,
// sprints, the kanban of the active sprint (the stages of the group) and the scrum fields of a task (where it lies,
// story points, the epic). Sprints and the backlog are numbered in one row, epics in their own (migration 1014); the
// answers are in camelCase gives them. Who may plan and lead is go24's: members plan, the owner, the
// moderators and the scrum team start and complete sprints.

func init() {
	for name, m := range map[string]portalMethod{
		"tasks.api.scrum.backlog.get":        {Scope: "tasks:read", Run: (*Server).portalBacklogGet},
		"tasks.api.scrum.backlog.add":        {Scope: "tasks:write", Run: (*Server).portalBacklogAdd},
		"tasks.api.scrum.epic.add":           {Scope: "tasks:write", Run: (*Server).portalEpicAdd},
		"tasks.api.scrum.epic.get":           {Scope: "tasks:read", Run: (*Server).portalEpicGet},
		"tasks.api.scrum.epic.list":          {Scope: "tasks:read", Run: (*Server).portalEpicList},
		"tasks.api.scrum.epic.update":        {Scope: "tasks:write", Run: (*Server).portalEpicUpdate},
		"tasks.api.scrum.epic.delete":        {Scope: "tasks:write", Run: (*Server).portalEpicDelete},
		"tasks.api.scrum.epic.getfields":     {Scope: "tasks:read", Run: portalScrumFields(portalEpicFields)},
		"tasks.api.scrum.sprint.add":         {Scope: "tasks:write", Run: (*Server).portalSprintAdd},
		"tasks.api.scrum.sprint.get":         {Scope: "tasks:read", Run: (*Server).portalSprintGet},
		"tasks.api.scrum.sprint.list":        {Scope: "tasks:read", Run: (*Server).portalSprintList},
		"tasks.api.scrum.sprint.update":      {Scope: "tasks:write", Run: (*Server).portalSprintUpdate},
		"tasks.api.scrum.sprint.delete":      {Scope: "tasks:write", Run: (*Server).portalSprintDelete},
		"tasks.api.scrum.sprint.start":       {Scope: "tasks:write", Run: portalSprintStatus("active")},
		"tasks.api.scrum.sprint.complete":    {Scope: "tasks:write", Run: portalSprintStatus("completed")},
		"tasks.api.scrum.sprint.getfields":   {Scope: "tasks:read", Run: portalScrumFields(portalSprintFields)},
		"tasks.api.scrum.kanban.getstages":   {Scope: "tasks:read", Run: (*Server).portalKanbanStages},
		"tasks.api.scrum.kanban.addstage":    {Scope: "tasks:write", Run: (*Server).portalKanbanStageAdd},
		"tasks.api.scrum.kanban.updatestage": {Scope: "tasks:write", Run: (*Server).portalKanbanStageUpdate},
		"tasks.api.scrum.kanban.deletestage": {Scope: "tasks:write", Run: (*Server).portalKanbanStageDelete},
		"tasks.api.scrum.kanban.addtask":     {Scope: "tasks:write", Run: (*Server).portalKanbanTaskAdd},
		"tasks.api.scrum.kanban.deletetask":  {Scope: "tasks:write", Run: (*Server).portalKanbanTaskDelete},
		"tasks.api.scrum.kanban.getfields":   {Scope: "tasks:read", Run: portalScrumFields(portalKanbanFields)},
		"tasks.api.scrum.task.get":           {Scope: "tasks:read", Run: (*Server).portalScrumTaskGet},
		"tasks.api.scrum.task.update":        {Scope: "tasks:write", Run: (*Server).portalScrumTaskUpdate},
		"tasks.api.scrum.task.getfields":     {Scope: "tasks:read", Run: portalScrumFields(portalScrumTaskFields)},
	} {
		portalMethods[name] = m
	}
}

// The fields of getFields, name → type describes them.
var (
	portalEpicFields = [][2]string{{"id", "integer"}, {"groupId", "integer"}, {"name", "string"}, {"description", "string"},
		{"createdBy", "integer"}, {"modifiedBy", "integer"}, {"color", "string"}}
	portalSprintFields = [][2]string{{"id", "integer"}, {"groupId", "integer"}, {"entityType", "string"}, {"name", "string"},
		{"goal", "string"}, {"sort", "integer"}, {"createdBy", "integer"}, {"modifiedBy", "integer"}, {"dateStart", "string"},
		{"dateEnd", "string"}, {"status", "string"}}
	portalKanbanFields = [][2]string{{"id", "integer"}, {"name", "string"}, {"sort", "integer"}, {"type", "string"},
		{"sprintId", "integer"}, {"color", "string"}}
	portalScrumTaskFields = [][2]string{{"entityId", "integer"}, {"storyPoints", "string"}, {"epicId", "integer"},
		{"sort", "integer"}, {"createdBy", "integer"}, {"modifiedBy", "integer"}}
)

func portalScrumFields(fields [][2]string) func(*Server, portalCall) (portalOut, error) {
	return func(*Server, portalCall) (portalOut, error) {
		out := map[string]any{}
		for _, f := range fields {
			out[f[0]] = map[string]any{"type": f[1]}
		}
		return portalOut{Result: map[string]any{"fields": out}}, nil
	}
}

// portalCamelRow turns a row of RowsPage (ID, GROUP_ID) into the camelCase of the scrum methods (id, groupId).
func portalCamelRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for k, v := range row {
		out[camelPortal(k)] = v
	}
	return out
}

// portalCamelPage is portalRowsPage of rows in camelCase.
func portalCamelPage(c portalCall, rows []map[string]any) portalOut {
	out := portalRowsPage(c, rows)
	page := out.Result.([]map[string]any)
	for i, row := range page {
		page[i] = portalCamelRow(row)
	}
	return out
}

// portalScrumDay is a day of a sprint at local midnight ("" — none); a date column comes as midnight UTC.
func portalScrumDay(t *time.Time) string {
	if t == nil {
		return ""
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local).Format(time.RFC3339)
}

// portalScrumGroup is the scrum of groupId the caller sees.
func (s *Server) portalScrumGroup(c portalCall, v any) (tasks.Project, error) {
	p, err := s.portalGroupNum(c, v, false)
	if err == nil && !p.Scrum() {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.not_scrum")
	}
	return p, err
}

// portalScrumSee: the scrums the caller sees ($1 — the caller, $2 — the admin mode; p — the project).
var portalScrumSee = `p.kind = 'scrum' AND (EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
	OR $2 OR (p.privacy <> 'secret' AND NOT ` + tasks.GuestSQL("$1") + `))`

// ---------- backlog ----------

// portalBacklogNum is the number of the scrum's backlog, given on the first ask.
func (s *Server) portalBacklogNum(c portalCall, p tasks.Project) (int64, error) {
	var n int64
	err := s.DB.QueryRow(c.r.Context(), `UPDATE projects SET backlog_num = coalesce(backlog_num, nextval('scrum_entity_num'))
		WHERE id = $1 RETURNING backlog_num`, p.ID).Scan(&n)
	return n, err
}

// tasks.api.scrum.backlog.get: id — the number of the group → its backlog {id, groupId, entityType: backlog, …}.
func (s *Server) portalBacklogGet(c portalCall) (portalOut, error) {
	p, err := s.portalScrumGroup(c, portalArg(c.in, 0, "id", "groupId"))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalBacklog(c, p)
}

func (s *Server) portalBacklog(c portalCall, p tasks.Project) (portalOut, error) {
	n, err := s.portalBacklogNum(c, p)
	if err != nil {
		return portalOut{}, err
	}
	var group, owner int64
	if err := s.DB.QueryRow(c.r.Context(), `SELECT p.num, coalesce(u.num, 0) FROM projects p LEFT JOIN users u ON u.id = p.owner_id
		WHERE p.id = $1`, p.ID).Scan(&group, &owner); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"id": n, "groupId": group, "entityType": "backlog", "name": "", "goal": "", "sort": 0,
		"createdBy": owner, "modifiedBy": owner, "status": ""}}, nil
}

// tasks.api.scrum.backlog.add: fields {groupId} — a scrum of go24 always has its backlog, the method gives it.
func (s *Server) portalBacklogAdd(c portalCall) (portalOut, error) {
	p, err := s.portalScrumGroup(c, portalParam(portalMap(portalArg(c.in, 0, "fields")), "groupId"))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalBacklog(c, p)
}

// ---------- epics ----------

// portalEpics reads the epics of the scrums the caller sees, cond — more conditions on e.
func (s *Server) portalEpics(c portalCall, cond string, args ...any) ([]map[string]any, error) {
	ctx := c.r.Context()
	rows, err := s.DB.Query(ctx, `SELECT e.num, p.num, e.name, e.description, coalesce(u.num, 0), e.color
		FROM epics e JOIN projects p ON p.id = e.project_id LEFT JOIN users u ON u.id = e.created_by
		WHERE `+portalScrumSee+cond+` ORDER BY e.num`, append([]any{c.p.UserID, tasks.IsAdmin(ctx)}, args...)...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var num, group, author int64
	var name, desc, color string
	_, err = pgx.ForEachRow(rows, []any{&num, &group, &name, &desc, &author, &color}, func() error {
		out = append(out, map[string]any{"ID": num, "GROUP_ID": group, "NAME": name, "DESCRIPTION": desc, "CREATED_BY": author,
			"MODIFIED_BY": author, "COLOR": "#" + strings.ToLower(color)})
		return nil
	})
	return out, err
}

// portalEpicOf is an epic by its number: its row and its scrum.
func (s *Server) portalEpicOf(c portalCall, v any) (id string, row map[string]any, p tasks.Project, err error) {
	n := strings.TrimSpace(portalStr(v))
	list, err := s.portalEpics(c, ` AND e.num::text = $3`, n)
	if err != nil {
		return
	}
	if len(list) == 0 {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		return
	}
	var key string
	if err = s.DB.QueryRow(c.r.Context(), `SELECT e.id::text, p.key FROM epics e JOIN projects p ON p.id = e.project_id
		WHERE e.num::text = $1`, n).Scan(&id, &key); err != nil {
		return
	}
	p, err = s.Tasks.ProjectByKey(c.r.Context(), c.p.UserID, key)
	return id, list[0], p, err
}

// portalEpicOut is the epic numbered id in camelCase.
func (s *Server) portalEpicOut(c portalCall, id string) (portalOut, error) {
	_, row, _, err := s.portalEpicOf(c, s.portalNumOf(c, "epics", id))
	return portalOut{Result: portalCamelRow(row)}, err
}

// tasks.api.scrum.epic.add: fields {groupId, name, description, color} → the epic.
func (s *Server) portalEpicAdd(c portalCall) (portalOut, error) {
	f := portalMap(portalArg(c.in, 0, "fields"))
	p, err := s.portalScrumGroup(c, portalParam(f, "groupId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.Tasks.NewEpic(c.r.Context(), c.p.UserID, p, portalStr(portalParam(f, "name")), portalStr(portalParam(f, "color")),
		portalStr(portalParam(f, "description")))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalEpicOut(c, id)
}

func (s *Server) portalEpicGet(c portalCall) (portalOut, error) {
	_, row, _, err := s.portalEpicOf(c, portalArg(c.in, 0, "id"))
	return portalOut{Result: portalCamelRow(row)}, err
}

// tasks.api.scrum.epic.list: filter (GROUP_ID, NAME…), order, select, start → the epics of the scrums one sees.
func (s *Server) portalEpicList(c portalCall) (portalOut, error) {
	rows, err := s.portalEpics(c, "")
	if err != nil {
		return portalOut{}, err
	}
	return portalCamelPage(c, rows), nil
}

// tasks.api.scrum.epic.update: id, fields {name, description, color} — what is not given stays.
func (s *Server) portalEpicUpdate(c portalCall) (portalOut, error) {
	id, row, p, err := s.portalEpicOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	f := portalMap(portalArg(c.in, 1, "fields"))
	val := func(name, key string) string {
		if v, ok := portalParamOK(f, name); ok {
			return portalStr(v)
		}
		return portalStr(row[key])
	}
	if err := s.Tasks.UpdateEpic(c.r.Context(), p, id, val("name", "NAME"), val("color", "COLOR"), val("description", "DESCRIPTION")); err != nil {
		return portalOut{}, err
	}
	return s.portalEpicOut(c, id)
}

func (s *Server) portalEpicDelete(c portalCall) (portalOut, error) {
	id, _, p, err := s.portalEpicOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.DeleteEpic(c.r.Context(), p, id)
}

// ---------- sprints ----------

// portalSprints reads the sprints of the scrums the caller sees, cond — more conditions on sp. Who created an old sprint
// is not known: the owner of the group.
func (s *Server) portalSprints(c portalCall, cond string, args ...any) ([]map[string]any, error) {
	ctx := c.r.Context()
	rows, err := s.DB.Query(ctx, `SELECT sp.num, p.num, sp.name, sp.goal, sp.sort, coalesce(u.num, o.num, 0), sp.date_start,
			sp.date_end, sp.status
		FROM sprints sp JOIN projects p ON p.id = sp.project_id LEFT JOIN users u ON u.id = sp.created_by
			LEFT JOIN users o ON o.id = p.owner_id
		WHERE `+portalScrumSee+cond+` ORDER BY sp.num`, append([]any{c.p.UserID, tasks.IsAdmin(ctx)}, args...)...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var num, group, author int64
	var sort int
	var name, goal, status string
	var start, end *time.Time
	_, err = pgx.ForEachRow(rows, []any{&num, &group, &name, &goal, &sort, &author, &start, &end, &status}, func() error {
		out = append(out, map[string]any{"ID": num, "GROUP_ID": group, "ENTITY_TYPE": "sprint", "NAME": name, "GOAL": goal,
			"SORT": sort, "CREATED_BY": author, "MODIFIED_BY": author, "DATE_START": portalScrumDay(start),
			"DATE_END": portalScrumDay(end), "STATUS": status})
		return nil
	})
	return out, err
}

// portalSprintOf is a sprint by its number: its id, row and scrum.
func (s *Server) portalSprintOf(c portalCall, v any) (id string, row map[string]any, p tasks.Project, err error) {
	n := strings.TrimSpace(portalStr(v))
	list, err := s.portalSprints(c, ` AND sp.num::text = $3`, n)
	if err != nil {
		return
	}
	if len(list) == 0 {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		return
	}
	var key string
	if err = s.DB.QueryRow(c.r.Context(), `SELECT sp.id::text, p.key FROM sprints sp JOIN projects p ON p.id = sp.project_id
		WHERE sp.num::text = $1`, n).Scan(&id, &key); err != nil {
		return
	}
	p, err = s.Tasks.ProjectByKey(c.r.Context(), c.p.UserID, key)
	return id, list[0], p, err
}

func (s *Server) portalSprintOut(c portalCall, id string) (portalOut, error) {
	_, row, _, err := s.portalSprintOf(c, s.portalNumOf(c, "sprints", id))
	return portalOut{Result: portalCamelRow(row)}, err
}

// portalSprintSet applies fields {name, goal, dateStart, dateEnd, status} to a sprint: the status «active» starts it,
// «completed» completes it (the unfinished tasks go to the backlog).
func (s *Server) portalSprintSet(c portalCall, p tasks.Project, id string, row, f map[string]any) error {
	ctx := c.r.Context()
	name, goal := portalStr(row["NAME"]), portalStr(row["GOAL"])
	if v, ok := portalParamOK(f, "name"); ok {
		name = portalStr(v)
	}
	if v, ok := portalParamOK(f, "goal"); ok {
		goal = portalStr(v)
	}
	day := func(field, key string) (*time.Time, bool, error) {
		v, ok := portalParamOK(f, field)
		if !ok {
			t, err := portalDate(portalStr(row[key]))
			return t, false, err
		}
		t, err := portalDate(portalStr(v))
		if err != nil {
			err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", field)
		}
		return t, true, err
	}
	start, set1, err := day("dateStart", "DATE_START")
	if err != nil {
		return err
	}
	end, set2, err := day("dateEnd", "DATE_END")
	if err != nil {
		return err
	}
	_, set3 := portalParamOK(f, "name")
	_, set4 := portalParamOK(f, "goal")
	if set1 || set2 || set3 || set4 {
		if err := s.Tasks.UpdateSprint(ctx, p, id, name, goal, start, end); err != nil {
			return err
		}
	}
	if v, ok := portalParamOK(f, "status"); ok {
		return s.portalSprintTo(c, p, id, portalStr(row["STATUS"]), strings.ToLower(portalStr(v)))
	}
	return nil
}

// portalSprintTo moves a sprint from the status cur to want.
func (s *Server) portalSprintTo(c portalCall, p tasks.Project, id, cur, want string) error {
	switch {
	case want == cur:
		return nil
	case want == "active":
		return s.Tasks.StartSprint(c.r.Context(), p, id)
	case want == "completed":
		return s.Tasks.CompleteSprint(c.r.Context(), p, id, "")
	}
	return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "status")
}

// tasks.api.scrum.sprint.add: fields {groupId, name, goal, dateStart, dateEnd, status} → the sprint. Without dates it
// follows the last one for two weeks, as «Создать спринт».
func (s *Server) portalSprintAdd(c portalCall) (portalOut, error) {
	f := portalMap(portalArg(c.in, 0, "fields"))
	p, err := s.portalScrumGroup(c, portalParam(f, "groupId"))
	if err != nil {
		return portalOut{}, err
	}
	name := portalStr(portalParam(f, "name"))
	id, err := s.Tasks.NewSprint(c.r.Context(), c.p.UserID, p, name)
	if err != nil {
		return portalOut{}, err
	}
	_, row, _, err := s.portalSprintOf(c, s.portalNumOf(c, "sprints", id))
	if err == nil {
		err = s.portalSprintSet(c, p, id, row, f)
	}
	if err != nil {
		_ = s.Tasks.DeleteSprint(c.r.Context(), p, id) // a sprint added with wrong fields is not left behind
		return portalOut{}, err
	}
	return s.portalSprintOut(c, id)
}

// portalNumOf is the number of a row of the table by its id ("" when it is gone).
func (s *Server) portalNumOf(c portalCall, table, id string) string {
	var n string
	_ = s.DB.QueryRow(c.r.Context(), `SELECT num::text FROM `+table+` WHERE id = $1`, id).Scan(&n)
	return n
}

func (s *Server) portalSprintGet(c portalCall) (portalOut, error) {
	_, row, _, err := s.portalSprintOf(c, portalArg(c.in, 0, "id"))
	return portalOut{Result: portalCamelRow(row)}, err
}

// tasks.api.scrum.sprint.list: filter (GROUP_ID, STATUS…), order, select, start.
func (s *Server) portalSprintList(c portalCall) (portalOut, error) {
	rows, err := s.portalSprints(c, "")
	if err != nil {
		return portalOut{}, err
	}
	return portalCamelPage(c, rows), nil
}

func (s *Server) portalSprintUpdate(c portalCall) (portalOut, error) {
	id, row, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalSprintSet(c, p, id, row, portalMap(portalArg(c.in, 1, "fields"))); err != nil {
		return portalOut{}, err
	}
	return s.portalSprintOut(c, id)
}

// tasks.api.scrum.sprint.delete: a planned sprint; its tasks go back to the backlog.
func (s *Server) portalSprintDelete(c portalCall) (portalOut, error) {
	id, _, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.DeleteSprint(c.r.Context(), p, id)
}

// tasks.api.scrum.sprint.start / complete: id → the sprint.
func portalSprintStatus(want string) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		id, row, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "id"))
		if err != nil {
			return portalOut{}, err
		}
		if portalStr(row["STATUS"]) == want { // starting the active sprint again is an error, as there
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "status")
		}
		if err := s.portalSprintTo(c, p, id, portalStr(row["STATUS"]), want); err != nil {
			return portalOut{}, err
		}
		return s.portalSprintOut(c, id)
	}
}

// ---------- the kanban of a sprint ----------

// portalStageType is the type of a kanban stage: NEW — the first, FINISH — «Сделаны», WORK — the others.
func portalStageType(code string) string {
	switch code {
	case "new":
		return "NEW"
	case "finish":
		return "FINISH"
	}
	return "WORK"
}

// tasks.api.scrum.kanban.getStages: sprintId → [{id, name, sort, type, sprintId, color}]. The stages of a go24
// scrum are its group's, the same for each sprint.
func (s *Server) portalKanbanStages(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	_, row, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "sprintId"))
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Tasks.Stages(ctx, p.ID)
	if err != nil {
		return portalOut{}, err
	}
	nums, err := s.portalNums(ctx, "project_stages", stageIDs(list))
	if err != nil {
		return portalOut{}, err
	}
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := []map[string]any{}
	for i, st := range list {
		title := st.Title
		if title == "" {
			title = s.I18n.T(lang, "stage."+st.Code)
		}
		out = append(out, map[string]any{"id": nums[st.ID], "name": title, "sort": (i + 1) * 100, "type": portalStageType(st.Code),
			"sprintId": row["ID"], "color": st.Color})
	}
	return portalOut{Result: out}, nil
}

// portalKanbanStage is a stage of the sprint's group by its number.
func (s *Server) portalKanbanStage(c portalCall, v any) (portalTaskStage, error) {
	st, err := s.portalStageOf(c, v)
	if err == nil && (st.Project == nil || !st.Project.Scrum()) {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return st, err
}

// tasks.api.scrum.kanban.addStage: fields {sprintId, name, color} → the number of the stage (before «Сделаны»).
func (s *Server) portalKanbanStageAdd(c portalCall) (portalOut, error) {
	f := portalMap(portalArg(c.in, 0, "fields"))
	_, _, p, err := s.portalSprintOf(c, portalParam(f, "sprintId"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.AddProjectStage(c.r.Context(), p, portalStr(portalParam(f, "name")), portalStr(portalParam(f, "color"))); err != nil {
		return portalOut{}, err
	}
	var num int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT max(num) FROM project_stages WHERE project_id = $1`, p.ID).Scan(&num)
	return portalOut{Result: num}, err
}

// tasks.api.scrum.kanban.updateStage: stageId, fields {name, color}.
func (s *Server) portalKanbanStageUpdate(c portalCall) (portalOut, error) {
	st, err := s.portalKanbanStage(c, portalArg(c.in, 0, "stageId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	f := portalMap(portalArg(c.in, 1, "fields"))
	title, color := st.Title, st.Color
	if v, ok := portalParamOK(f, "name"); ok {
		title = portalStr(v)
	}
	if v, ok := portalParamOK(f, "color"); ok {
		color = portalStr(v)
	}
	if title == "" {
		title = s.I18n.T(s.I18n.Match(c.r.Header.Get("Accept-Language")), "stage."+st.Code)
	}
	return portalOut{Result: true}, s.Tasks.UpdateProjectStage(c.r.Context(), *st.Project, st.ID, title, color)
}

// tasks.api.scrum.kanban.deleteStage: stageId — an empty stage; «Сделаны» and the last working one stay.
func (s *Server) portalKanbanStageDelete(c portalCall) (portalOut, error) {
	st, err := s.portalKanbanStage(c, portalArg(c.in, 0, "stageId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.DeleteProjectStage(c.r.Context(), *st.Project, st.ID)
}

// tasks.api.scrum.kanban.addTask: sprintId, taskId, stageId — the task goes into the sprint; in the active one — onto
// the stage (by default the first).
func (s *Server) portalKanbanTaskAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	sid, row, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "sprintId"))
	if err != nil {
		return portalOut{}, err
	}
	tid, err := s.portalScrumTask(c, portalArg(c.in, 1, "taskId"), p)
	if err != nil {
		return portalOut{}, err
	}
	stage := strings.TrimSpace(portalStr(portalArg(c.in, 2, "stageId")))
	var st portalTaskStage
	if stage != "" && stage != "0" {
		if st, err = s.portalKanbanStage(c, stage); err != nil {
			return portalOut{}, err
		}
		if st.Project.ID != p.ID {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "stageId")
		}
	}
	if err := s.Tasks.MoveToSprint(ctx, c.p.UserID, tid, sid); err != nil {
		return portalOut{}, err
	}
	if st.ID != "" && portalStr(row["STATUS"]) == "active" {
		if err := s.Tasks.MoveToStage(ctx, c.p.UserID, tid, st.ID); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}

// tasks.api.scrum.kanban.deleteTask: sprintId, taskId — the task leaves the sprint for the backlog.
func (s *Server) portalKanbanTaskDelete(c portalCall) (portalOut, error) {
	sid, _, p, err := s.portalSprintOf(c, portalArg(c.in, 0, "sprintId"))
	if err != nil {
		return portalOut{}, err
	}
	tid, err := s.portalScrumTask(c, portalArg(c.in, 1, "taskId"), p)
	if err != nil {
		return portalOut{}, err
	}
	var in string
	if err := s.DB.QueryRow(c.r.Context(), `SELECT coalesce(sprint_id::text, '') FROM tasks WHERE id = $1`, tid).Scan(&in); err != nil {
		return portalOut{}, err
	}
	if in != sid {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return portalOut{Result: true}, s.Tasks.MoveToSprint(c.r.Context(), c.p.UserID, tid, "")
}

// ---------- the scrum fields of a task ----------

// portalScrumTask is the task numbered v the caller sees, of the scrum p (any scrum, when p.ID is "").
func (s *Server) portalScrumTask(c portalCall, v any, p tasks.Project) (string, error) {
	tid, err := s.portalTaskID(c, v)
	if err != nil {
		return "", err
	}
	var project, kind string
	if err := s.DB.QueryRow(c.r.Context(), `SELECT p.id::text, p.kind FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, tid).Scan(&project, &kind); err != nil {
		return "", err
	}
	if kind != "scrum" || p.ID != "" && project != p.ID {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.not_scrum")
	}
	return tid, nil
}

// tasks.api.scrum.task.get: id → {entityId (the sprint or the backlog), storyPoints, epicId, sort, createdBy,
// modifiedBy}.
func (s *Server) portalScrumTaskGet(c portalCall) (portalOut, error) {
	tid, err := s.portalScrumTask(c, portalArg(c.in, 0, "id", "taskId"), tasks.Project{})
	if err != nil {
		return portalOut{}, err
	}
	return s.portalScrumTaskOut(c, tid)
}

func (s *Server) portalScrumTaskOut(c portalCall, tid string) (portalOut, error) {
	ctx := c.r.Context()
	var sprint *int64
	var epic, author int64
	var sort int
	var pts *float64
	var key string
	if err := s.DB.QueryRow(ctx, `SELECT sp.num, coalesce(e.num, 0), t.backlog_sort, t.story_points::float8, coalesce(u.num, 0), p.key
		FROM tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN sprints sp ON sp.id = t.sprint_id
			LEFT JOIN epics e ON e.id = t.epic_id LEFT JOIN users u ON u.id = t.creator_id WHERE t.id = $1`, tid).
		Scan(&sprint, &epic, &sort, &pts, &author, &key); err != nil {
		return portalOut{}, err
	}
	var entity int64
	if sprint != nil {
		entity = *sprint
	} else {
		p, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, key)
		if err != nil {
			return portalOut{}, err
		}
		if entity, err = s.portalBacklogNum(c, p); err != nil {
			return portalOut{}, err
		}
	}
	sp := ""
	if pts != nil {
		sp = strconv.FormatFloat(*pts, 'f', -1, 64)
	}
	return portalOut{Result: map[string]any{"entityId": entity, "storyPoints": sp, "epicId": epic, "sort": sort,
		"createdBy": author, "modifiedBy": author}}, nil
}

// tasks.api.scrum.task.update: id, fields {entityId — a sprint or the backlog of the task's scrum, storyPoints ("" —
// none), epicId (0 — none), sort} → true.
func (s *Server) portalScrumTaskUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalScrumTask(c, portalArg(c.in, 0, "id", "taskId"), tasks.Project{})
	if err != nil {
		return portalOut{}, err
	}
	var project, key string
	if err := s.DB.QueryRow(ctx, `SELECT p.id::text, p.key FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = $1`, tid).
		Scan(&project, &key); err != nil {
		return portalOut{}, err
	}
	bad := func(field string) error {
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", field)
	}
	f := portalMap(portalArg(c.in, 1, "fields"))
	if v, ok := portalParamOK(f, "entityId"); ok {
		n := strings.TrimSpace(portalStr(v))
		var sprint string
		var backlog bool
		err := s.DB.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM sprints WHERE num::text = $1 AND project_id = $2), ''),
			EXISTS (SELECT 1 FROM projects WHERE backlog_num::text = $1 AND id = $2)`, n, project).Scan(&sprint, &backlog)
		if err != nil {
			return portalOut{}, err
		}
		if sprint == "" && !backlog {
			return portalOut{}, bad("entityId")
		}
		if err := s.Tasks.MoveToSprint(ctx, c.p.UserID, tid, sprint); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(f, "storyPoints"); ok {
		var pts *float64
		if x := strings.TrimSpace(strings.ReplaceAll(portalStr(v), ",", ".")); x != "" {
			n, err := strconv.ParseFloat(x, 64)
			if err != nil {
				return portalOut{}, bad("storyPoints")
			}
			pts = &n
		}
		if err := s.Tasks.SetStoryPoints(ctx, c.p.UserID, tid, pts); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(f, "epicId"); ok {
		epic := ""
		if n := strings.TrimSpace(portalStr(v)); n != "" && n != "0" {
			err := s.DB.QueryRow(ctx, `SELECT id::text FROM epics WHERE num::text = $1 AND project_id = $2`, n, project).Scan(&epic)
			if errors.Is(err, pgx.ErrNoRows) {
				return portalOut{}, bad("epicId")
			}
			if err != nil {
				return portalOut{}, err
			}
		}
		if err := s.Tasks.SetEpic(ctx, c.p.UserID, tid, epic); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(f, "sort"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(portalStr(v)))
		if err != nil {
			return portalOut{}, bad("sort")
		}
		if err := s.Tasks.SetScrumSort(ctx, c.p.UserID, tid, n); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}
