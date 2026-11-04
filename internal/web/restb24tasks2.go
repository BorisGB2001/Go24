package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// REST compatible with the portal, tasks, step 2 (GO24DEV-995): task.elapseditem.* (time tracking), task.stages.* (the
// stages of a group's kanban and of «Мой план»), task.dependence.add/delete (the links of Gantt), tasks.task.result.*,
// portalHistoryRow is a line of a task's history with the field and the statuses names them.
type portalHistoryRow struct {
	id              int64
	field, from, to string
	at              time.Time
	user            string // the number of who changed it
}

// portalHistoryRows is the history of the task in the order of its lines (dir — ASC or DESC).
func (s *Server) portalHistoryRows(ctx context.Context, taskID, dir string) ([]portalHistoryRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT h.id, h.field, h.old_value, h.new_value, h.created_at, u.num::text FROM task_history h
		JOIN users u ON u.id = h.actor_id WHERE h.task_id = $1 ORDER BY h.id `+dir, taskID)
	if err != nil {
		return nil, err
	}
	var list []portalHistoryRow
	var r portalHistoryRow
	_, err = pgx.ForEachRow(rows, []any{&r.id, &r.field, &r.from, &r.to, &r.at, &r.user}, func() error {
		f, ok := portalHistoryFields[r.field]
		if !ok {
			f = strings.ToUpper(r.field)
		}
		if f == "STATUS" {
			r.from, r.to = portalHistoryStatus[r.from], portalHistoryStatus[r.to]
		}
		r.field = f
		list = append(list, r)
		return nil
	})
	return list, err
}

// tasks.task.history.list, tasks.task.counters.get, task.planner.getlist (the plan of the day) and
// tasks.task.startwatch/stopwatch. Time entries and stages have numbers (migration 996); a result is known by the
// number of its comment.

func init() {
	for name, m := range map[string]portalMethod{
		"task.elapseditem.getlist":            {Scope: "tasks:read", Run: (*Server).portalTimeList},
		"task.elapseditem.get":                {Scope: "tasks:read", Run: (*Server).portalTimeGet},
		"task.elapseditem.add":                {Scope: "tasks:write", Run: (*Server).portalTimeAdd},
		"task.elapseditem.update":             {Scope: "tasks:write", Run: (*Server).portalTimeUpdate},
		"task.elapseditem.delete":             {Scope: "tasks:write", Run: (*Server).portalTimeDelete},
		"task.stages.get":                     {Scope: "tasks:read", Run: (*Server).portalStagesGet},
		"task.stages.add":                     {Scope: "tasks:write", Run: (*Server).portalStageAdd},
		"task.stages.update":                  {Scope: "tasks:write", Run: (*Server).portalStageUpdate},
		"task.stages.delete":                  {Scope: "tasks:write", Run: (*Server).portalStageDelete},
		"task.stages.canmovetask":             {Scope: "tasks:read", Run: (*Server).portalStageCanMove},
		"task.stages.movetask":                {Scope: "tasks:write", Run: (*Server).portalStageMove},
		"task.dependence.add":                 {Scope: "tasks:write", Run: (*Server).portalDependenceAdd},
		"task.dependence.delete":              {Scope: "tasks:write", Run: (*Server).portalDependenceDelete},
		"tasks.task.result.list":              {Scope: "tasks:read", Run: (*Server).portalResultList},
		"tasks.task.result.addfromcomment":    {Scope: "tasks:write", Run: portalResultMark(true)},
		"tasks.task.result.deletefromcomment": {Scope: "tasks:write", Run: portalResultMark(false)},
		"tasks.task.history.list":             {Scope: "tasks:read", Run: (*Server).portalHistoryList},
		"tasks.task.counters.get":             {Scope: "tasks:read", Run: (*Server).portalTaskCounters},
		"task.planner.getlist":                {Scope: "tasks:read", Run: (*Server).portalPlannerList},
		"tasks.task.startwatch":               {Scope: "tasks:write", Run: portalTaskWatch(true)},
		"tasks.task.stopwatch":                {Scope: "tasks:write", Run: portalTaskWatch(false)},
	} {
		portalMethods[name] = m
	}
}

// ---------- task.elapseditem ----------

// portalTimeSource is SOURCE: 2 — entered by hand, 3 — the timer.
var portalTimeSource = map[string]string{"manual": "2", "timer": "3"}

// portalTimeRows reads time entries (e) of tasks (tk) with their authors (u) gives them. CREATED_DATE is the
// day the time was logged for, at the hour the entry was made; DATE_STOP is that moment and DATE_START — the time
// before it.
func (s *Server) portalTimeRows(ctx context.Context, cond, order string, args []any, start, limit int) ([]map[string]any, error) {
	q := `SELECT e.num::text, tk.num::text, u.num::text, e.comment, e.seconds, e.source, e.spent_on, e.created_at
		FROM time_entries e JOIN tasks tk ON tk.id = e.task_id JOIN users u ON u.id = e.user_id WHERE ` + cond + ` ORDER BY ` + order
	if limit > 0 {
		q += ` LIMIT ` + strconv.Itoa(limit) + ` OFFSET ` + strconv.Itoa(start)
	}
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	var num, task, user, comment, source string
	var secs int
	var day, at time.Time
	_, err = pgx.ForEachRow(rows, []any{&num, &task, &user, &comment, &secs, &source, &day, &at}, func() error {
		local := at.In(time.Local)
		y, m, d := day.Date() // a date column comes as midnight UTC
		created := time.Date(y, m, d, local.Hour(), local.Minute(), local.Second(), 0, time.Local)
		out = append(out, map[string]any{"ID": num, "TASK_ID": task, "USER_ID": user, "COMMENT_TEXT": comment,
			"SECONDS": strconv.Itoa(secs), "MINUTES": strconv.Itoa(secs / 60), "SOURCE": portalTimeSource[source],
			"CREATED_DATE": created.Format(time.RFC3339), "DATE_START": created.Add(-time.Duration(secs) * time.Second).Format(time.RFC3339),
			"DATE_STOP": created.Format(time.RFC3339)})
		return nil
	})
	return out, err
}

// portalTimeTask is the task of TASKID ("" when none is given: the entries of all the tasks the caller sees).
func (s *Server) portalTimeTask(c portalCall) (string, error) {
	v := strings.TrimSpace(portalStr(portalArg(c.in, 0, "TASKID", "taskId")))
	if v == "" || v == "0" {
		return "", nil
	}
	return s.portalTaskID(c, v)
}

// task.elapseditem.getlist: TASKID (or none), ORDER, FILTER (ID, USER_ID, TASK_ID, CREATED_DATE, SECONDS with the
// operators), 50 a page.
func (s *Server) portalTimeList(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTimeTask(c)
	if err != nil {
		return portalOut{}, err
	}
	args := []any{c.p.UserID, tasks.IsAdmin(ctx)}
	where := []string{`EXISTS (SELECT 1 FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = e.task_id AND ` + tasks.VisibleSQL("$1", "$2") + `)`}
	if tid != "" {
		args = append(args, tid)
		where = append(where, "e.task_id = $3")
	}
	place := 2
	if tid == "" {
		place = 1 // getlist(ORDER, FILTER) without a task
	}
	cols := map[string]string{"ID": "e.num", "USER_ID": "u.num", "TASK_ID": "tk.num", "CREATED_DATE": "e.spent_on", "SECONDS": "e.seconds"}
	for k, v := range portalMap(portalArg(c.in, place, "FILTER")) {
		op := strings.TrimRight(k, "ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz")
		col, ok := cols[strings.ToUpper(k[len(op):])]
		if !ok || !slices.Contains([]string{"", "=", "!", ">", "<", ">=", "<="}, op) {
			continue
		}
		cast := "::bigint"
		if col == "e.spent_on" {
			t, err := portalDateTime(portalStr(v))
			if err != nil || t == nil {
				return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "CREATED_DATE")
			}
			args = append(args, t.Format(time.DateOnly))
			cast = "::date"
		} else {
			args = append(args, portalStr(v))
		}
		switch op {
		case "":
			op = "="
		case "!":
			op = "<>"
		}
		where = append(where, col+" "+op+" $"+strconv.Itoa(len(args))+cast)
	}
	order := "e.num"
	for k, v := range portalMap(portalArg(c.in, place-1, "ORDER")) {
		if col, ok := cols[strings.ToUpper(k)]; ok {
			dir := " ASC"
			if strings.EqualFold(portalStr(v), "desc") {
				dir = " DESC"
			}
			order = col + dir + ", e.num"
		}
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM time_entries e JOIN tasks tk ON tk.id = e.task_id JOIN users u ON u.id = e.user_id
		WHERE `+cond, args...).Scan(&total); err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	list, err := s.portalTimeRows(ctx, cond, order, args, start, portalPage)
	if err != nil {
		return portalOut{}, err
	}
	return portalPaged(list, start, len(list), total), nil
}

// portalTimeItem is the go24 id of the entry ITEMID of the task.
func (s *Server) portalTimeItem(c portalCall, taskID string) (string, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM time_entries WHERE task_id = $1 AND num::text = $2`, taskID,
		strings.TrimSpace(portalStr(portalArg(c.in, 1, "ITEMID", "itemId")))).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return id, err
}

func (s *Server) portalTimeGet(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalTimeItem(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalTimeRows(c.r.Context(), "e.id = $1", "e.num", []any{id}, 0, 0)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0]}, nil
}

// portalTimeFields reads ARFIELDS over the entry's current values: SECONDS (or MINUTES), COMMENT_TEXT, CREATED_DATE.
// USER_ID may name only the caller: one logs one's own time.
func (s *Server) portalTimeFields(c portalCall, place int, secs int, day time.Time, comment string) (int, time.Time, string, error) {
	f := portalMap(portalArg(c.in, place, "ARFIELDS", "fields"))
	if v, ok := portalParamOK(f, "USER_ID"); ok {
		id, err := s.portalNum(c.r.Context(), "users", v)
		if err != nil || id != "" && id != c.p.UserID {
			return 0, day, comment, tasks.ErrForbidden
		}
	}
	if v, ok := portalParamOK(f, "SECONDS"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(portalStr(v)))
		if err != nil {
			return 0, day, comment, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "SECONDS")
		}
		secs = n
	} else if v, ok := portalParamOK(f, "MINUTES"); ok {
		n, err := strconv.Atoi(strings.TrimSpace(portalStr(v)))
		if err != nil {
			return 0, day, comment, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "MINUTES")
		}
		secs = n * 60
	}
	if v, ok := portalParamOK(f, "COMMENT_TEXT"); ok {
		comment = portalStr(v)
	}
	if v, ok := portalParamOK(f, "CREATED_DATE"); ok && strings.TrimSpace(portalStr(v)) != "" {
		t, err := portalDateTime(portalStr(v))
		if err != nil {
			return 0, day, comment, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "CREATED_DATE")
		}
		day = t.In(time.Local)
	}
	return secs, day, comment, nil
}

// task.elapseditem.add: TASKID, ARFIELDS — the number of the new entry.
func (s *Server) portalTimeAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	secs, day, comment, err := s.portalTimeFields(c, 1, 0, time.Now(), "")
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Tasks.AddTime(ctx, c.p.UserID, tid, secs, day, comment); err != nil {
		return portalOut{}, err
	}
	var num int64
	err = s.DB.QueryRow(ctx, `SELECT max(num) FROM time_entries WHERE task_id = $1 AND user_id = $2`, tid, c.p.UserID).Scan(&num)
	return portalOut{Result: num}, err
}

// task.elapseditem.update: TASKID, ITEMID, ARFIELDS.
func (s *Server) portalTimeUpdate(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalTimeItem(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	var secs int
	var day time.Time
	var comment string
	if err := s.DB.QueryRow(ctx, `SELECT seconds, spent_on, comment FROM time_entries WHERE id = $1`, id).Scan(&secs, &day, &comment); err != nil {
		return portalOut{}, err
	}
	y, m, d := day.Date()
	if secs, day, comment, err = s.portalTimeFields(c, 2, secs, time.Date(y, m, d, 12, 0, 0, 0, time.Local), comment); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.UpdateTime(ctx, c.p.UserID, tid, id, secs, day, comment)
}

func (s *Server) portalTimeDelete(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "TASKID", "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalTimeItem(c, tid)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.DeleteTime(c.r.Context(), c.p.UserID, tid, id)
}

// ---------- task.stages ----------

// portalSystemType is SYSTEM_TYPE of a group's stage: the first and the last are, the rest are the group's.
var portalSystemType = map[string]string{"new": "NEW", "finish": "FINISH"}

// portalTaskStage is a stage known by its number: of a group (Project is set) or of the caller's «Мой план».
type portalTaskStage struct {
	ID, Code, Title, Color string
	Project                *tasks.Project
}

func (s *Server) portalStageOf(c portalCall, v any) (portalTaskStage, error) {
	ctx := c.r.Context()
	n := strings.TrimSpace(portalStr(v))
	var st portalTaskStage
	var key, owner string
	err := s.DB.QueryRow(ctx, `SELECT x.id::text, coalesce(x.code, ''), x.title, x.color, p.key FROM project_stages x
		JOIN projects p ON p.id = x.project_id WHERE x.num::text = $1`, n).Scan(&st.ID, &st.Code, &st.Title, &st.Color, &key)
	if err == nil {
		p, err := s.Tasks.ProjectByKey(ctx, c.p.UserID, key)
		if err != nil {
			return st, err
		}
		st.Project = &p
		return st, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return st, err
	}
	err = s.DB.QueryRow(ctx, `SELECT id::text, coalesce(code, ''), title, user_id::text FROM plan_stages WHERE num::text = $1`, n).
		Scan(&st.ID, &st.Code, &st.Title, &owner)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && owner != c.p.UserID { // plans are personal
		return st, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return st, err
}

// portalStageEntity is the group of entityId (ENTITY_ID), or nil for 0 — the caller's «Мой план».
func (s *Server) portalStageEntity(c portalCall, v any) (*tasks.Project, error) {
	if n := strings.TrimSpace(portalStr(v)); n == "" || n == "0" {
		return nil, nil
	}
	p, err := s.portalGroupNum(c, v, false)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// task.stages.get: entityId — {"ID": {ID, TITLE, SORT, COLOR, SYSTEM_TYPE, ENTITY_ID, ENTITY_TYPE}} in their order.
func (s *Server) portalStagesGet(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	p, err := s.portalStageEntity(c, portalArg(c.in, 0, "entityId"))
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	var group string
	if p != nil {
		if err := s.DB.QueryRow(ctx, `SELECT num::text FROM projects WHERE id = $1`, p.ID).Scan(&group); err != nil {
			return portalOut{}, err
		}
	}
	add := func(i int, num, title, color, system, entity, kind string) {
		out[num] = map[string]any{"ID": num, "TITLE": title, "SORT": strconv.Itoa((i + 1) * 100), "COLOR": color,
			"SYSTEM_TYPE": system, "ENTITY_ID": entity, "ENTITY_TYPE": kind}
	}
	if p != nil {
		list, err := s.Tasks.Stages(ctx, p.ID)
		if err != nil {
			return portalOut{}, err
		}
		nums, err := s.portalNums(ctx, "project_stages", stageIDs(list))
		if err != nil {
			return portalOut{}, err
		}
		for i, st := range list {
			title := st.Title
			if title == "" {
				title = s.I18n.T(lang, "stage."+st.Code)
			}
			add(i, strconv.FormatInt(nums[st.ID], 10), title, st.Color, portalSystemType[st.Code], group, "G")
		}
		return portalOut{Result: out}, nil
	}
	cols, err := s.Tasks.Plan(ctx, c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	var ids []string
	for _, col := range cols {
		if col.ID != "" {
			ids = append(ids, col.ID)
		}
	}
	nums, err := s.portalNums(ctx, "plan_stages", ids)
	if err != nil {
		return portalOut{}, err
	}
	var me string
	if err := s.DB.QueryRow(ctx, `SELECT num::text FROM users WHERE id = $1`, c.p.UserID).Scan(&me); err != nil {
		return portalOut{}, err
	}
	for i, col := range slices.DeleteFunc(cols, func(x tasks.PlanColumn) bool { return x.ID == "" }) {
		title := col.Title
		if col.Code != "" {
			title = s.I18n.T(lang, "plan.stage."+col.Code)
		}
		add(i, strconv.FormatInt(nums[col.ID], 10), title, tasks.StageColors[0], "", me, "U")
	}
	return portalOut{Result: out}, nil
}

func stageIDs(list []tasks.Stage) []string {
	out := make([]string, 0, len(list))
	for _, st := range list {
		out = append(out, st.ID)
	}
	return out
}

// task.stages.add: fields {TITLE, COLOR, ENTITY_ID} — the number of the new stage (a group's comes before «Сделаны»).
func (s *Server) portalStageAdd(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	f := portalMap(portalArg(c.in, 0, "fields"))
	p, err := s.portalStageEntity(c, portalParam(f, "ENTITY_ID"))
	if err != nil {
		return portalOut{}, err
	}
	title, color := portalStr(portalParam(f, "TITLE")), portalStr(portalParam(f, "COLOR"))
	var num int64
	if p != nil {
		if err := s.Tasks.AddProjectStage(ctx, *p, title, color); err != nil {
			return portalOut{}, err
		}
		err = s.DB.QueryRow(ctx, `SELECT max(num) FROM project_stages WHERE project_id = $1`, p.ID).Scan(&num)
	} else {
		if err := s.Tasks.AddStage(ctx, c.p.UserID, title); err != nil {
			return portalOut{}, err
		}
		err = s.DB.QueryRow(ctx, `SELECT max(num) FROM plan_stages WHERE user_id = $1`, c.p.UserID).Scan(&num)
	}
	return portalOut{Result: num}, err
}

// task.stages.update: id, fields {TITLE, COLOR}.
func (s *Server) portalStageUpdate(c portalCall) (portalOut, error) {
	st, err := s.portalStageOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	f := portalMap(portalArg(c.in, 1, "fields"))
	title, color := st.Title, st.Color
	if v, ok := portalParamOK(f, "TITLE"); ok {
		title = portalStr(v)
	}
	if v, ok := portalParamOK(f, "COLOR"); ok {
		color = portalStr(v)
	}
	if st.Project == nil {
		if title == st.Title {
			return portalOut{Result: true}, nil
		}
		return portalOut{Result: true}, s.Tasks.RenameStage(c.r.Context(), c.p.UserID, st.ID, title)
	}
	if title == "" { // a built-in stage keeps the name of the locale
		title = s.I18n.T(s.I18n.Match(c.r.Header.Get("Accept-Language")), "stage."+st.Code)
	}
	return portalOut{Result: true}, s.Tasks.UpdateProjectStage(c.r.Context(), *st.Project, st.ID, title, color)
}

func (s *Server) portalStageDelete(c portalCall) (portalOut, error) {
	st, err := s.portalStageOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if st.Project == nil {
		return portalOut{Result: true}, s.Tasks.DeleteStage(c.r.Context(), c.p.UserID, st.ID)
	}
	return portalOut{Result: true}, s.Tasks.DeleteProjectStage(c.r.Context(), *st.Project, st.ID)
}

// task.stages.canmovetask: entityId, entityType (G — a group, U — one's plan).
func (s *Server) portalStageCanMove(c portalCall) (portalOut, error) {
	if strings.EqualFold(portalStr(portalArg(c.in, 1, "entityType")), "U") {
		var me string
		if err := s.DB.QueryRow(c.r.Context(), `SELECT num::text FROM users WHERE id = $1`, c.p.UserID).Scan(&me); err != nil {
			return portalOut{}, err
		}
		id := strings.TrimSpace(portalStr(portalArg(c.in, 0, "entityId")))
		return portalOut{Result: id == "0" || id == me}, nil
	}
	p, err := s.portalStageEntity(c, portalArg(c.in, 0, "entityId"))
	if err != nil || p == nil {
		return portalOut{Result: false}, nil
	}
	return portalOut{Result: p.CanWork()}, nil
}

// task.stages.movetask: id (of the task), stageId — a stage of the task's group (as dragging on the kanban: «Сделаны»
// completes it) or of one's plan. The task goes to the top of a group's stage and the end of a plan's.
func (s *Server) portalStageMove(c portalCall) (portalOut, error) {
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	st, err := s.portalStageOf(c, portalArg(c.in, 1, "stageId"))
	if err != nil {
		return portalOut{}, err
	}
	if st.Project == nil {
		return portalOut{Result: true}, s.Tasks.MovePlan(c.r.Context(), c.p.UserID, tid, st.ID)
	}
	return portalOut{Result: true}, s.Tasks.MoveToStage(c.r.Context(), c.p.UserID, tid, st.ID)
}

// ---------- task.dependence ----------

// portalLinkTypes: linkType — 0 start-start, 1 start-finish, 2 finish-start (the default), 3 finish-finish.
var portalLinkTypes = map[string]string{"0": "start_start", "1": "start_finish", "2": "finish_start", "3": "finish_finish"}

// portalDependence: taskIdFrom — the predecessor, taskIdTo — the task that depends on it.
func (s *Server) portalDependence(c portalCall) (from, fromKey, to string, err error) {
	if from, err = s.portalTaskID(c, portalArg(c.in, 0, "taskIdFrom")); err != nil {
		return
	}
	if to, err = s.portalTaskID(c, portalArg(c.in, 1, "taskIdTo")); err != nil {
		return
	}
	err = s.DB.QueryRow(c.r.Context(), `SELECT p.key || '-' || t.number FROM tasks t JOIN projects p ON p.id = t.project_id
		WHERE t.id = $1`, from).Scan(&fromKey)
	return
}

func (s *Server) portalDependenceAdd(c portalCall) (portalOut, error) {
	_, fromKey, to, err := s.portalDependence(c)
	if err != nil {
		return portalOut{}, err
	}
	typ := "finish_start"
	if v := strings.TrimSpace(portalStr(portalArg(c.in, 2, "linkType"))); v != "" {
		var ok bool
		if typ, ok = portalLinkTypes[v]; !ok {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "linkType")
		}
	}
	return portalOut{Result: true}, s.Tasks.AddLink(c.r.Context(), c.p.UserID, to, fromKey, typ)
}

func (s *Server) portalDependenceDelete(c portalCall) (portalOut, error) {
	from, _, to, err := s.portalDependence(c)
	if err != nil {
		return portalOut{}, err
	}
	var id string
	err = s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM task_links WHERE from_task = $1 AND to_task = $2`, from, to).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Tasks.RemoveLink(c.r.Context(), c.p.UserID, to, id)
}

// ---------- tasks.task.result ----------

// tasks.task.result.list: taskId — [{id, taskId, commentId, createdBy, createdAt, updatedAt, status, text, …}].
func (s *Server) portalResultList(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	rows, err := s.DB.Query(ctx, `SELECT m.num, tk.num, u.num, r.created_at, m.created_at, coalesce(m.edited_at, m.created_at), m.body
		FROM task_results r JOIN task_comments m ON m.id = r.comment_id JOIN tasks tk ON tk.id = r.task_id JOIN users u ON u.id = m.author_id
		WHERE r.task_id = $1 ORDER BY m.created_at`, tid)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	var num, task, author int64
	var marked, created, edited time.Time
	var body string
	_, err = pgx.ForEachRow(rows, []any{&num, &task, &author, &marked, &created, &edited, &body}, func() error {
		out = append(out, map[string]any{"id": num, "taskId": task, "commentId": num, "createdBy": author,
			"createdAt": created.In(time.Local).Format(time.RFC3339), "updatedAt": edited.In(time.Local).Format(time.RFC3339),
			"status": 0, "text": body, "formattedText": body, "files": []any{}})
		return nil
	})
	return portalOut{Result: out}, err
}

// tasks.task.result.addFromComment / deleteFromComment: commentId — «Добавить в результат» and back.
func portalResultMark(on bool) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		var id, tnum string
		err := s.DB.QueryRow(c.r.Context(), `SELECT m.id::text, t.num::text FROM task_comments m JOIN tasks t ON t.id = m.task_id
			WHERE m.num::text = $1`, strings.TrimSpace(portalStr(portalArg(c.in, 0, "commentId")))).Scan(&id, &tnum)
		if errors.Is(err, pgx.ErrNoRows) {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		}
		if err != nil {
			return portalOut{}, err
		}
		tid, err := s.portalTaskID(c, tnum)
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Tasks.SetResult(c.r.Context(), c.p.UserID, tid, id, on); err != nil {
			return portalOut{}, err
		}
		if !on {
			return portalOut{Result: true}, nil
		}
		list, err := s.portalResultList(portalCall{r: c.r, p: c.p, in: map[string]any{"taskId": tnum}, method: c.method})
		if err != nil {
			return portalOut{}, err
		}
		for _, r := range list.Result.([]map[string]any) {
			if strconv.FormatInt(r["commentId"].(int64), 10) == portalStr(portalArg(c.in, 0, "commentId")) {
				return portalOut{Result: r}, nil
			}
		}
		return portalOut{Result: true}, nil
	}
}

// ---------- tasks.task.history.list ----------

// portalHistoryFields: the fields of go24's history → those (the rest — in capitals).
var portalHistoryFields = map[string]string{"created": "NEW", "responsible": "RESPONSIBLE_ID", "start_plan": "START_DATE_PLAN",
	"end_plan": "END_DATE_PLAN", "accomplice": "ACCOMPLICES", "auditor": "AUDITORS", "tag": "TAGS", "rating": "MARK",
	"estimate": "TIME_ESTIMATE", "time": "TIME_SPENT_IN_LOGS", "predecessor": "DEPENDS_ON", "file": "FILES",
	"checklist": "CHECKLIST_ITEM_CREATE", "project": "GROUP_ID", "parent": "PARENT_ID", "control": "TASK_CONTROL"}

// portalHistoryStatus: a status in history → its number.
var portalHistoryStatus = map[string]string{"pending": "2", "in_progress": "3", "awaiting_control": "4", "completed": "5", "deferred": "6"}

// tasks.task.history.list: taskId, filter {FIELD}, order {ID, CREATED_DATE} — {"list": [{id, createdDate, field,
// value {from, to}, user}]}.
func (s *Server) portalHistoryList(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	tid, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId"))
	if err != nil {
		return portalOut{}, err
	}
	var want []string
	for k, v := range portalMap(portalArg(c.in, 1, "filter")) {
		if strings.EqualFold(strings.TrimLeft(k, "="), "FIELD") {
			want = append(want, portalList(v)...)
		}
	}
	dir := "ASC"
	for k, v := range portalMap(portalArg(c.in, 2, "order")) {
		if (strings.EqualFold(k, "ID") || strings.EqualFold(k, "CREATED_DATE")) && strings.EqualFold(portalStr(v), "desc") {
			dir = "DESC"
		}
	}
	all, err := s.portalHistoryRows(ctx, tid, dir)
	if err != nil {
		return portalOut{}, err
	}
	list := slices.DeleteFunc(all, func(r portalHistoryRow) bool {
		return len(want) > 0 && !slices.ContainsFunc(want, func(x string) bool { return strings.EqualFold(x, r.field) })
	})
	var nums []string
	for _, x := range list {
		nums = append(nums, x.user)
	}
	people, err := s.portalPeople(ctx, nums)
	if err != nil {
		return portalOut{}, err
	}
	out := make([]map[string]any, 0, len(list))
	opt := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	for _, x := range list {
		out = append(out, map[string]any{"id": strconv.FormatInt(x.id, 10), "createdDate": x.at.In(time.Local).Format(time.RFC3339),
			"field": x.field, "value": map[string]any{"from": opt(x.from), "to": opt(x.to)}, "user": people[x.user]})
	}
	return portalOut{Result: map[string]any{"list": out}}, nil
}

// ---------- tasks.task.counters.get ----------

// portalCounterRoles: type of tasks.task.counters.get → the tasks of the role ($1 — the employee).
var portalCounterRoles = map[string]string{
	"view_role_responsible": `t.responsible_id = $1`,
	"view_role_accomplice":  `EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'accomplice')`,
	"view_role_auditor":     `EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1 AND tp.role = 'auditor')`,
	"view_role_originator":  `(t.creator_id = $1 AND t.responsible_id <> $1)`,
}

// tasks.task.counters.get: userId (one's own; another's — in the admin mode), groupId, type (view_all or a role) —
// {"expired": {counter}, "new_comments": {counter}}.
func (s *Server) portalTaskCounters(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	user := c.p.UserID
	if v := portalArg(c.in, 0, "userId"); strings.TrimSpace(portalStr(v)) != "" {
		id, err := s.portalNum(ctx, "users", v)
		if err != nil {
			return portalOut{}, err
		}
		if id != "" && id != user && !tasks.IsAdmin(ctx) {
			return portalOut{}, tasks.ErrForbidden
		}
		if id != "" {
			user = id
		}
	}
	typ := strings.ToLower(strings.TrimSpace(portalStr(portalArg(c.in, 2, "type"))))
	role, ok := portalCounterRoles[typ]
	if !ok {
		if typ != "" && typ != "view_all" {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "type")
		}
		var all []string
		for _, k := range []string{"view_role_responsible", "view_role_accomplice", "view_role_auditor", "view_role_originator"} {
			all = append(all, portalCounterRoles[k])
		}
		role = "(" + strings.Join(all, " OR ") + ")"
	}
	args := []any{user}
	if p, err := s.portalStageEntity(c, portalArg(c.in, 1, "groupId")); err != nil {
		return portalOut{}, err
	} else if p != nil {
		args = append(args, p.ID)
		role += " AND t.project_id = $2"
	}
	var expired, comments int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE t.status <> 'completed' AND t.deadline < now()),
			count(*) FILTER (WHERE EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = $1 AND n.task_id = t.id
				AND n.read_at IS NULL AND n.kind IN ('comment', 'mention')))
		FROM tasks t WHERE `+role, args...).Scan(&expired, &comments)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"expired": map[string]any{"counter": expired, "code": "expired"},
		"new_comments": map[string]any{"counter": comments, "code": "new_comments"}}}, nil
}

// ---------- task.planner, startwatch ----------

// task.planner.getlist: the numbers of the tasks of the caller's plan of the day, in their order.
func (s *Server) portalPlannerList(c portalCall) (portalOut, error) {
	list, err := s.Tasks.DayPlan(c.r.Context(), c.p.UserID, time.Now())
	if err != nil {
		return portalOut{}, err
	}
	ids := make([]string, 0, len(list))
	for _, it := range list {
		ids = append(ids, it.ID)
	}
	nums, err := s.portalNums(c.r.Context(), "tasks_all", ids)
	if err != nil {
		return portalOut{}, err
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(nums[id], 10))
	}
	return portalOut{Result: out}, nil
}

// tasks.task.startwatch / stopwatch: taskId — «Стать наблюдателем» and back.
func portalTaskWatch(on bool) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		id, err := s.portalTaskID(c, portalArg(c.in, 0, "taskId", "id"))
		if err != nil {
			return portalOut{}, err
		}
		if err := s.Tasks.Watch(c.r.Context(), c.p.UserID, id, on); err != nil {
			return portalOut{}, err
		}
		return portalOut{Result: map[string]any{"task": true}}, nil
	}
}
