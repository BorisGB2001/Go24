package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal, tasks (GO24DEV-820): tasks.task.* (getFields, list, get, add, update, delete, the
// actions start, pause, complete, defer, renew, approve, disapprove, delegate, favorite and mute), task.commentitem.*,
// task.checklistitem.* and department.get. Tasks, groups (projects), comments and checklist items have numbers
// (migration 662). A task is given gives it — {"task": {"id": "1", "responsibleId": "3", …}}: the keys of
// select and filter in capitals, the answer's in camelCase. Changes go the way of go24: the same rights and events.

var portalTask = portalEntity{Entity: "task", Kind: "tasks",
	From: `tasks x JOIN projects p ON p.id = x.project_id JOIN users uc ON uc.id = x.creator_id
		JOIN users ur ON ur.id = x.responsible_id LEFT JOIN users ul ON ul.id = x.closed_by
		LEFT JOIN tasks_all xp ON xp.id = x.parent_id`,
	Fields: []portalField{
		{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
		{Name: "PARENT_ID", Type: "integer", Col: "xp.num"},
		{Name: "TITLE", Type: "string", Col: "x.title", Required: true},
		{Name: "DESCRIPTION", Type: "string", Col: "x.description"},
		{Name: "MARK", Type: "enum", Col: "CASE x.rating WHEN 'positive' THEN 'P' WHEN 'negative' THEN 'N' END"},
		{Name: "PRIORITY", Type: "enum", Col: "x.priority::text"},
		{Name: "STATUS", Type: "enum", Col: portalTaskStatus, ReadOnly: true},
		{Name: "REAL_STATUS", Type: "enum", Col: portalTaskStatus, ReadOnly: true},
		{Name: "MULTITASK", Type: "char", Col: "'N'", ReadOnly: true},
		{Name: "GROUP_ID", Type: "integer", Col: "p.num"},
		{Name: "CREATED_BY", Type: "user", Col: "uc.num", ReadOnly: true},
		{Name: "CREATED_DATE", Type: "datetime", Col: "x.created_at", ReadOnly: true},
		{Name: "RESPONSIBLE_ID", Type: "user", Col: "ur.num"},
		{Name: "CHANGED_DATE", Type: "datetime", Col: "x.updated_at", ReadOnly: true},
		{Name: "CLOSED_BY", Type: "user", Col: "ul.num", ReadOnly: true},
		{Name: "CLOSED_DATE", Type: "datetime", Col: "x.closed_at", ReadOnly: true},
		{Name: "DEADLINE", Type: "datetime", Col: "x.deadline"},
		{Name: "START_DATE_PLAN", Type: "date", Col: "x.start_plan"},
		{Name: "END_DATE_PLAN", Type: "date", Col: "x.end_plan"},
		{Name: "ALLOW_CHANGE_DEADLINE", Type: "char", Col: "CASE WHEN x.allow_change_deadline THEN 'Y' ELSE 'N' END"},
		{Name: "MATCH_WORK_TIME", Type: "char", Col: "CASE WHEN x.match_work_time THEN 'Y' ELSE 'N' END"},
		{Name: "TASK_CONTROL", Type: "char", Col: "CASE WHEN x.requires_control THEN 'Y' ELSE 'N' END"},
		{Name: "ALLOW_TIME_TRACKING", Type: "char", Col: "CASE WHEN x.no_time_tracking THEN 'N' ELSE 'Y' END"},
		{Name: "TIME_ESTIMATE", Type: "integer", Col: "(coalesce(x.estimate_minutes, 0) * 60)::bigint"},
		{Name: "TIME_SPENT_IN_LOGS", Type: "integer", ReadOnly: true,
			Col: "(SELECT coalesce(sum(e.seconds), 0) FROM time_entries e WHERE e.task_id = x.id)::bigint"},
		{Name: "COMMENTS_COUNT", Type: "integer", ReadOnly: true,
			Col: "(SELECT count(*) FROM task_comments m WHERE m.task_id = x.id)"},
		{Name: "ACCOMPLICES", Type: "user", Multiple: true, Col: `(SELECT coalesce(array_agg(u.num ORDER BY u.num), '{}')
			FROM task_participants tp JOIN users u ON u.id = tp.user_id WHERE tp.task_id = x.id AND tp.role = 'accomplice')`},
		{Name: "AUDITORS", Type: "user", Multiple: true, Col: `(SELECT coalesce(array_agg(u.num ORDER BY u.num), '{}')
			FROM task_participants tp JOIN users u ON u.id = tp.user_id WHERE tp.task_id = x.id AND tp.role = 'auditor')`},
		{Name: "TAGS", Type: "string", Multiple: true,
			Col: "(SELECT coalesce(array_agg(g.tag ORDER BY g.tag), '{}') FROM task_tags g WHERE g.task_id = x.id)"},
	},
}

// portalTaskStatus is the status: 2 pending, 3 in progress, 4 awaiting control, 5 completed, 6 deferred.
const portalTaskStatus = `CASE x.status WHEN 'pending' THEN '2' WHEN 'in_progress' THEN '3' WHEN 'awaiting_control' THEN '4'
	WHEN 'completed' THEN '5' WHEN 'deferred' THEN '6' END`

// portalTaskEnums are the values of the enum fields, as tasks.task.getFields gives them (locale keys).
var portalTaskEnums = map[string][]string{"PRIORITY": {"0", "1", "2"}, "STATUS": {"2", "3", "4", "5", "6"},
	"REAL_STATUS": {"2", "3", "4", "5", "6"}, "MARK": {"P", "N"}}

func init() {
	for name, m := range map[string]portalMethod{
		"tasks.task.getfields":        {Scope: "tasks:read", Run: (*Server).portalTaskFields},
		"tasks.task.list":             {Scope: "tasks:read", Run: (*Server).portalTaskList},
		"tasks.task.get":              {Scope: "tasks:read", Run: (*Server).portalTaskGet},
		"tasks.task.add":              {Scope: "tasks:write", Run: (*Server).portalTaskAdd},
		"tasks.task.update":           {Scope: "tasks:write", Run: (*Server).portalTaskUpdate},
		"tasks.task.delete":           {Scope: "tasks:write", Run: (*Server).portalTaskDelete},
		"tasks.task.delegate":         {Scope: "tasks:write", Run: (*Server).portalTaskDelegate},
		"tasks.task.favorite.add":     {Scope: "tasks:write", Run: portalTaskMark("favorite", true)},
		"tasks.task.favorite.remove":  {Scope: "tasks:write", Run: portalTaskMark("favorite", false)},
		"tasks.task.mute":             {Scope: "tasks:write", Run: portalTaskMark("mute", true)},
		"tasks.task.unmute":           {Scope: "tasks:write", Run: portalTaskMark("mute", false)},
		"task.commentitem.getlist":    {Scope: "tasks:read", Run: (*Server).portalCommentList},
		"task.commentitem.get":        {Scope: "tasks:read", Run: (*Server).portalCommentGet},
		"task.commentitem.add":        {Scope: "tasks:write", Run: (*Server).portalCommentAdd},
		"task.commentitem.update":     {Scope: "tasks:write", Run: (*Server).portalCommentUpdate},
		"task.commentitem.delete":     {Scope: "tasks:write", Run: (*Server).portalCommentDelete},
		"task.checklistitem.getlist":  {Scope: "tasks:read", Run: (*Server).portalCheckList},
		"task.checklistitem.get":      {Scope: "tasks:read", Run: (*Server).portalCheckGet},
		"task.checklistitem.add":      {Scope: "tasks:write", Run: (*Server).portalCheckAdd},
		"task.checklistitem.update":   {Scope: "tasks:write", Run: (*Server).portalCheckUpdate},
		"task.checklistitem.complete": {Scope: "tasks:write", Run: portalCheckDone(true)},
		"task.checklistitem.renew":    {Scope: "tasks:write", Run: portalCheckDone(false)},
		"task.checklistitem.delete":   {Scope: "tasks:write", Run: (*Server).portalCheckDelete},
		"department.get":              {Scope: "company:read", Run: (*Server).portalDepartmentGet},
	} {
		portalMethods[name] = m
	}
	for _, a := range []tasks.Action{tasks.ActionStart, tasks.ActionPause, tasks.ActionComplete, tasks.ActionDefer,
		tasks.ActionRenew, tasks.ActionApprove, tasks.ActionDisapprove} {
		portalMethods["tasks.task."+string(a)] = portalMethod{Scope: "tasks:write", Run: portalTaskAction(a)}
	}
}

// portalArg is a parameter by one of its names in any case, or by its place (the old task.* methods take [taskId, …]).
func portalArg(in map[string]any, place int, names ...string) any {
	for _, n := range names {
		if v, ok := portalParamOK(in, n); ok {
			return v
		}
	}
	return in[strconv.Itoa(place)]
}

// camelPortal is the key of a task's field in the answer: RESPONSIBLE_ID → responsibleId.
func camelPortal(name string) string {
	parts := strings.Split(strings.ToLower(name), "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// upperPortal is the key of a field as given — responsibleId or RESPONSIBLE_ID — in capitals: RESPONSIBLE_ID.
func upperPortal(key string) string {
	if strings.ToUpper(key) == key {
		return key
	}
	var b strings.Builder
	for i, r := range key {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// ---------- tasks.task: reading ----------

// portalTaskEntity is a task with the own fields the caller sees (UF_*), after the built-in ones.
func (s *Server) portalTaskEntity(c portalCall) (portalEntity, error) {
	sight, err := s.portalSight(c)
	if err != nil {
		return portalEntity{}, err
	}
	return s.portalWithUF(c.r.Context(), portalTask, sight)
}

// tasks.task.getFields: {"fields": {ID: {title, type, …}}}, the own fields with their names.
func (s *Server) portalTaskFields(c portalCall) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	e, err := s.portalTaskEntity(c)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for _, f := range e.Fields {
		if f.UF != nil {
			out[f.Name] = map[string]any{"title": f.UF.Name, "type": f.Type, "isUf": true}
			continue
		}
		typ := f.Type
		switch {
		case f.Multiple:
			typ = "array"
		case typ == "user":
			typ = "integer"
		}
		d := map[string]any{"title": s.I18n.T(lang, "portal.task."+f.Name), "type": typ}
		if f.Name == "ID" {
			d["primary"] = true
		}
		if f.Required {
			d["required"] = true
		}
		if vals, ok := portalTaskEnums[f.Name]; ok {
			vs := map[string]string{}
			for _, v := range vals {
				vs[v] = s.I18n.T(lang, "portal.task."+f.Name+"."+v)
			}
			d["values"] = vs
		}
		out[f.Name] = d
	}
	return portalOut{Result: map[string]any{"fields": out}}, nil
}

// portalTaskSelect are the fields to give: select[] (ID always); none or «*» — all the built-in ones, the own fields —
// by name or all of them with «UF_*».
func portalTaskSelect(e portalEntity, sel []string) []portalField {
	if len(sel) == 0 || slices.Contains(sel, "*") {
		return portalTask.Fields
	}
	uf := slices.ContainsFunc(sel, func(x string) bool { return strings.EqualFold(x, "UF_*") })
	var out []portalField
	for _, f := range e.Fields {
		if f.Name == "ID" || f.UF != nil && uf || slices.ContainsFunc(sel, func(x string) bool { return strings.EqualFold(upperPortal(x), f.Name) }) {
			out = append(out, f)
		}
	}
	return out
}

// portalTaskWhere adds the filter keys a task has beyond its fields: MEMBER, ACCOMPLICE, AUDITOR (numbers of employees),
// TAG; STATUS -1 — overdue. The rest goes to portalWhere.
func portalTaskWhere(filter map[string]any, arg func(any) string) (map[string]any, []string) {
	rest := map[string]any{}
	var where []string
	for k, v := range filter {
		op := ""
		for _, o := range portalOps {
			if strings.HasPrefix(k, o) {
				op = o
				break
			}
		}
		neg := strings.HasPrefix(op, "!")
		var cond string
		switch key := upperPortal(k[len(op):]); key {
		case "MEMBER":
			if op != "" && op != "!" && op != "=" && op != "@" {
				continue
			}
			cond = `EXISTS (SELECT 1 FROM users u WHERE u.num::text = ANY(` + arg(portalList(v)) + `::text[]) AND (u.id IN (x.creator_id,
				x.responsible_id) OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = x.id AND tp.user_id = u.id)))`
		case "ACCOMPLICE", "AUDITOR":
			cond = `EXISTS (SELECT 1 FROM task_participants tp JOIN users u ON u.id = tp.user_id WHERE tp.task_id = x.id
				AND tp.role = '` + strings.ToLower(key) + `' AND u.num::text = ANY(` + arg(portalList(v)) + `::text[]))`
		case "TAG":
			cond = `EXISTS (SELECT 1 FROM task_tags g WHERE g.task_id = x.id AND lower(g.tag) = ANY(` + arg(lowerAll(portalList(v))) + `::text[]))`
		case "STATUS", "REAL_STATUS":
			if !slices.Contains(portalList(v), "-1") {
				rest[k] = v
				continue
			}
			cond = `(x.status <> 'completed' AND x.deadline < now())`
		default:
			rest[op+key] = v // responsibleId reads as RESPONSIBLE_ID
			continue
		}
		if neg {
			cond = "NOT " + cond
		}
		where = append(where, cond)
	}
	return rest, where
}

// tasks.task.list: filter, order, select, start — {"tasks": [...]} of the tasks the caller sees, 50 a page.
func (s *Server) portalTaskList(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	args := []any{c.p.UserID, tasks.IsAdmin(ctx)}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	e, err := s.portalTaskEntity(c)
	if err != nil {
		return portalOut{}, err
	}
	filter, where := portalTaskWhere(portalMap(portalParam(c.in, "filter")), arg)
	more, err := s.portalWhere(ctx, e, filter, arg)
	if err != nil {
		return portalOut{}, err
	}
	cond := strings.Join(append([]string{portalTaskVisible}, append(where, more...)...), " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM `+portalTask.From+` WHERE `+cond, args...).Scan(&total); err != nil {
		return portalOut{}, err
	}
	order := map[string]any{}
	for k, v := range portalMap(portalParam(c.in, "order")) {
		order[upperPortal(k)] = v
	}
	start := portalStart(c.in)
	list, err := s.portalTasks(ctx, cond, portalOrder(e, order), args, start, portalPage, portalTaskSelect(e, portalList(portalParam(c.in, "select"))))
	if err != nil {
		return portalOut{}, err
	}
	return portalPaged(map[string]any{"tasks": list}, start, len(list), total), nil
}

// portalTaskVisible is the condition «the caller ($1) sees the task x» ($2 — admin mode).
var portalTaskVisible = `EXISTS (SELECT 1 FROM tasks t WHERE t.id = x.id AND ` + tasks.VisibleSQL("$1", "$2") + `)`

// portalTasks reads tasks gives them: the keys in camelCase, and with the people and the group — creator,
// responsible, group {id, name}.
func (s *Server) portalTasks(ctx context.Context, cond, order string, args []any, start, limit int, sel []portalField) ([]map[string]any, error) {
	rows, err := s.portalRows(ctx, portalTask, cond, order, args, start, limit, sel)
	if err != nil {
		return nil, err
	}
	var users, groups []string
	for _, t := range rows {
		for _, k := range []string{"CREATED_BY", "RESPONSIBLE_ID"} {
			if v, ok := t[k].(string); ok {
				users = append(users, v)
			}
		}
		if v, ok := t["GROUP_ID"].(string); ok {
			groups = append(groups, v)
		}
	}
	people, err := s.portalPeople(ctx, users)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if len(groups) > 0 {
		r, err := s.DB.Query(ctx, `SELECT num::text, name FROM projects WHERE num::text = ANY($1)`, groups)
		if err != nil {
			return nil, err
		}
		var num, name string
		if _, err := pgx.ForEachRow(r, []any{&num, &name}, func() error { names[num] = name; return nil }); err != nil {
			return nil, err
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, t := range rows {
		task := map[string]any{}
		for k, v := range t {
			task[camelPortal(k)] = v
		}
		if v, ok := t["CREATED_BY"].(string); ok {
			task["creator"] = people[v]
		}
		if v, ok := t["RESPONSIBLE_ID"].(string); ok {
			task["responsible"] = people[v]
		}
		if v, ok := t["GROUP_ID"].(string); ok {
			task["group"] = map[string]any{"id": v, "name": names[v]}
		}
		out = append(out, task)
	}
	return out, nil
}

// portalPeople are employees by their numbers: {id, name, icon, workPosition}.
func (s *Server) portalPeople(ctx context.Context, nums []string) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	if len(nums) == 0 {
		return out, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT num::text, id::text, name, photo_key, position FROM users WHERE num::text = ANY($1)`, nums)
	if err != nil {
		return nil, err
	}
	var num, id, name, photo, pos string
	_, err = pgx.ForEachRow(rows, []any{&num, &id, &name, &photo, &pos}, func() error {
		icon := ""
		if photo != "" {
			icon = strings.TrimRight(s.BaseURL, "/") + company.PhotoURL(id, photo)
		}
		out[num] = map[string]any{"id": num, "name": name, "icon": icon, "workPosition": pos}
		return nil
	})
	return out, err
}

// portalTaskID is the go24 id of the task numbered v the caller sees; no such task — not found.
func (s *Server) portalTaskID(c portalCall, v any) (string, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	if err != nil {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_task_id")
	}
	var id string
	err = s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM tasks WHERE num = $1`, n).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return "", err
	}
	return id, s.Tasks.CheckVisible(c.r.Context(), c.p.UserID, id)
}

// portalOneTask is the task as tasks.task.get gives it: {"task": {...}}.
func (s *Server) portalOneTask(c portalCall, id string, sel []portalField) (portalOut, error) {
	list, err := s.portalTasks(c.r.Context(), "x.id = $1", "x.num", []any{id}, 0, 0, sel)
	if err != nil {
		return portalOut{}, err
	}
	if len(list) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return portalOut{Result: map[string]any{"task": list[0]}}, nil
}

// tasks.task.get: taskId, select.
func (s *Server) portalTaskGet(c portalCall) (portalOut, error) {
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	e, err := s.portalTaskEntity(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalOneTask(c, id, portalTaskSelect(e, portalList(portalParam(c.in, "select"))))
}

// ---------- tasks.task: changing ----------

// portalTaskIn is what fields of tasks.task.add and update give, in go24's terms; nil — not given.
type portalTaskIn struct {
	Title, Description, Responsible, Group, Parent *string
	Priority                                       *tasks.Priority
	Deadline, StartPlan, EndPlan                   **time.Time
	Control, AllowDeadline, WorkTime, TimeTracking *bool
	Estimate                                       **int
	Accomplices, Auditors, Tags                    *[]string
}

// portalTaskUFIn are the values of the own fields given to tasks.task.add or update (field id → value).
func (s *Server) portalTaskUFIn(c portalCall) (map[string]string, error) {
	e, err := s.portalTaskEntity(c)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{}
	for k, v := range portalMap(portalArg(c.in, 1, "fields")) {
		fields[upperPortal(k)] = v
	}
	return portalUFIn(e, fields)
}

func (s *Server) portalTaskInput(c portalCall) (portalTaskIn, error) {
	ctx := c.r.Context()
	var in portalTaskIn
	for k, v := range portalMap(portalArg(c.in, 1, "fields")) {
		str := strings.TrimSpace(portalStr(v))
		switch upperPortal(k) {
		case "TITLE":
			in.Title = &str
		case "DESCRIPTION":
			in.Description = &str
		case "RESPONSIBLE_ID":
			id, err := s.portalNum(ctx, "users", v)
			if err != nil || id == "" {
				return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "RESPONSIBLE_ID")
			}
			in.Responsible = &id
		case "GROUP_ID":
			var key string
			if str != "" && str != "0" {
				if err := s.DB.QueryRow(ctx, `SELECT key FROM projects WHERE num::text = $1 OR id::text = $1`, str).Scan(&key); err != nil {
					return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "GROUP_ID")
				}
			}
			in.Group = &key
		case "PARENT_ID":
			var key string
			if str != "" && str != "0" {
				if err := s.DB.QueryRow(ctx, `SELECT p.key || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id
					WHERE t.num::text = $1`, str).Scan(&key); err != nil {
					return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "PARENT_ID")
				}
			}
			in.Parent = &key
		case "PRIORITY":
			n, err := strconv.Atoi(str)
			if err != nil || n < 0 || n > 2 {
				return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "PRIORITY")
			}
			pr := tasks.Priority(n)
			in.Priority = &pr
		case "DEADLINE", "START_DATE_PLAN", "END_DATE_PLAN":
			t, err := portalDate(str)
			if upperPortal(k) == "DEADLINE" {
				t, err = portalDateTime(str)
			}
			if err != nil {
				return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", upperPortal(k))
			}
			switch upperPortal(k) {
			case "DEADLINE":
				in.Deadline = &t
			case "START_DATE_PLAN":
				in.StartPlan = &t
			default:
				in.EndPlan = &t
			}
		case "TASK_CONTROL", "ALLOW_CHANGE_DEADLINE", "MATCH_WORK_TIME", "ALLOW_TIME_TRACKING":
			yes := strings.EqualFold(str, "Y") || str == "1" || strings.EqualFold(str, "true")
			switch upperPortal(k) {
			case "TASK_CONTROL":
				in.Control = &yes
			case "ALLOW_CHANGE_DEADLINE":
				in.AllowDeadline = &yes
			case "ALLOW_TIME_TRACKING":
				in.TimeTracking = &yes
			default:
				in.WorkTime = &yes
			}
		case "TIME_ESTIMATE": // seconds
			var est *int
			if n, err := strconv.Atoi(str); err == nil && n > 0 {
				est = new(int)
				*est = (n + 59) / 60
			} else if str != "" && str != "0" {
				return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "TIME_ESTIMATE")
			}
			in.Estimate = &est
		case "ACCOMPLICES", "AUDITORS":
			var ids []string
			for _, x := range portalList(v) {
				id, err := s.portalNum(ctx, "users", x)
				if err != nil {
					return in, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", upperPortal(k))
				}
				if id != "" {
					ids = append(ids, id)
				}
			}
			if upperPortal(k) == "ACCOMPLICES" {
				in.Accomplices = &ids
			} else {
				in.Auditors = &ids
			}
		case "TAGS": // a list, or the tags by commas
			var tags []string
			for _, x := range portalList(v) {
				for _, t := range strings.Split(x, ",") {
					if t = strings.TrimSpace(t); t != "" && !slices.Contains(tags, t) {
						tags = append(tags, t)
					}
				}
			}
			in.Tags = &tags
		}
	}
	return in, nil
}

// portalDateTime is a moment given gives it (ISO 8601, with the zone or local), or a day — its end.
func portalDateTime(v string) (*time.Time, error) {
	if v = strings.TrimSpace(v); v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02T15:04", "02.01.2006 15:04:05", "02.01.2006 15:04"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, nil
		}
	}
	for _, layout := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			t = t.Add(24*time.Hour - time.Minute) // 23:59 of the day
			return &t, nil
		}
	}
	return nil, i18n.E("err.bad_date")
}

// portalDefaultGroup is the project of a task given no GROUP_ID: the one the caller last put a task into, or the first
// they may work in — every go24 task is in a project.
func (s *Server) portalDefaultGroup(c portalCall) (string, error) {
	var key string
	err := s.DB.QueryRow(c.r.Context(), `SELECT p.key FROM tasks t JOIN projects p ON p.id = t.project_id
		JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE t.creator_id = $1 AND NOT p.archived ORDER BY t.created_at DESC LIMIT 1`, c.p.UserID).Scan(&key)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	list, err := s.Tasks.ListProjects(c.r.Context(), c.p.UserID)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if p.CanCreate() {
			return p.Key, nil
		}
	}
	return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "GROUP_ID")
}

// tasks.task.add: fields → {"task": {...}}.
func (s *Server) portalTaskAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	in, err := s.portalTaskInput(c)
	if err != nil {
		return portalOut{}, err
	}
	uf, err := s.portalTaskUFIn(c)
	if err != nil {
		return portalOut{}, err
	}
	group := deref(in.Group, "")
	if group == "" {
		if group, err = s.portalDefaultGroup(c); err != nil {
			return portalOut{}, err
		}
	}
	pr, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, group)
	if err != nil {
		return portalOut{}, err
	}
	key, err := s.Tasks.CreateTask(ctx, c.p.UserID, pr, tasks.NewTask{
		Title:               deref(in.Title, ""),
		Description:         deref(in.Description, ""),
		ResponsibleID:       deref(in.Responsible, c.p.UserID),
		Priority:            deref(in.Priority, 1),
		Deadline:            deref(in.Deadline, nil),
		RequiresControl:     deref(in.Control, false),
		ParentKey:           deref(in.Parent, ""),
		Accomplices:         deref(in.Accomplices, nil),
		Auditors:            deref(in.Auditors, nil),
		AllowChangeDeadline: deref(in.AllowDeadline, false),
		MatchWorkTime:       deref(in.WorkTime, false),
		Params:              tasks.Params{NoTimeTracking: !deref(in.TimeTracking, true)},
	})
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.Tasks.TaskIDByKey(ctx, key)
	if err != nil {
		return portalOut{}, err
	}
	if in.StartPlan != nil || in.EndPlan != nil {
		if err := s.Tasks.SetPlanDates(ctx, c.p.UserID, id, deref(in.StartPlan, nil), deref(in.EndPlan, nil)); err != nil {
			return portalOut{}, err
		}
	}
	if in.Estimate != nil && *in.Estimate != nil {
		if err := s.portalTaskChange(c, id, portalTaskIn{Estimate: in.Estimate}); err != nil {
			return portalOut{}, err
		}
	}
	if err := s.portalTaskTags(c, id, in.Tags); err != nil {
		return portalOut{}, err
	}
	if len(uf) > 0 {
		if err := s.Tasks.SetFieldValues(ctx, c.p.UserID, id, uf); err != nil {
			return portalOut{}, err
		}
	}
	return s.portalOneTask(c, id, portalTask.Fields)
}

// portalTaskChange applies the fields given to the task; the rest stay as they are.
func (s *Server) portalTaskChange(c portalCall, id string, in portalTaskIn) error {
	ctx := c.r.Context()
	cur, err := s.Tasks.GetCard(ctx, c.p.UserID, id)
	if err != nil {
		return err
	}
	if in.Title != nil || in.Description != nil || in.Responsible != nil || in.Priority != nil || in.Deadline != nil ||
		in.Control != nil || in.Estimate != nil || in.StartPlan != nil || in.EndPlan != nil || in.AllowDeadline != nil || in.WorkTime != nil || in.TimeTracking != nil {
		params := cur.Params
		params.NoTimeTracking = !deref(in.TimeTracking, !cur.NoTimeTracking)
		if err := s.Tasks.UpdateTask(ctx, c.p.UserID, id, tasks.TaskUpdate{
			Version:             cur.Version,
			Title:               deref(in.Title, cur.Title),
			Description:         deref(in.Description, cur.Description),
			ResponsibleID:       deref(in.Responsible, cur.ResponsibleID),
			Priority:            deref(in.Priority, cur.Priority),
			Deadline:            deref(in.Deadline, cur.Deadline),
			RequiresControl:     deref(in.Control, cur.RequiresControl),
			EstimateMinutes:     deref(in.Estimate, cur.EstimateMinutes),
			StartPlan:           deref(in.StartPlan, cur.StartPlan),
			EndPlan:             deref(in.EndPlan, cur.EndPlan),
			AllowChangeDeadline: deref(in.AllowDeadline, cur.AllowChangeDeadline),
			MatchWorkTime:       deref(in.WorkTime, cur.MatchWorkTime),
			Params:              params,
		}); err != nil {
			return err
		}
	}
	if in.Accomplices != nil || in.Auditors != nil {
		ids := func(us []tasks.User) []string {
			out := []string{}
			for _, u := range us {
				out = append(out, u.ID)
			}
			return out
		}
		if err := s.Tasks.SetParticipants(ctx, c.p.UserID, id, deref(in.Accomplices, ids(cur.Accomplices)),
			deref(in.Auditors, ids(cur.Auditors))); err != nil {
			return err
		}
	}
	return s.portalTaskTags(c, id, in.Tags)
}

// portalTaskTags makes the task's tags the ones given (nil — as they are).
func (s *Server) portalTaskTags(c portalCall, id string, tags *[]string) error {
	if tags == nil {
		return nil
	}
	ctx := c.r.Context()
	var cur []string
	rows, err := s.DB.Query(ctx, `SELECT tag FROM task_tags WHERE task_id = $1`, id)
	if err != nil {
		return err
	}
	if cur, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return err
	}
	has := func(list []string, t string) bool {
		return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, t) })
	}
	for _, t := range cur {
		if !has(*tags, t) {
			if err := s.Tasks.SetTag(ctx, c.p.UserID, id, t, false); err != nil {
				return err
			}
		}
	}
	for _, t := range *tags {
		if !has(cur, t) {
			if err := s.Tasks.SetTag(ctx, c.p.UserID, id, t, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// tasks.task.update: taskId, fields → {"task": {...}}; GROUP_ID and PARENT_ID do not move a task.
func (s *Server) portalTaskUpdate(c portalCall) (portalOut, error) {
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	in, err := s.portalTaskInput(c)
	if err != nil {
		return portalOut{}, err
	}
	uf, err := s.portalTaskUFIn(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalTaskChange(c, id, in); err != nil {
		return portalOut{}, err
	}
	if len(uf) > 0 {
		if err := s.Tasks.SetFieldValues(c.r.Context(), c.p.UserID, id, uf); err != nil {
			return portalOut{}, err
		}
	}
	return s.portalOneTask(c, id, portalTask.Fields)
}

// tasks.task.delete: taskId → {"task": true}; the task goes to the trash.
func (s *Server) portalTaskDelete(c portalCall) (portalOut, error) {
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.DeleteTask(c.r.Context(), c.p.UserID, id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"task": true}}, nil
}

// tasks.task.start, pause, complete, defer, renew, approve, disapprove: taskId → {"task": {...}}.
func portalTaskAction(a tasks.Action) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Tasks.ApplyAction(c.r.Context(), c.p.UserID, id, a); err != nil {
			return portalOut{}, err
		}
		return s.portalOneTask(c, id, portalTask.Fields)
	}
}

// tasks.task.delegate: taskId, userId → {"task": {...}}.
func (s *Server) portalTaskDelegate(c portalCall) (portalOut, error) {
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	to, err := s.portalNum(c.r.Context(), "users", portalArg(c.in, 1, "userId"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.Delegate(c.r.Context(), c.p.UserID, id, to); err != nil {
		return portalOut{}, err
	}
	return s.portalOneTask(c, id, portalTask.Fields)
}

// tasks.task.favorite.add/remove, tasks.task.mute/unmute: taskId → {"task": true}.
func portalTaskMark(mark string, on bool) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Tasks.SetMark(c.r.Context(), c.p.UserID, id, mark, on); err != nil {
			return portalOut{}, err
		}
		return portalOut{Result: map[string]any{"task": true}}, nil
	}
}

// ---------- task.commentitem ----------

const portalCommentCols = `m.num::text, um.num::text, um.name, um.email, m.created_at, m.body`

// portalComment is a comment as task.commentitem.get gives it.
func portalComment(num, author, name, email string, at time.Time, body string) map[string]any {
	return map[string]any{"ID": num, "AUTHOR_ID": author, "AUTHOR_NAME": name, "AUTHOR_EMAIL": email,
		"POST_DATE": at.In(time.Local).Format(time.RFC3339), "POST_MESSAGE": body, "POST_MESSAGE_HTML": body}
}

func (s *Server) portalComments(ctx context.Context, cond string, order string, args ...any) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT `+portalCommentCols+` FROM task_comments m JOIN users um ON um.id = m.author_id
		WHERE `+cond+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var num, author, name, email, body string
	var at time.Time
	_, err = pgx.ForEachRow(rows, []any{&num, &author, &name, &email, &at, &body}, func() error {
		out = append(out, portalComment(num, author, name, email, at, body))
		return nil
	})
	return out, err
}

// task.commentitem.getlist: TASKID, ORDER {ID|POST_DATE: asc|desc}, FILTER {AUTHOR_ID, >POST_DATE, <POST_DATE…}.
func (s *Server) portalCommentList(c portalCall) (portalOut, error) {
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	args := []any{id}
	where := []string{"m.task_id = $1"}
	for k, v := range portalMap(portalArg(c.in, 2, "FILTER")) {
		op := strings.TrimRight(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz")
		args = append(args, portalStr(v))
		ph := "$" + strconv.Itoa(len(args))
		switch field := strings.ToUpper(k[len(op):]); {
		case field == "AUTHOR_ID" && (op == "" || op == "=" || op == "!"):
			cond := "um.num::text = " + ph
			if op == "!" {
				cond = "NOT " + cond
			}
			where = append(where, cond)
		case (field == "POST_DATE" || field == "ID") && slices.Contains([]string{">", "<", ">=", "<=", "", "="}, op):
			col, cast := "m.created_at", "::timestamptz"
			if field == "ID" {
				col, cast = "m.num", "::bigint"
			}
			if op == "" {
				op = "="
			}
			where = append(where, col+" "+op+" "+ph+cast)
		default:
			args = args[:len(args)-1]
		}
	}
	order := "m.created_at, m.num"
	for k, v := range portalMap(portalArg(c.in, 1, "ORDER")) {
		dir := "ASC"
		if strings.EqualFold(portalStr(v), "desc") {
			dir = "DESC"
		}
		switch strings.ToUpper(k) {
		case "ID":
			order = "m.num " + dir
		case "POST_DATE":
			order = "m.created_at " + dir + ", m.num " + dir
		case "AUTHOR_ID":
			order = "um.num " + dir + ", m.num"
		}
	}
	list, err := s.portalComments(c.r.Context(), strings.Join(where, " AND "), order, args...)
	return portalOut{Result: list}, err
}

// portalCommentID is the go24 id of the comment numbered ITEMID of the task.
func (s *Server) portalCommentID(c portalCall, taskID string) (string, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM task_comments WHERE task_id = $1 AND num::text = $2`, taskID,
		strings.TrimSpace(portalStr(portalArg(c.in, 1, "ITEMID")))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return id, err
}

// task.commentitem.get: TASKID, ITEMID.
func (s *Server) portalCommentGet(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCommentID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalComments(c.r.Context(), "m.id = $1", "m.num", id)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0]}, nil
}

// portalCommentText is POST_MESSAGE of FIELDS (the place after TASKID, or after ITEMID).
func portalCommentText(in map[string]any, place int) string {
	return strings.TrimSpace(portalStr(portalParam(portalMap(portalArg(in, place, "FIELDS")), "POST_MESSAGE")))
}

// task.commentitem.add: TASKID, FIELDS {POST_MESSAGE} → the number of the comment. The author is the caller.
func (s *Server) portalCommentAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	var cm tasks.Comment
	if s.Matrix != nil { // as in the web: the message is kept in the messenger first
		cm, err = s.Matrix.PostTaskReply(ctx, c.p.UserID, tid, "", portalCommentText(c.in, 1))
	} else {
		cm, err = s.Tasks.PostReply(ctx, c.p.UserID, tid, "", portalCommentText(c.in, 1))
	}
	if err != nil {
		return portalOut{}, err
	}
	var num int64
	if err := s.DB.QueryRow(ctx, `SELECT num FROM task_comments WHERE id = $1`, cm.ID).Scan(&num); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: num}, nil
}

// task.commentitem.update: TASKID, ITEMID, FIELDS {POST_MESSAGE} → true; one's own comment.
func (s *Server) portalCommentUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCommentID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	if s.Matrix != nil {
		_, err = s.Matrix.EditTaskComment(ctx, c.p.UserID, tid, id, portalCommentText(c.in, 2))
	} else {
		_, err = s.Tasks.EditComment(ctx, c.p.UserID, tid, id, portalCommentText(c.in, 2))
	}
	return portalOut{Result: true}, err
}

// task.commentitem.delete: TASKID, ITEMID → true; one's own comment (an administrator — any).
func (s *Server) portalCommentDelete(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCommentID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	if s.Matrix != nil {
		err = s.Matrix.DeleteTaskComment(ctx, c.p.UserID, tid, id)
	} else {
		err = s.Tasks.DeleteComment(ctx, c.p.UserID, tid, id)
	}
	return portalOut{Result: true}, err
}

// ---------- task.checklistitem ----------

// portalChecks are checklist items as task.checklistitem.get gives them: all the lists of the task, in order; PARENT_ID
// is the item a nested one is in.
func (s *Server) portalChecks(ctx context.Context, taskID, itemID string) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.num::text, t.num::text, uc.num::text, i.title, i.done, i.important, ud.num::text, i.done_at,
			i.level, (SELECT j.num::text FROM task_checklist_items j WHERE j.checklist_id = i.checklist_id
				AND j.level < i.level AND (j.sort, j.created_at) < (i.sort, i.created_at)
				ORDER BY j.sort DESC, j.created_at DESC LIMIT 1)
		FROM task_checklist_items i JOIN tasks t ON t.id = i.task_id JOIN task_checklists l ON l.id = i.checklist_id
		JOIN users uc ON uc.id = i.created_by LEFT JOIN users ud ON ud.id = i.done_by
		WHERE i.task_id = $1 AND ($2 = '' OR i.id::text = $2) ORDER BY l.sort, l.created_at, i.sort, i.created_at`, taskID, itemID)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var num, task, by, title string
	var done, important bool
	var doneBy, parent *string
	var doneAt *time.Time
	var level int
	_, err = pgx.ForEachRow(rows, []any{&num, &task, &by, &title, &done, &important, &doneBy, &doneAt, &level, &parent}, func() error {
		pid := "0"
		if level > 0 && parent != nil {
			pid = *parent
		}
		item := map[string]any{"ID": num, "TASK_ID": task, "PARENT_ID": pid, "CREATED_BY": by, "TITLE": title,
			"SORT_INDEX": strconv.Itoa(len(out)), "IS_COMPLETE": portalStr(done), "IS_IMPORTANT": portalStr(important),
			"TOGGLED_BY": doneBy, "TOGGLED_DATE": nil}
		if doneAt != nil {
			item["TOGGLED_DATE"] = doneAt.In(time.Local).Format(time.RFC3339)
		}
		out = append(out, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// ATTACHMENTS: the item's files by their numbers, [] — none gives them
	rows, err = s.DB.Query(ctx, `SELECT i.num::text, f.num::text, f.name, f.size FROM files f
		JOIN task_checklist_items i ON i.id = f.checklist_item_id WHERE f.task_id = $1 ORDER BY f.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	files := map[string]map[string]any{}
	var item, file, name string
	var size int64
	if _, err = pgx.ForEachRow(rows, []any{&item, &file, &name, &size}, func() error {
		if files[item] == nil {
			files[item] = map[string]any{}
		}
		files[item][file] = map[string]any{"ATTACHMENT_ID": file, "FILE_ID": file, "NAME": name, "SIZE": strconv.FormatInt(size, 10)}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, it := range out {
		if f := files[it["ID"].(string)]; f != nil {
			it["ATTACHMENTS"] = f
		} else {
			it["ATTACHMENTS"] = []any{}
		}
	}
	return out, nil
}

// portalCheckID is the go24 id of the checklist item numbered ITEMID of the task.
func (s *Server) portalCheckID(c portalCall, taskID string) (string, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM task_checklist_items WHERE task_id = $1 AND num::text = $2`, taskID,
		strings.TrimSpace(portalStr(portalArg(c.in, 1, "ITEMID")))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return id, err
}

// task.checklistitem.getlist: TASKID.
func (s *Server) portalCheckList(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalChecks(c.r.Context(), tid, "")
	return portalOut{Result: list}, err
}

// task.checklistitem.get: TASKID, ITEMID.
func (s *Server) portalCheckGet(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCheckID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalChecks(c.r.Context(), tid, id)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0]}, nil
}

// portalCheckFields are FIELDS {TITLE, IS_COMPLETE} at their place.
func portalCheckFields(in map[string]any, place int) (title string, done *bool) {
	f := portalMap(portalArg(in, place, "FIELDS"))
	title = strings.Join(strings.Fields(portalStr(portalParam(f, "TITLE"))), " ") // one line: one item
	if v, ok := portalParamOK(f, "IS_COMPLETE"); ok {
		d := strings.EqualFold(portalStr(v), "Y")
		done = &d
	}
	return title, done
}

// portalCheckSet ticks (unticks) the item, if it is not so yet.
func (s *Server) portalCheckSet(c portalCall, taskID, itemID string, done bool) error {
	var cur bool
	if err := s.DB.QueryRow(c.r.Context(), `SELECT done FROM task_checklist_items WHERE id = $1`, itemID).Scan(&cur); err != nil {
		return err
	}
	if cur == done {
		return nil
	}
	return s.Tasks.ChangeChecklistItem(c.r.Context(), c.p.UserID, taskID, itemID, false)
}

// task.checklistitem.add: TASKID, FIELDS {TITLE, IS_COMPLETE} → the number of the item (in the first list).
func (s *Server) portalCheckAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	title, done := portalCheckFields(c.in, 1)
	if err := s.Tasks.AddChecklistItems(ctx, c.p.UserID, tid, "", title); err != nil {
		return portalOut{}, err
	}
	var id string
	var num int64
	if err := s.DB.QueryRow(ctx, `SELECT id::text, num FROM task_checklist_items WHERE task_id = $1 AND created_by = $2
		ORDER BY num DESC LIMIT 1`, tid, c.p.UserID).Scan(&id, &num); err != nil {
		return portalOut{}, err
	}
	if done != nil && *done {
		if err := s.portalCheckSet(c, tid, id, true); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: num}, nil
}

// task.checklistitem.update: TASKID, ITEMID, FIELDS {TITLE, IS_COMPLETE} → true.
func (s *Server) portalCheckUpdate(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCheckID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	title, done := portalCheckFields(c.in, 2)
	if title != "" {
		if err := s.Tasks.EditChecklistItem(c.r.Context(), c.p.UserID, tid, id, title); err != nil {
			return portalOut{}, err
		}
	}
	if done != nil {
		if err := s.portalCheckSet(c, tid, id, *done); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}

// task.checklistitem.complete and renew: TASKID, ITEMID → true.
func portalCheckDone(done bool) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
		if err != nil {
			return portalOut{}, err
		}
		id, err := s.portalCheckID(c, tid)
		if err != nil {
			return portalOut{}, err
		}
		return portalOut{Result: true}, s.portalCheckSet(c, tid, id, done)
	}
}

// task.checklistitem.delete: TASKID, ITEMID → true.
func (s *Server) portalCheckDelete(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCheckID(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.ChangeChecklistItem(c.r.Context(), c.p.UserID, tid, id, true)
}

// ---------- department ----------

// department.get: the departments [{ID, NAME, SORT, PARENT, UF_HEAD}]; filter by those fields, sort and order, 50 a
// page.
func (s *Server) portalDepartmentGet(c portalCall) (portalOut, error) {
	cols := map[string]string{"ID": "d.num::text", "NAME": "d.name", "SORT": "d.sort::text", "PARENT": "coalesce(dp.num::text, '')",
		"UF_HEAD": "coalesce(uh.num::text, '')"}
	args := []any{}
	var where []string
	for k, v := range c.in {
		col, ok := cols[strings.ToUpper(k)]
		if !ok {
			continue
		}
		args = append(args, portalStr(v))
		where = append(where, col+" = $"+strconv.Itoa(len(args)))
	}
	for k, v := range portalMap(portalParam(c.in, "filter")) {
		if col, ok := cols[strings.ToUpper(k)]; ok {
			args = append(args, portalList(v))
			where = append(where, col+" = ANY($"+strconv.Itoa(len(args))+"::text[])")
		}
	}
	order := "d.num"
	if col, ok := map[string]string{"ID": "d.num", "NAME": "d.name", "SORT": "d.sort", "PARENT": "dp.num"}[strings.ToUpper(portalStr(portalParam(c.in, "sort")))]; ok {
		order = col
		if strings.EqualFold(portalStr(portalParam(c.in, "order")), "desc") {
			order += " DESC"
		}
		order += ", d.num"
	}
	cond := "true"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	from := `departments d LEFT JOIN departments dp ON dp.id = d.parent_id LEFT JOIN users uh ON uh.id = d.head_id`
	ctx := c.r.Context()
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM `+from+` WHERE `+cond, args...).Scan(&total); err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	rows, err := s.DB.Query(ctx, `SELECT d.num::text, d.name, d.sort::text, dp.num::text, uh.num::text FROM `+from+` WHERE `+cond+
		` ORDER BY `+order+` OFFSET `+strconv.Itoa(start)+` LIMIT `+strconv.Itoa(portalPage), args...)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	var id, name, sort string
	var parent, head *string
	_, err = pgx.ForEachRow(rows, []any{&id, &name, &sort, &parent, &head}, func() error {
		d := map[string]any{"ID": id, "NAME": name, "SORT": sort}
		if parent != nil {
			d["PARENT"] = *parent
		}
		if head != nil {
			d["UF_HEAD"] = *head
		}
		out = append(out, d)
		return nil
	})
	if err != nil {
		return portalOut{}, err
	}
	return portalPaged(out, start, len(out), total), nil
}
