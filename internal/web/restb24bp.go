package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/bp"
	"go24/internal/crm"
)

// REST compatible with the portal, workflows (GO24DEV-855): bizproc.workflow.template.*, bizproc.workflow.* and
// bizproc.task.* by name. Templates and tasks have numbers (migration 720), a process is its uuid (WORKFLOW_ID of
// the portal is a string as well). A document is DOCUMENT_ID: ["crm", "CCrmDocumentDeal", "DEAL_12"] — a
// card by its number, ["lists", "BizprocDocument", "<element>"], ["disk", "Legacy\\Disk\\BizProcDocument", "<file>"]
// — by go24 id. The rights are those of go24 REST (apibp.go): the key owner's processes and tasks, an administrator's
// key administers.

func init() {
	for name, m := range map[string]portalMethod{
		"bizproc.workflow.template.list":   {Scope: "bp:read", Run: (*Server).portalBPTemplates},
		"bizproc.workflow.template.delete": {Scope: "bp:write", Run: (*Server).portalBPTemplateDelete},
		"bizproc.workflow.start":           {Scope: "bp:write", Run: (*Server).portalBPStart},
		"bizproc.workflow.terminate":       {Scope: "bp:write", Run: (*Server).portalBPTerminate},
		"bizproc.workflow.kill":            {Scope: "bp:write", Run: (*Server).portalBPKill},
		"bizproc.workflow.instances":       {Scope: "bp:read", Run: (*Server).portalBPInstances},
		"bizproc.workflow.instance.list":   {Scope: "bp:read", Run: (*Server).portalBPInstances},
		"bizproc.task.list":                {Scope: "bp:read", Run: (*Server).portalBPTasks},
		"bizproc.task.complete":            {Scope: "bp:write", Run: (*Server).portalBPComplete},
		"bizproc.task.delegate":            {Scope: "bp:write", Run: (*Server).portalBPDelegate},
	} {
		portalMethods[name] = m
	}
}

// portalDocKinds: a go24 entity of a template → the module and the document class.
var portalDocKinds = map[string][2]string{
	"deal":        {"crm", "CCrmDocumentDeal"},
	"lead":        {"crm", "CCrmDocumentLead"},
	"contact":     {"crm", "CCrmDocumentContact"},
	"company":     {"crm", "CCrmDocumentCompany"},
	bp.EntityFile: {"disk", `Legacy\Disk\BizProcDocument`},
	bp.EntityList: {"lists", "BizprocDocument"},
	"":            {"bizproc", `Legacy\Bizproc\Public\Entity\Document\Workflow`}, // a standalone process
}

const portalDynamic = `Legacy\Crm\Integration\BizProc\Document\Dynamic`

// portalDocType is DOCUMENT_TYPE of a template: the module, the class and the type (DEAL, DYNAMIC_<type>, iblock_<list>).
func portalDocType(entity, typeID, listID string) []string {
	k := portalDocKinds[entity]
	switch {
	case entity == "deal" && typeID != "":
		return []string{"crm", portalDynamic, "DYNAMIC_" + typeID}
	case crm.Entities[entity]:
		return []string{k[0], k[1], strings.ToUpper(entity)}
	case entity == bp.EntityList:
		return []string{k[0], k[1], "iblock_" + listID}
	case entity == bp.EntityFile:
		return []string{k[0], k[1], "STORAGE_COMMON"}
	}
	return []string{k[0], k[1], "WORKFLOW"}
}

// portalFieldTypes: a kind of a form's field → the type of a parameter.
var portalFieldTypes = map[string]string{"string": "string", "text": "text", "number": "double", "date": "date", "datetime": "datetime",
	"bool": "bool", "select": "select", "user": "user"}

// portalNums: go24 ids of the rows of a table (bp_templates, bp_approvals) → their numbers.
func (s *Server) portalNums(ctx context.Context, table string, ids []string) (map[string]int64, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, num FROM `+table+` WHERE id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// portalNumID is the go24 id of a template or a task by its number (a go24 uuid is taken too).
func (s *Server) portalNumID(c portalCall, table string, v any) (string, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM `+table+` WHERE num::text = $1 OR id::text = $1`, portalStr(v)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || portalStr(v) == "" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return id, err
}

// portalPick keeps the fields asked for (none or «*» — all), and ID always.
func portalPick(row map[string]any, sel []string) map[string]any {
	if len(sel) == 0 || slices.Contains(sel, "*") {
		return row
	}
	out := map[string]any{"ID": row["ID"]}
	for _, k := range sel {
		if v, ok := row[strings.ToUpper(k)]; ok {
			out[strings.ToUpper(k)] = v
		}
	}
	return out
}

// portalRowMatch: the row passes the filter (a list — any of); before the field «!» — not equal, «%» — contains, «!%» —
// does not, «>», «>=», «<», «<=» — compared as numbers, or as text (dates). The fields the rows do not have are skipped.
func portalRowMatch(row map[string]any, filter map[string]any) bool {
	for k, want := range filter {
		op := ""
		for _, o := range []string{"!%", ">=", "<=", "!=", "!", "=", ">", "<", "%"} {
			if strings.HasPrefix(k, o) {
				op, k = o, k[len(o):]
				break
			}
		}
		switch op {
		case "!=":
			op = "!"
		case "=":
			op = ""
		}
		k = strings.ToUpper(k)
		got, ok := row[k]
		if !ok {
			continue
		}
		g := portalStr(got)
		if l, ok := got.([]string); ok { // DOCUMENT_TYPE
			g = strings.Join(l, "|")
		}
		wants := portalList(want)
		if l, ok := want.([]any); ok && k == "DOCUMENT_TYPE" {
			wants = []string{strings.Join(portalList(l), "|")}
		}
		var pass func(w string) bool
		switch op {
		case "%", "!%":
			pass = func(w string) bool { return strings.Contains(strings.ToLower(g), strings.ToLower(w)) }
		case ">", ">=", "<", "<=":
			pass = func(w string) bool {
				r := portalCompare(g, w)
				return op == ">" && r > 0 || op == ">=" && r >= 0 || op == "<" && r < 0 || op == "<=" && r <= 0
			}
		default:
			pass = func(w string) bool { return strings.EqualFold(w, g) }
		}
		if slices.ContainsFunc(wants, pass) == strings.HasPrefix(op, "!") {
			return false
		}
	}
	return true
}

// portalCompare compares two values as numbers when both are, else as text.
func portalCompare(x, y string) int {
	nx, ex := strconv.ParseFloat(x, 64)
	ny, ey := strconv.ParseFloat(y, 64)
	if ex == nil && ey == nil {
		return cmp.Compare(nx, ny)
	}
	return strings.Compare(x, y)
}

// portalRowsPage cuts a page of rows: filtered, ordered by one field of order (ID by default), the fields of select.
func portalRowsPage(c portalCall, rows []map[string]any) portalOut {
	return portalRowsPageBy(c, rows, "ID", false)
}

// portalRowsPageBy is portalRowsPage ordered by the field key (desc — the other way) when order says nothing.
func portalRowsPageBy(c portalCall, rows []map[string]any, key string, desc bool) portalOut {
	filter := portalMap(portalParam(c.in, "filter"))
	var kept []map[string]any
	for _, row := range rows {
		if portalRowMatch(row, filter) {
			kept = append(kept, row)
		}
	}
	for k, v := range portalMap(portalParam(c.in, "order")) {
		key, desc = strings.ToUpper(k), strings.EqualFold(portalStr(v), "desc")
	}
	slices.SortStableFunc(kept, func(a, b map[string]any) int {
		r := portalCompare(portalStr(a[key]), portalStr(b[key]))
		if desc {
			return -r
		}
		return r
	})
	start := portalStart(c.in)
	sel := portalList(portalParam(c.in, "select"))
	page := []map[string]any{}
	for i := start; i < len(kept) && i < start+portalPage; i++ {
		page = append(page, portalPick(kept[i], sel))
	}
	return portalPaged(page, start, len(page), len(kept))
}

// ---------- templates ----------

// bizproc.workflow.template.list: the templates the caller may start (an administrator — all, the inactive ones too).
func (s *Server) portalBPTemplates(c portalCall) (portalOut, error) {
	ctx, admin := s.apiBPCtx(c.r, c.p)
	list, err := s.BP.Templates(c.r.Context(), "", !admin)
	if err == nil {
		list, err = s.BP.Startable(ctx, c.p.UserID, list)
	}
	if err != nil {
		return portalOut{}, err
	}
	ids := make([]string, 0, len(list))
	for _, t := range list {
		ids = append(ids, t.ID)
	}
	nums, err := s.portalNums(c.r.Context(), "bp_templates", ids)
	if err != nil {
		return portalOut{}, err
	}
	rows := []map[string]any{}
	for _, t := range list {
		params, vars, consts := map[string]any{}, map[string]any{}, map[string]any{}
		for _, f := range t.Fields {
			p := map[string]any{"Name": f.Label, "Description": "", "Type": portalFieldTypes[f.Kind], "Required": portalYN(f.Required),
				"Multiple": map[bool]string{true: "Y", false: "N"}[f.Multiple], "Default": f.Default}
			if f.Kind == "select" {
				opts := map[string]string{}
				for _, o := range f.Options {
					opts[o] = o
				}
				p["Options"] = opts
			}
			switch f.Scope {
			case "var":
				vars[f.Key] = p
			case "const":
				consts[f.Key] = p
			default:
				params[f.Key] = p
			}
		}
		auto := 0
		if t.AutoCreate {
			auto |= 1
		}
		if t.AutoChange {
			auto |= 2
		}
		dt := portalDocType(t.Entity, t.TypeID, t.ListID)
		rows = append(rows, map[string]any{"ID": strconv.FormatInt(nums[t.ID], 10), "XML_ID": t.ID, "MODULE_ID": dt[0], "ENTITY": dt[1],
			"DOCUMENT_TYPE": dt, "AUTO_EXECUTE": strconv.Itoa(auto), "NAME": t.Name, "DESCRIPTION": t.Description, "PARAMETERS": params,
			"VARIABLES": vars, "CONSTANTS": consts, "MODIFIED": t.CreatedAt.Format(time.RFC3339), "IS_MODIFIED": "N", "USER_ID": "",
			"SYSTEM_CODE": "", "ACTIVE": portalYN(t.Active)})
	}
	return portalRowsPage(c, rows), nil
}

// bizproc.workflow.template.delete: ID — the template goes with its processes (administrators).
func (s *Server) portalBPTemplateDelete(c portalCall) (portalOut, error) {
	if _, admin := s.apiBPCtx(c.r, c.p); !admin {
		return portalOut{}, bp.ErrForbidden
	}
	id, err := s.portalNumID(c, "bp_templates", portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.BP.DeleteTemplate(c.r.Context(), id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// ---------- processes ----------

// portalDocument is the go24 id of DOCUMENT_ID for a template of the entity want (none — a standalone
// process); a document of another kind is wrong.
func (s *Server) portalDocument(c portalCall, v any, want string) (string, error) {
	doc := portalList(v)
	bad := s.portalErr(c.r, http.StatusBadRequest, "ERROR_WRONG_DOCUMENT_ID", "err.portal_document")
	if len(doc) == 0 || doc[0] == "bizproc" {
		if want != "" {
			return "", bad
		}
		return "", nil
	}
	if len(doc) != 3 {
		return "", bad
	}
	kind, n, _ := strings.Cut(doc[2], "_")
	switch {
	case doc[0] == "crm" && kind == "DYNAMIC" && want == "deal": // DYNAMIC_<type>_<number>: a card of a smart process
		var id string
		if err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM crm_deals WHERE num::text = $1`, n[strings.LastIndex(n, "_")+1:]).Scan(&id); err != nil {
			return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		}
		return id, nil
	case doc[0] == "crm" && strings.ToLower(kind) == want && crm.Entities[want]:
		return s.portalCardID(c, portalEntities[want], n)
	case doc[0] == "lists" && want == bp.EntityList, doc[0] == "disk" && want == bp.EntityFile:
		return doc[2], nil
	}
	return "", bad
}

// bizproc.workflow.start: TEMPLATE_ID, DOCUMENT_ID (of a template of a document), PARAMETERS {key: value} → the id of
// the process. A parameter of an employee is user_<number>, a number or a go24 uuid; a list of values is joined by
// commas.
func (s *Server) portalBPStart(c portalCall) (portalOut, error) {
	tid, err := s.portalNumID(c, "bp_templates", portalParam(c.in, "template_id"))
	if err != nil {
		return portalOut{}, err
	}
	t, err := s.BP.Template(c.r.Context(), tid)
	if err != nil {
		return portalOut{}, err
	}
	entityID, err := s.portalDocument(c, portalParam(c.in, "document_id"), t.Entity)
	if err != nil {
		return portalOut{}, err
	}
	values := map[string]string{}
	params := portalMap(portalParam(c.in, "parameters"))
	for _, f := range t.Fields {
		v, ok := portalParamOK(params, f.Key)
		if !ok {
			continue
		}
		vals := portalList(v)
		if f.Kind == "user" {
			for i, u := range vals {
				if id, err := s.portalUserID(c, strings.TrimPrefix(u, "user_")); err == nil {
					vals[i] = id
				}
			}
		}
		if f.Kind == "bool" && len(vals) == 1 {
			vals[0] = map[string]string{"Y": "true", "N": "false", "1": "true", "0": "false"}[strings.ToUpper(vals[0])]
		}
		sep := ","
		if f.Multiple { // an array — the values of a multiple field
			sep = bp.Multi
		}
		values[f.Key] = strings.Join(vals, sep)
	}
	ctx, _ := s.apiBPCtx(c.r, c.p)
	id, err := s.BP.Start(ctx, c.p.UserID, t.ID, entityID, values)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: id}, nil
}

// portalUserID is the go24 id of an employee by number or uuid.
func (s *Server) portalUserID(c portalCall, v string) (string, error) {
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM users WHERE num::text = $1 OR id::text = $1`, v).Scan(&id)
	return id, err
}

// bizproc.workflow.terminate: ID, STATUS (the reason) — the process stops, as «Остановить».
func (s *Server) portalBPTerminate(c portalCall) (portalOut, error) {
	ctx, _ := s.apiBPCtx(c.r, c.p)
	if err := s.BP.Stop(ctx, c.p.UserID, portalStr(portalParam(c.in, "id")), portalStr(portalParam(c.in, "status"))); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// bizproc.workflow.kill: ID — the process goes with its history (administrators).
func (s *Server) portalBPKill(c portalCall) (portalOut, error) {
	ctx, _ := s.apiBPCtx(c.r, c.p)
	if err := s.BP.DeleteInstance(ctx, portalStr(portalParam(c.in, "id"))); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalProc is what the portal tells of a process: its template, document and starter as numbers.
type portalProc struct {
	TemplateNum             int64
	TemplateName            string
	Module, Entity, DocID   string
	StartedBy               int64
	Started                 time.Time
	Label, Status, Document string // the status «Установить статус» set; running…; the process's name
}

// portalProcs tells of the processes by their ids.
func (s *Server) portalProcs(ctx context.Context, ids []string) (map[string]portalProc, error) {
	rows, err := s.DB.Query(ctx, `SELECT i.id::text, t.num, t.name, i.entity, coalesce(t.type_id::text, ''), coalesce(i.entity_id::text, ''),
			coalesce(CASE i.entity WHEN 'deal' THEN (SELECT num FROM crm_deals WHERE id = i.entity_id) WHEN 'lead' THEN (SELECT num FROM crm_leads WHERE id = i.entity_id)
				WHEN 'contact' THEN (SELECT num FROM crm_contacts WHERE id = i.entity_id) WHEN 'company' THEN (SELECT num FROM crm_companies WHERE id = i.entity_id) END, 0),
			u.num, i.created_at, i.label, i.status, i.name
		FROM bp_instances i JOIN bp_templates t ON t.id = i.template_id JOIN users u ON u.id = i.started_by WHERE i.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	out := map[string]portalProc{}
	for rows.Next() {
		var id, entity, typeID, entityID string
		var cardNum int64
		var p portalProc
		if err := rows.Scan(&id, &p.TemplateNum, &p.TemplateName, &entity, &typeID, &entityID, &cardNum, &p.StartedBy, &p.Started, &p.Label,
			&p.Status, &p.Document); err != nil {
			return nil, err
		}
		dt := portalDocType(entity, typeID, "")
		p.Module, p.Entity, p.DocID = dt[0], dt[1], entityID
		if crm.Entities[entity] {
			p.DocID = strings.ToUpper(entity) + "_" + strconv.FormatInt(cardNum, 10)
			if typeID != "" {
				p.DocID = "DYNAMIC_" + typeID + "_" + strconv.FormatInt(cardNum, 10)
			}
		}
		out[id] = p
	}
	return out, rows.Err()
}

// bizproc.workflow.instances: the running processes — of everybody for an administrator, else those the caller started.
func (s *Server) portalBPInstances(c portalCall) (portalOut, error) {
	var list []bp.Instance
	var err error
	if _, admin := s.apiBPCtx(c.r, c.p); admin {
		list, err = s.BP.AllInstances(c.r.Context(), bp.Filter{Status: "running", Limit: 500})
	} else {
		list, err = s.BP.Instances(c.r.Context(), c.p.UserID, "", "")
	}
	if err != nil {
		return portalOut{}, err
	}
	ids := []string{}
	for _, x := range list {
		if x.Status == "running" {
			ids = append(ids, x.ID)
		}
	}
	procs, err := s.portalProcs(c.r.Context(), ids)
	if err != nil {
		return portalOut{}, err
	}
	rows := []map[string]any{}
	for _, id := range ids {
		p := procs[id]
		rows = append(rows, map[string]any{"ID": id, "MODIFIED": p.Started.Format(time.RFC3339), "OWNED_UNTIL": nil, "MODULE_ID": p.Module,
			"ENTITY": p.Entity, "DOCUMENT_ID": p.DocID, "STARTED": p.Started.Format(time.RFC3339), "STARTED_BY": strconv.FormatInt(p.StartedBy, 10),
			"TEMPLATE_ID": strconv.FormatInt(p.TemplateNum, 10), "WORKFLOW_STATE": p.Label})
	}
	return portalRowsPage(c, rows), nil
}

// ---------- tasks ----------

// portalActivities: a kind of task → the activity it is.
var portalActivities = map[string]string{"approve": "ApproveActivity", "review": "ReviewActivity", "request": "RequestInformationActivity"}

// bizproc.task.list: the tasks waiting for the caller (an administrator — for filter[USER_ID] too); with STATUS or
// USER_STATUS in the filter — the decided ones too.
func (s *Server) portalBPTasks(c portalCall) (portalOut, error) {
	filter := portalMap(portalParam(c.in, "filter"))
	user := c.p.UserID
	if u := portalStr(portalParam(filter, "user_id")); u != "" {
		id, err := s.portalUserID(c, u)
		if err != nil {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		}
		if _, admin := s.apiBPCtx(c.r, c.p); id != user && !admin {
			return portalOut{}, bp.ErrForbidden
		}
		user = id
	}
	// the decided tasks too when the filter asks about the statuses keeps them (USER_STATUS > 0)
	state := ""
	for k := range filter {
		if k := strings.ToLower(strings.TrimLeft(k, "!<>=%@")); k == "status" || k == "user_status" {
			state = "all"
		}
	}
	list, err := s.BP.UserApprovals(c.r.Context(), bp.TaskFilter{UserID: user, State: state})
	if err != nil {
		return portalOut{}, err
	}
	ids, procIDs := []string{}, []string{}
	for _, a := range list {
		ids, procIDs = append(ids, a.ID), append(procIDs, a.InstanceID)
	}
	nums, err := s.portalNums(c.r.Context(), "bp_approvals", ids)
	if err != nil {
		return portalOut{}, err
	}
	procs, err := s.portalProcs(c.r.Context(), procIDs)
	if err != nil {
		return portalOut{}, err
	}
	userNum, err := s.portalUserNum(c, user)
	if err != nil {
		return portalOut{}, err
	}
	rows := []map[string]any{}
	for _, a := range list {
		p := procs[a.InstanceID]
		activity := portalActivities[a.Kind]
		if a.Kind == "request" && a.CanReject {
			activity = "RequestInformationOptionalActivity"
		}
		fields := []map[string]any{}
		for _, f := range a.Fields {
			fields = append(fields, map[string]any{"Id": f.Key, "Name": f.Label, "Type": portalFieldTypes[f.Kind], "Required": portalYN(f.Required),
				"Multiple": map[bool]string{true: "Y", false: "N"}[f.Multiple], "Default": f.Default, "Options": f.Options})
		}
		// the step's comment required and buttons' texts, as the activities' properties
		params := map[string]any{"Fields": fields, "ShowComment": "Y",
			"CommentRequired": map[string]string{"": "N", "yes": "Y", "approve": "YA", "reject": "YR"}[a.Remark]}
		switch a.Kind {
		case "approve":
			params["TaskButton1Message"], params["TaskButton2Message"] = a.Yes, a.No
		case "review":
			params["TaskButtonMessage"] = a.Yes
		default:
			params["TaskOkMessage"], params["TaskCancelMessage"] = a.Yes, a.No
		}
		var overdue any
		if a.Due != nil {
			overdue = a.Due.Format(time.RFC3339)
		}
		// USER_STATUS: 0 waiting, 1 yes, 2 no, 3 ok (seen, answered), 4 cancel (a request rejected)
		status := map[string]string{"": "0", "approve": "1", "reject": "2", "seen": "3", "done": "3"}[a.Decision]
		if a.Kind == "request" && a.Decision == "reject" {
			status = "4"
		}
		modified := a.CreatedAt
		if a.DecidedAt != nil {
			modified = *a.DecidedAt
		}
		rows = append(rows, map[string]any{"ID": strconv.FormatInt(nums[a.ID], 10), "XML_ID": a.ID, "WORKFLOW_ID": a.InstanceID,
			"DOCUMENT_NAME": p.Document, "NAME": a.Name, "DESCRIPTION": a.Text, "MODIFIED": modified.Format(time.RFC3339),
			"WORKFLOW_STARTED": p.Started.Format(time.RFC3339), "WORKFLOW_STARTED_BY": strconv.FormatInt(p.StartedBy, 10), "OVERDUE_DATE": overdue,
			"WORKFLOW_TEMPLATE_ID": strconv.FormatInt(p.TemplateNum, 10), "WORKFLOW_TEMPLATE_NAME": p.TemplateName, "WORKFLOW_STATE": p.Label,
			"STATUS": status, "USER_ID": strconv.FormatInt(userNum, 10), "USER_STATUS": status, "USER_COMMENT": a.Comment, "MODULE_ID": p.Module, "ENTITY": p.Entity,
			"DOCUMENT_ID": p.DocID, "ACTIVITY": activity, "ACTIVITY_NAME": "A" + strconv.Itoa(a.Step+1), "PARAMETERS": params,
			"DOCUMENT_URL": s.BaseURL + "/bp/i/" + a.InstanceID})
	}
	for k := range filter { // taken above
		if strings.EqualFold(k, "user_id") {
			delete(filter, k)
		}
	}
	return portalRowsPage(c, rows), nil
}

// bizproc.task.complete: TASK_ID, STATUS — 1 or yes (approve), 2 or no (reject), 3 or ok (a review is seen, a request
// answered with FIELDS), 4 or cancel (a request rejected); COMMENT.
func (s *Server) portalBPComplete(c portalCall) (portalOut, error) {
	id, err := s.portalNumID(c, "bp_approvals", portalParam(c.in, "task_id"))
	if err != nil {
		return portalOut{}, err
	}
	var kind string
	if err := s.DB.QueryRow(c.r.Context(), `SELECT kind FROM bp_approvals WHERE id::text = $1`, id).Scan(&kind); err != nil {
		return portalOut{}, err
	}
	status := map[string]string{"1": "yes", "2": "no", "3": "ok", "4": "cancel"}[portalStr(portalParam(c.in, "status"))]
	if status == "" {
		status = strings.ToLower(portalStr(portalParam(c.in, "status")))
	}
	comment := portalStr(portalParam(c.in, "comment"))
	switch {
	case kind == "request" && status == "ok":
		values := map[string]string{}
		for k, v := range portalMap(portalParam(c.in, "fields")) {
			values[k] = strings.Join(portalList(v), ",")
		}
		err = s.BP.Answer(c.r.Context(), c.p.UserID, id, values, comment)
	case kind == "request" && (status == "no" || status == "cancel"), kind == "approve" && status == "no":
		err = s.BP.Decide(c.r.Context(), c.p.UserID, id, "reject", comment)
	case kind == "approve" && status == "yes":
		err = s.BP.Decide(c.r.Context(), c.p.UserID, id, "approve", comment)
	case kind == "review" && (status == "ok" || status == "yes"):
		err = s.BP.Decide(c.r.Context(), c.p.UserID, id, "seen", comment)
	default:
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_WRONG_STATUS", "err.portal_bp_status")
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// bizproc.task.delegate: TASK_IDS, FROM_USER_ID (the caller; an administrator — anyone), TO_USER_ID.
func (s *Server) portalBPDelegate(c portalCall) (portalOut, error) {
	ctx, admin := s.apiBPCtx(c.r, c.p)
	from := c.p.UserID
	if v := portalStr(portalParam(c.in, "from_user_id")); v != "" {
		id, err := s.portalUserID(c, v)
		if err != nil || id != from && !admin {
			return portalOut{}, bp.ErrForbidden
		}
		from = id
	}
	to, err := s.portalUserID(c, portalStr(portalParam(c.in, "to_user_id")))
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	for _, n := range portalList(portalParam(c.in, "task_ids")) {
		id, err := s.portalNumID(c, "bp_approvals", n)
		if err != nil {
			return portalOut{}, err
		}
		if _, err := s.BP.Delegate(ctx, from, id, to); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}
