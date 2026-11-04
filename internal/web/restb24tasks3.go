package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// REST compatible with the portal, tasks, step 3 (GO24DEV-1000): tasks.task.getaccess (what people may do with a task),
// tasks.task.files.attach (a file of the Drive into the task), task.logitem.list (the history, the old method) and
// task.item.userfield.* (the own fields of tasks, UF_<code> or UF_AUTO_<number>, as the CRM's of restb24crmuf.go).

func init() {
	for name, m := range map[string]portalMethod{
		"tasks.task.getaccess":         {Scope: "tasks:read", Run: (*Server).portalTaskAccess},
		"tasks.task.files.attach":      {Scope: "tasks:write", Run: (*Server).portalTaskFileAttach},
		"task.logitem.list":            {Scope: "tasks:read", Run: (*Server).portalLogItems},
		"task.item.userfield.getlist":  {Scope: "tasks:read", Run: portalTaskUF((*Server).portalUFList, "ORDER:order", "FILTER:filter")},
		"task.item.userfield.get":      {Scope: "tasks:read", Run: portalTaskUF((*Server).portalUFGet, "ID:id")},
		"task.item.userfield.add":      {Scope: "tasks:write", Run: portalTaskUF((*Server).portalUFAdd, "PARAMS:fields")},
		"task.item.userfield.update":   {Scope: "tasks:write", Run: portalTaskUF((*Server).portalUFUpdate, "ID:id", "DATA:fields")},
		"task.item.userfield.delete":   {Scope: "tasks:write", Run: portalTaskUF((*Server).portalUFDelete, "ID:id")},
		"task.item.userfield.gettypes": {Scope: "tasks:read", Run: (*Server).portalUFTypesList},
		"task.item.userfield.getfields": {Scope: "tasks:read", Run: func(s *Server, c portalCall) (portalOut, error) {
			return s.portalUFDescribe(c, "ID", "ENTITY_ID", "FIELD_NAME", "USER_TYPE_ID", "XML_ID", "SORT", "MULTIPLE", "MANDATORY",
				"SHOW_FILTER", "SHOW_IN_LIST", "EDIT_IN_LIST", "IS_SEARCHABLE", "SETTINGS", "EDIT_FORM_LABEL", "LIST_COLUMN_LABEL",
				"LIST_FILTER_LABEL", "ERROR_MESSAGE", "HELP_MESSAGE", "LIST")
		}},
	} {
		portalMethods[name] = m
	}
}

// ---------- task.item.userfield.* ----------

// portalTaskUF runs a userfield method of the CRM for the fields of tasks: args are the parameters of the task.item
// method in their places, "ID:id" — the name and the one of the CRM method. A list is a plain array, as there.
func portalTaskUF(run func(*Server, portalCall, string) (portalOut, error), args ...string) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		in := map[string]any{}
		for i, a := range args {
			portalName, name, _ := strings.Cut(a, ":")
			if v := portalArg(c.in, i, portalName, name); v != nil {
				in[name] = v
			}
		}
		c.in = in
		out, err := run(s, c, "task")
		return portalOut{Result: out.Result}, err
	}
}

// ---------- tasks.task.getaccess ----------

// portalTaskActions are the actions of allowedActions in the order.
var portalTaskActions = []string{"ACTION_ACCEPT", "ACTION_DECLINE", "ACTION_COMPLETE", "ACTION_APPROVE", "ACTION_DISAPPROVE",
	"ACTION_START", "ACTION_PAUSE", "ACTION_DELEGATE", "ACTION_REMOVE", "ACTION_EDIT", "ACTION_DEFER", "ACTION_RENEW",
	"ACTION_CREATE", "ACTION_CHANGE_DEADLINE", "ACTION_CHECKLIST_ADD_ITEMS", "ACTION_ADD_FAVORITE", "ACTION_DELETE_FAVORITE",
	"ACTION_RATE", "ACTION_CHANGE_DIRECTOR", "ACTION_ELAPSED_TIME_ADD", "ACTION_START_TIME_TRACKING"}

// portalStatusActions: an action of the workflow → its name in allowedActions.
var portalStatusActions = map[tasks.Action]string{tasks.ActionStart: "ACTION_START", tasks.ActionPause: "ACTION_PAUSE",
	tasks.ActionComplete: "ACTION_COMPLETE", tasks.ActionApprove: "ACTION_APPROVE", tasks.ActionDisapprove: "ACTION_DISAPPROVE",
	tasks.ActionDefer: "ACTION_DEFER", tasks.ActionRenew: "ACTION_RENEW"}

// portalAllowedActions is what the person may do with the task now; nil — they do not see it.
func (s *Server) portalAllowedActions(ctx context.Context, userID, taskID string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, a := range portalTaskActions {
		out[a] = false
	}
	c, err := s.Tasks.RowCard(ctx, userID, taskID)
	if errors.Is(err, tasks.ErrNotFound) || errors.Is(err, tasks.ErrForbidden) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, a := range c.Actions {
		if n, ok := portalStatusActions[a]; ok {
			out[n] = true
		}
	}
	marks, err := s.Tasks.Marks(ctx, userID, taskID)
	if err != nil {
		return nil, err
	}
	open := c.Status != tasks.StatusCompleted && !c.Frozen
	work := (c.Roles.Worker() || c.Roles.Manager) && !c.Frozen
	out["ACTION_DELEGATE"] = c.Roles.Responsible && open
	out["ACTION_REMOVE"] = c.CanDelete
	out["ACTION_EDIT"] = c.CanEdit() && !c.Frozen
	out["ACTION_CREATE"] = !c.Frozen
	out["ACTION_CHANGE_DEADLINE"] = out["ACTION_EDIT"] || c.Roles.Responsible && c.AllowChangeDeadline && open
	out["ACTION_CHECKLIST_ADD_ITEMS"] = work
	out["ACTION_ADD_FAVORITE"] = !marks["favorite"]
	out["ACTION_DELETE_FAVORITE"] = marks["favorite"]
	out["ACTION_RATE"] = c.Roles.Creator && !c.Frozen
	out["ACTION_CHANGE_DIRECTOR"] = out["ACTION_EDIT"]
	out["ACTION_ELAPSED_TIME_ADD"] = work && !c.NoTimeTracking
	out["ACTION_START_TIME_TRACKING"] = work && !c.NoTimeTracking && open
	return out, nil
}

// tasks.task.getaccess: taskId, users (the numbers of employees; by default — the caller) → {"allowedActions":
// {"U1": {"ACTION_EDIT": true, …}}}. Who does not see the task may do nothing with it; others' rights are asked
// without the caller's admin mode.
func (s *Server) portalTaskAccess(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	nums := portalList(portalArg(c.in, 1, "users"))
	if len(nums) == 0 {
		var me string
		if err := s.DB.QueryRow(ctx, `SELECT num::text FROM users WHERE id = $1`, c.p.UserID).Scan(&me); err != nil {
			return portalOut{}, err
		}
		nums = []string{me}
	}
	out := map[string]any{}
	for _, n := range nums {
		n = strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(n)), "U")
		uid, err := s.portalNum(ctx, "users", n)
		if err != nil {
			return portalOut{}, err
		}
		if uid == "" {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "users")
		}
		uctx := ctx
		if uid != c.p.UserID {
			uctx = tasks.WithoutAdmin(ctx)
		}
		acts, err := s.portalAllowedActions(uctx, uid, id)
		if err != nil {
			return portalOut{}, err
		}
		out["U"+n] = acts
	}
	return portalOut{Result: map[string]any{"allowedActions": out}}, nil
}

// ---------- tasks.task.files.attach ----------

// tasks.task.files.attach: taskId, fileId (a file of the Drive, as disk.file.get knows it) → {"attachmentId": the
// number of the file in the task}. A copy goes into the task, as «Файл с Диска» of the card.
func (s *Server) portalTaskFileAttach(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
	if err != nil {
		return portalOut{}, err
	}
	o, err := s.portalDiskRef(c, portalArg(c.in, 1, "fileId"), "file", false)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.CanAttach(ctx, c.p.UserID, id); err != nil {
		return portalOut{}, err
	}
	since := time.Now()
	if _, err := s.attachDriveFile(c.r, auth.Session{UserID: c.p.UserID}, o.ID, storages.NewFile{TaskID: id}); err != nil {
		return portalOut{}, err
	}
	var num int64
	err = s.DB.QueryRow(ctx, `SELECT num FROM files WHERE task_id = $1 AND uploaded_by = $2 AND created_at >= $3
		ORDER BY created_at DESC, num DESC LIMIT 1`, id, c.p.UserID, since.Add(-time.Second)).Scan(&num)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"attachmentId": num}}, nil
}

// ---------- task.logitem.list ----------

// task.logitem.list: TASKID, ORDER {ID | CREATED_DATE: asc | desc}, FILTER {FIELD} → [{ID, CREATED_DATE, USER_ID,
// TASK_ID, FIELD, FROM_VALUE, TO_VALUE}], the old twin of tasks.task.history.list.
func (s *Server) portalLogItems(c portalCall) (portalOut, error) {
	num := portalArg(c.in, 0, "TASKID", "taskId")
	tid, err := s.portalTaskID(c, num)
	if err != nil {
		return portalOut{}, err
	}
	dir := "ASC"
	for k, v := range portalMap(portalArg(c.in, 1, "ORDER")) {
		if (strings.EqualFold(k, "ID") || strings.EqualFold(k, "CREATED_DATE")) && strings.EqualFold(portalStr(v), "desc") {
			dir = "DESC"
		}
	}
	var want []string
	for k, v := range portalMap(portalArg(c.in, 2, "FILTER")) {
		if strings.EqualFold(strings.TrimLeft(k, "="), "FIELD") {
			want = append(want, portalList(v)...)
		}
	}
	list, err := s.portalHistoryRows(c.r.Context(), tid, dir)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, x := range list {
		if len(want) > 0 && !slices.ContainsFunc(want, func(w string) bool { return strings.EqualFold(w, x.field) }) {
			continue
		}
		out = append(out, map[string]any{"ID": strconv.FormatInt(x.id, 10), "CREATED_DATE": x.at.In(time.Local).Format(time.RFC3339),
			"USER_ID": x.user, "TASK_ID": strings.TrimSpace(portalStr(num)), "FIELD": x.field, "FROM_VALUE": x.from, "TO_VALUE": x.to})
	}
	return portalOut{Result: out}, nil
}
