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

	"go24/internal/crm"
)

// REST compatible with the portal, the CRM's lists and deeds (GO24DEV-834): crm.status.* (the stages of leads and of
// each deal pipeline and the items of the dictionaries — sources, types, industries — in one list, as b_crm_status),
// crm.dealcategory.* and crm.category.* (the deal pipelines), crm.currency.*, crm.enum.*, crm.activity.* («Дела») and
// crm.timeline.comment.* (the comments of a card's timeline). Dictionary items and deeds have numbers (migration
// 663); a comment's number is the id of its timeline line.

func init() {
	for name, m := range map[string]portalMethod{
		"crm.status.fields":            {Scope: "crm:read", Run: (*Server).portalStatusFields},
		"crm.status.list":              {Scope: "crm:read", Run: (*Server).portalStatusList},
		"crm.status.get":               {Scope: "crm:read", Run: (*Server).portalStatusGet},
		"crm.status.entity.types":      {Scope: "crm:read", Run: (*Server).portalStatusTypes},
		"crm.status.entity.items":      {Scope: "crm:read", Run: (*Server).portalStatusItems},
		"crm.dealcategory.fields":      {Scope: "crm:read", Run: (*Server).portalDealCategoryFields},
		"crm.dealcategory.list":        {Scope: "crm:read", Run: (*Server).portalDealCategoryList},
		"crm.dealcategory.get":         {Scope: "crm:read", Run: (*Server).portalDealCategoryGet},
		"crm.dealcategory.default.get": {Scope: "crm:read", Run: (*Server).portalDealCategoryDefault},
		"crm.dealcategory.stage.list":  {Scope: "crm:read", Run: (*Server).portalDealCategoryStages},
		"crm.category.list":            {Scope: "crm:read", Run: (*Server).portalCategoryList},
		"crm.category.get":             {Scope: "crm:read", Run: (*Server).portalCategoryGet},
		"crm.currency.list":            {Scope: "crm:read", Run: (*Server).portalCurrencyList},
		"crm.currency.get":             {Scope: "crm:read", Run: (*Server).portalCurrencyGet},
		"crm.currency.base.get":        {Scope: "crm:read", Run: (*Server).portalCurrencyBase},
		"crm.enum.ownertype":           {Scope: "crm:read", Run: (*Server).portalEnumOwnerType},
		"crm.enum.activitytype":        {Scope: "crm:read", Run: (*Server).portalEnumActivityType},
		"crm.activity.fields":          {Scope: "crm:read", Run: func(s *Server, c portalCall) (portalOut, error) { return s.portalCRMFields(c, portalActivity) }},
		"crm.activity.list":            {Scope: "crm:read", Run: func(s *Server, c portalCall) (portalOut, error) { return s.portalCRMList(c, portalActivity) }},
		"crm.activity.get":             {Scope: "crm:read", Run: (*Server).portalActivityGet},
		"crm.activity.add":             {Scope: "crm:write", Run: (*Server).portalActivityAdd},
		"crm.activity.update":          {Scope: "crm:write", Run: (*Server).portalActivityUpdate},
		"crm.activity.delete":          {Scope: "crm:write", Run: (*Server).portalActivityDelete},
		"crm.timeline.comment.fields":  {Scope: "crm:read", Run: (*Server).portalTimelineFields},
		"crm.timeline.comment.list":    {Scope: "crm:read", Run: (*Server).portalTimelineList},
		"crm.timeline.comment.get":     {Scope: "crm:read", Run: (*Server).portalTimelineGet},
		"crm.timeline.comment.add":     {Scope: "crm:write", Run: (*Server).portalTimelineAdd},
		"crm.timeline.comment.update":  {Scope: "crm:write", Run: (*Server).portalTimelineUpdate},
		"crm.timeline.comment.delete":  {Scope: "crm:write", Run: (*Server).portalTimelineDelete},
	} {
		portalMethods[name] = m
	}
}

func portalYN(b bool) string {
	if b {
		return "Y"
	}
	return "N"
}

// portalFields describes fields as crm.*.fields does, the titles from the locale (prefix + name).
func (s *Server) portalFields(c portalCall, prefix string, fields ...portalField) map[string]any {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	for _, f := range fields {
		out[f.Name] = map[string]any{"type": f.Type, "isRequired": f.Required, "isReadOnly": f.ReadOnly, "isImmutable": false,
			"isMultiple": f.Multiple, "isDynamic": false, "title": s.I18n.T(lang, prefix+f.Name)}
	}
	return out
}

// ---------- crm.status.*: stages and dictionaries ----------

// portalDictEntities: a dictionary of go24 → its ENTITY_ID.
var portalDictEntities = map[string]string{"source": "SOURCE", "contact_type": "CONTACT_TYPE", "company_type": "COMPANY_TYPE",
	"industry": "INDUSTRY", "deal_type": "DEAL_TYPE"}

// portalStatus is an item of crm.status.list.
type portalStatus struct {
	ID                 int64
	EntityID, StatusID string
	Name, Init         string // its name, and the built-in one
	Sort               int
	System             bool
	Color, Semantic    string // a stage's: process, success, failure
	Category           int64  // a deal stage's pipeline
	Stage              bool
}

func (x portalStatus) out() map[string]any {
	m := map[string]any{"ID": strconv.FormatInt(x.ID, 10), "ENTITY_ID": x.EntityID, "STATUS_ID": x.StatusID, "NAME": x.Name,
		"NAME_INIT": x.Init, "SORT": strconv.Itoa(x.Sort), "SYSTEM": portalYN(x.System), "CATEGORY_ID": nil, "COLOR": nil, "SEMANTICS": nil}
	if x.Stage {
		m["COLOR"] = "#" + x.Color
		m["SEMANTICS"] = map[string]any{"success": "S", "failure": "F"}[x.Semantic]
		m["EXTRA"] = map[string]any{"SEMANTICS": x.Semantic, "COLOR": "#" + x.Color}
		if x.EntityID != "STATUS" {
			m["CATEGORY_ID"] = strconv.FormatInt(x.Category, 10)
		}
	}
	return m
}

// portalStageEntity is ENTITY_ID of the stages: of leads, of the main pipeline, of another one; of a smart process's
// pipeline — portalTypeStages.
func portalStageEntity(entity string, category int64) string {
	switch {
	case entity == "lead":
		return "STATUS"
	case category == 0:
		return "DEAL_STAGE"
	}
	return "DEAL_STAGE_" + strconv.FormatInt(category, 10)
}

// portalTypeStages is ENTITY_ID of the stages of a smart process's pipeline: DYNAMIC_128_STAGE_5.
func portalTypeStages(typ, category int64) string {
	return "DYNAMIC_" + strconv.FormatInt(typ, 10) + "_STAGE_" + strconv.FormatInt(category, 10)
}

// portalStatuses are all the stages (of leads, then of the pipelines) and the dictionaries' items.
func (s *Server) portalStatuses(r *http.Request) ([]portalStatus, error) {
	ctx := r.Context()
	rows, err := s.DB.Query(ctx, `SELECT st.num, st.entity, coalesce(st.code, ''), st.name, st.color, st.semantic, st.sort, coalesce(p.num, 0),
			coalesce(t.num, 0)
		FROM crm_stages st LEFT JOIN crm_pipelines p ON p.id = st.pipeline_id LEFT JOIN crm_types t ON t.id = p.type_id
		ORDER BY st.entity DESC, t.num NULLS FIRST, p.num, st.sort, st.num`)
	if err != nil {
		return nil, err
	}
	var out []portalStatus
	for rows.Next() {
		var x portalStatus
		var entity, code string
		var typ int64
		if err := rows.Scan(&x.ID, &entity, &code, &x.Name, &x.Color, &x.Semantic, &x.Sort, &x.Category, &typ); err != nil {
			rows.Close()
			return nil, err
		}
		x.Stage, x.System = true, code != ""
		if x.EntityID = portalStageEntity(entity, x.Category); typ > 0 {
			x.EntityID = portalTypeStages(typ, x.Category)
		}
		x.StatusID = portalStage{Code: code, Num: x.ID, Category: x.Category, Entity: entity, TypeNum: typ}.Portal()
		if x.Init = x.Name; code != "" {
			x.Init = s.tr(r, "crm.stage."+code)
		}
		if x.Name == "" {
			x.Name = x.Init
		}
		out = append(out, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(ctx, `SELECT num, kind, coalesce(code, id::text), code IS NOT NULL, name, sort FROM crm_dict ORDER BY kind, sort, num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x portalStatus
		var kind string
		if err := rows.Scan(&x.ID, &kind, &x.StatusID, &x.System, &x.Name, &x.Sort); err != nil {
			return nil, err
		}
		x.EntityID = portalDictEntities[kind]
		if x.Init = x.Name; x.System {
			x.Init = s.tr(r, "crm.dict."+x.StatusID)
		}
		if x.Name == "" {
			x.Name = x.Init
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

var portalStatusFields = []portalField{
	{Name: "ID", Type: "integer", ReadOnly: true}, {Name: "ENTITY_ID", Type: "string", Required: true},
	{Name: "STATUS_ID", Type: "string", Required: true}, {Name: "SORT", Type: "integer"}, {Name: "NAME", Type: "string", Required: true},
	{Name: "NAME_INIT", Type: "string", ReadOnly: true}, {Name: "SYSTEM", Type: "char", ReadOnly: true},
	{Name: "CATEGORY_ID", Type: "integer", ReadOnly: true}, {Name: "COLOR", Type: "string"}, {Name: "SEMANTICS", Type: "string", ReadOnly: true},
}

func (s *Server) portalStatusFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalFields(c, "portal.status.", portalStatusFields...)}, nil
}

// crm.status.list: filter by any of the fields (equals; a list — any of), order by one of them; all the items.
func (s *Server) portalStatusList(c portalCall) (portalOut, error) {
	all, err := s.portalStatuses(c.r)
	if err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(c.in, "filter"))
	out := []map[string]any{}
	for _, x := range all {
		m := x.out()
		if portalMatch(m, filter) {
			out = append(out, m)
		}
	}
	for k, v := range portalMap(portalParam(c.in, "order")) {
		k, desc := strings.ToUpper(k), strings.EqualFold(portalStr(v), "desc")
		slices.SortStableFunc(out, func(a, b map[string]any) int {
			x, y := portalStr(a[k]), portalStr(b[k])
			n := strings.Compare(x, y)
			if i, err := strconv.Atoi(x); err == nil {
				if j, err := strconv.Atoi(y); err == nil {
					n = i - j
				}
			}
			if desc {
				return -n
			}
			return n
		})
		break // one key: the map has no order
	}
	return portalOut{Result: out, Total: len(out), List: true}, nil
}

// portalMatch: the item has the values of the filter (FIELD or =FIELD; a list — any of), in any case.
func portalMatch(m map[string]any, filter map[string]any) bool {
	for k, v := range filter {
		got := portalStr(m[strings.ToUpper(strings.TrimPrefix(k, "="))])
		if !slices.ContainsFunc(portalList(v), func(x string) bool { return strings.EqualFold(x, got) }) {
			return false
		}
	}
	return true
}

func (s *Server) portalStatusGet(c portalCall) (portalOut, error) {
	all, err := s.portalStatuses(c.r)
	if err != nil {
		return portalOut{}, err
	}
	id := portalStr(portalParam(c.in, "id"))
	for _, x := range all {
		if strconv.FormatInt(x.ID, 10) == id {
			return portalOut{Result: x.out()}, nil
		}
	}
	return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
}

// portalPipeline is a deal pipeline: CATEGORY_ID is its number, the main one's 0.
type portalPipeline struct {
	ID      string
	Num     int64
	Name    string
	Sort    int
	Created time.Time
}

func (s *Server) portalPipelines(r *http.Request) ([]portalPipeline, error) {
	return s.portalPipelinesOf(r, "")
}

// portalPipelinesOf are the pipelines of the deals ("") or of a smart process, the first is the main one.
func (s *Server) portalPipelinesOf(r *http.Request, typeID string) ([]portalPipeline, error) {
	rows, err := s.DB.Query(r.Context(), `SELECT id::text, num, coalesce(code, ''), name, sort, created_at FROM crm_pipelines
		WHERE type_id IS NOT DISTINCT FROM nullif($1, '')::uuid ORDER BY num <> 0, sort, num`, typeID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (portalPipeline, error) {
		var p portalPipeline
		var code string
		err := row.Scan(&p.ID, &p.Num, &code, &p.Name, &p.Sort, &p.Created)
		if p.Name == "" {
			p.Name = s.tr(r, "crm.pipeline."+code)
		}
		return p, err
	})
}

// crm.status.entity.types: the lists — of the stages of leads and of every pipeline, and the dictionaries.
func (s *Server) portalStatusTypes(c portalCall) (portalOut, error) {
	pipes, err := s.portalPipelines(c.r)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{{"ID": "STATUS", "NAME": s.tr(c.r, "portal.list.STATUS"), "ENTITY_TYPE_ID": 1,
		"SEMANTIC_INFO": map[string]any{"START_FIELD": "NEW", "FINAL_SUCCESS_FIELD": "CONVERTED", "FINAL_UNSUCCESS_FIELD": "JUNK"}}}
	for _, k := range crm.DictKinds {
		out = append(out, map[string]any{"ID": portalDictEntities[k], "NAME": s.tr(c.r, "portal.list."+portalDictEntities[k])})
	}
	for _, p := range pipes {
		out = append(out, map[string]any{"ID": portalStageEntity("deal", p.Num), "NAME": s.tr(c.r, "portal.list.DEAL_STAGE", p.Name),
			"ENTITY_TYPE_ID": 2, "CATEGORY_ID": p.Num})
	}
	types, err := s.portalSmartTypes(c.r.Context(), "true")
	for _, t := range types {
		if pipes, err = s.portalPipelinesOf(c.r, t.ID); err != nil {
			break
		}
		for _, p := range pipes {
			out = append(out, map[string]any{"ID": portalTypeStages(t.Num, p.Num), "NAME": t.Name + " (" + p.Name + ")",
				"ENTITY_TYPE_ID": t.Num, "CATEGORY_ID": p.Num})
		}
	}
	return portalOut{Result: out}, err
}

// portalListItems are the items of a list (ENTITY_ID) as crm.status.entity.items gives them.
func (s *Server) portalListItems(c portalCall, entity string) (portalOut, error) {
	all, err := s.portalStatuses(c.r)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, x := range all {
		if strings.EqualFold(x.EntityID, entity) {
			out = append(out, map[string]any{"NAME": x.Name, "SORT": x.Sort, "STATUS_ID": x.StatusID})
		}
	}
	return portalOut{Result: out}, nil
}

func (s *Server) portalStatusItems(c portalCall) (portalOut, error) {
	return s.portalListItems(c, portalStr(portalArg(c.in, 0, "entityId")))
}

// ---------- the deal pipelines: crm.dealcategory.* and crm.category.* ----------

var portalCategoryFields = []portalField{{Name: "ID", Type: "integer", ReadOnly: true}, {Name: "CREATED_DATE", Type: "date", ReadOnly: true},
	{Name: "NAME", Type: "string", Required: true}, {Name: "IS_LOCKED", Type: "char", ReadOnly: true}, {Name: "SORT", Type: "integer"}}

func (s *Server) portalDealCategoryFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalFields(c, "portal.category.", portalCategoryFields...)}, nil
}

func portalDealCategory(p portalPipeline) map[string]any {
	return map[string]any{"ID": strconv.FormatInt(p.Num, 10), "CREATED_DATE": p.Created.In(time.Local).Format(time.RFC3339),
		"NAME": p.Name, "IS_LOCKED": "N", "SORT": strconv.Itoa(p.Sort)}
}

// portalPipelineOf is the pipeline numbered as the parameter (by its name or place), or not found.
func (s *Server) portalPipelineOf(c portalCall, v any) (portalPipeline, error) {
	return s.portalPipelineIn(c, "", v)
}

// portalPipelineIn is the pipeline numbered as the parameter of the deals ("") or of a smart process.
func (s *Server) portalPipelineIn(c portalCall, typeID string, v any) (portalPipeline, error) {
	pipes, err := s.portalPipelinesOf(c.r, typeID)
	if err != nil {
		return portalPipeline{}, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	if i := slices.IndexFunc(pipes, func(p portalPipeline) bool { return p.Num == n }); err == nil && i >= 0 {
		return pipes[i], nil
	}
	return portalPipeline{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
}

// crm.dealcategory.list: the pipelines but the main one gives them.
func (s *Server) portalDealCategoryList(c portalCall) (portalOut, error) {
	pipes, err := s.portalPipelines(c.r)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, p := range pipes {
		if m := portalDealCategory(p); p.Num != 0 && portalMatch(m, portalMap(portalParam(c.in, "filter"))) {
			out = append(out, m)
		}
	}
	return portalOut{Result: out, Total: len(out), List: true}, nil
}

func (s *Server) portalDealCategoryGet(c portalCall) (portalOut, error) {
	p, err := s.portalPipelineOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalDealCategory(p)}, nil
}

func (s *Server) portalDealCategoryDefault(c portalCall) (portalOut, error) {
	p, err := s.portalPipelineOf(c, 0)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"ID": 0, "NAME": p.Name}}, nil
}

func (s *Server) portalDealCategoryStages(c portalCall) (portalOut, error) {
	p, err := s.portalPipelineOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalListItems(c, portalStageEntity("deal", p.Num))
}

func portalCategory(p portalPipeline, typeNum int64, main bool) map[string]any {
	return map[string]any{"id": p.Num, "name": p.Name, "sort": p.Sort, "entityTypeId": typeNum, "isDefault": portalYN(main)}
}

// portalCategoryType is the kind crm.category.* are of: the deals (entityTypeId 2) or a smart process.
func (s *Server) portalCategoryType(c portalCall) (portalEntity, error) {
	t := portalStr(portalParam(c.in, "entityTypeId"))
	if n, _ := strconv.ParseInt(t, 10, 64); n == 2 || n >= portalFirstType {
		return s.portalItemEntity(c, n)
	}
	return portalEntity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_category_type", t)
}

func (s *Server) portalCategoryList(c portalCall) (portalOut, error) {
	e, err := s.portalCategoryType(c)
	if err != nil {
		return portalOut{}, err
	}
	pipes, err := s.portalPipelinesOf(c.r, e.Type)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for i, p := range pipes {
		out = append(out, portalCategory(p, portalTypeNum(e), i == 0))
	}
	return portalOut{Result: map[string]any{"categories": out}, Total: len(out), List: true}, nil
}

func (s *Server) portalCategoryGet(c portalCall) (portalOut, error) {
	e, err := s.portalCategoryType(c)
	if err != nil {
		return portalOut{}, err
	}
	pipes, err := s.portalPipelinesOf(c.r, e.Type)
	if err != nil {
		return portalOut{}, err
	}
	p, err := s.portalPipelineIn(c, e.Type, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"category": portalCategory(p, portalTypeNum(e), p.Num == pipes[0].Num)}}, nil
}

// ---------- crm.currency.*, crm.enum.* ----------

func (s *Server) portalCurrency(c portalCall, x crm.Currency) map[string]any {
	name := x.Name
	if key := "currency." + x.Code; name == "" {
		if name = s.tr(c.r, key); name == key {
			name = x.Code
		}
	}
	return map[string]any{"CURRENCY": x.Code, "AMOUNT_CNT": "1", "AMOUNT": strconv.FormatFloat(x.Rate, 'f', 4, 64), "BASE": portalYN(x.Base),
		"FULL_NAME": name, "DATE_UPDATE": x.UpdatedAt.In(time.Local).Format(time.RFC3339)}
}

func (s *Server) portalCurrencyList(c portalCall) (portalOut, error) {
	list, err := s.CRM.Currencies(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for i, x := range list {
		m := s.portalCurrency(c, x)
		m["SORT"] = strconv.Itoa((i + 1) * 10)
		out = append(out, m)
	}
	return portalOut{Result: out, Total: len(out), List: true}, nil
}

func (s *Server) portalCurrencyGet(c portalCall) (portalOut, error) {
	list, err := s.CRM.Currencies(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	id := portalStr(portalParam(c.in, "id"))
	for i, x := range list {
		if strings.EqualFold(x.Code, id) {
			m := s.portalCurrency(c, x)
			m["SORT"] = strconv.Itoa((i + 1) * 10)
			return portalOut{Result: m}, nil
		}
	}
	return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
}

func (s *Server) portalCurrencyBase(c portalCall) (portalOut, error) {
	list, err := s.CRM.Currencies(c.r.Context())
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0].Code}, nil // the base one is first
}

// portalOwners are the kinds of cards numbers them (OWNER_TYPE_ID, entityTypeId).
var portalOwners = []struct {
	ID                  int
	Entity, Code, Short string
}{{1, "lead", "LEAD", "L"}, {2, "deal", "DEAL", "D"}, {3, "contact", "CONTACT", "C"}, {4, "company", "COMPANY", "CO"}}

// portalOwnerEntity is the go24 entity of OWNER_TYPE_ID (2) or ENTITY_TYPE (deal, DEAL, D); "" — none of the four.
func portalOwnerEntity(v any) string {
	x := strings.TrimSpace(portalStr(v))
	for _, o := range portalOwners {
		if x == strconv.Itoa(o.ID) || strings.EqualFold(x, o.Entity) || x == o.Short {
			return o.Entity
		}
	}
	return ""
}

func (s *Server) portalEnumOwnerType(c portalCall) (portalOut, error) {
	out := []map[string]any{}
	for _, o := range portalOwners {
		out = append(out, map[string]any{"ID": o.ID, "NAME": s.tr(c.r, "portal.owner."+o.Entity), "SYMBOL_CODE": o.Code, "SYMBOL_CODE_SHORT": o.Short})
	}
	return portalOut{Result: out}, nil
}

// portalActivityTypes: TYPE_ID → the kind of a go24 deed (6 — a to-do, «CRM_TODO»).
var portalActivityTypes = []struct {
	ID   int
	Kind string
}{{1, "meeting"}, {2, "call"}, {6, "todo"}}

func (s *Server) portalEnumActivityType(c portalCall) (portalOut, error) {
	out := []map[string]any{}
	for _, t := range portalActivityTypes {
		out = append(out, map[string]any{"ID": t.ID, "NAME": s.tr(c.r, "crm.act."+t.Kind)})
	}
	return portalOut{Result: out}, nil
}

// portalOwnerSQL is the SQL of the number of the card (entity, id are the columns of its kind and id).
func portalOwnerSQL(entity, id string) string {
	q := "CASE " + entity
	for _, o := range portalOwners {
		q += " WHEN '" + o.Entity + "' THEN (SELECT oc.num FROM crm_" + portalEntities[o.Entity].Kind + " oc WHERE oc.id = " + id + ")"
	}
	return q + " END"
}

// portalOwnerTypeSQL is the SQL of OWNER_TYPE_ID of the entity column.
func portalOwnerTypeSQL(entity string) string {
	q := "CASE " + entity
	for _, o := range portalOwners {
		q += " WHEN '" + o.Entity + "' THEN " + strconv.Itoa(o.ID)
	}
	return q + " END::bigint"
}

// portalOwner is the card of OWNER_TYPE_ID (ENTITY_TYPE) and OWNER_ID (ENTITY_ID): its entity and go24 id; the caller
// may do the action on it.
func (s *Server) portalOwner(c portalCall, typ, num any, action string) (entity, id string, err error) {
	if entity = portalOwnerEntity(typ); entity == "" || portalStr(num) == "" {
		return "", "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_owner")
	}
	if id, err = s.portalCardID(c, portalEntities[entity], num); err != nil {
		return "", "", err
	}
	_, err = s.apiCRMItem(cardReq(c.r, portalEntities[entity].Kind, id), c.p, action)
	return entity, id, err
}

// ---------- crm.activity.*: «Дела» ----------

var portalActivity = portalEntity{Entity: "activity", Kind: "activities",
	From: `crm_activities x JOIN users ur ON ur.id = x.responsible_id JOIN users uc ON uc.id = x.created_by`,
	Fields: []portalField{
		{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
		{Name: "OWNER_TYPE_ID", Type: "crm_enum_ownertype", Col: portalOwnerTypeSQL("x.entity"), Required: true},
		{Name: "OWNER_ID", Type: "integer", Col: portalOwnerSQL("x.entity", "x.entity_id"), Required: true},
		{Name: "TYPE_ID", Type: "crm_enum_activitytype", Col: "CASE x.kind WHEN 'meeting' THEN 1 WHEN 'call' THEN 2 ELSE 6 END::bigint"},
		{Name: "PROVIDER_ID", Type: "string", Col: "CASE x.kind WHEN 'meeting' THEN 'CRM_MEETING' WHEN 'call' THEN 'VOXIMPLANT_CALL' ELSE 'CRM_TODO' END"},
		{Name: "PROVIDER_TYPE_ID", Type: "string", Col: "upper(x.kind)"},
		{Name: "SUBJECT", Type: "string", Col: "x.subject", Required: true},
		{Name: "DESCRIPTION", Type: "string", Col: "x.description"},
		{Name: "DESCRIPTION_TYPE", Type: "crm_enum_contenttype", Col: "'1'", ReadOnly: true},
		{Name: "DIRECTION", Type: "crm_enum_activitydirection", Col: "'0'", ReadOnly: true},
		{Name: "PRIORITY", Type: "crm_enum_activitypriority", Col: "'2'", ReadOnly: true},
		{Name: "COMPLETED", Type: "char", Col: "CASE WHEN x.done_at IS NULL THEN 'N' ELSE 'Y' END"},
		{Name: "STATUS", Type: "crm_enum_activitystatus", Col: "CASE WHEN x.done_at IS NULL THEN 1 ELSE 2 END::bigint", ReadOnly: true},
		{Name: "RESPONSIBLE_ID", Type: "user", Col: "ur.num"},
		{Name: "AUTHOR_ID", Type: "user", Col: "uc.num", ReadOnly: true},
		{Name: "EDITOR_ID", Type: "user", Col: "uc.num", ReadOnly: true},
		{Name: "START_TIME", Type: "datetime", Col: "x.deadline"},
		{Name: "END_TIME", Type: "datetime", Col: "x.deadline"},
		{Name: "DEADLINE", Type: "datetime", Col: "x.deadline"},
		{Name: "CREATED", Type: "datetime", Col: "x.created_at", ReadOnly: true},
		{Name: "LAST_UPDATED", Type: "datetime", Col: "coalesce(x.done_at, x.created_at)", ReadOnly: true},
	},
}

// portalActivityScope is the reading condition of deeds: those of the cards the viewer ($1) may read, and their own.
func (s *Server) portalActivityScope(ctx context.Context, viewer string) (string, error) {
	var parts []string
	for _, o := range portalOwners {
		sc, err := s.CRM.ReadScope(ctx, viewer, o.Entity, "oc", 1)
		if err != nil {
			return "", err
		}
		parts = append(parts, "(x.entity = '"+o.Entity+"' AND EXISTS (SELECT 1 FROM crm_"+portalEntities[o.Entity].Kind+
			" oc WHERE oc.id = x.entity_id AND ("+sc+")))")
	}
	return strings.Join(append(parts, "x.responsible_id::text = $1"), " OR "), nil
}

// portalDeed is the deed numbered as the parameter id that the caller may read — or change («write»: its responsible
// or who may change its card).
func (s *Server) portalDeed(c portalCall, action string) (crm.Activity, error) {
	var id string
	n, _ := strconv.ParseInt(portalStr(portalParam(c.in, "id")), 10, 64)
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM crm_activities WHERE num = $1`, n).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return crm.Activity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return crm.Activity{}, err
	}
	a, err := s.CRM.Activity(c.r.Context(), id)
	if err != nil || a.Responsible.ID == c.p.UserID {
		return a, err
	}
	_, err = s.apiCRMItem(cardReq(c.r, portalEntities[a.Entity].Kind, a.EntityID), c.p, action)
	return a, err
}

func (s *Server) portalActivityGet(c portalCall) (portalOut, error) {
	a, err := s.portalDeed(c, "read")
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalRows(c.r.Context(), portalActivity, "x.id::text = $1", "x.num", []any{a.ID}, 0, 0, portalActivity.Fields)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0]}, nil
}

// portalDeedFields applies the fields to a deed: SUBJECT, DESCRIPTION, TYPE_ID (PROVIDER_ID), RESPONSIBLE_ID,
// DEADLINE (END_TIME, START_TIME); done tells COMPLETED (nil — not given).
func (s *Server) portalDeedFields(c portalCall, a *crm.Activity, fields map[string]any) (done *bool, err error) {
	if v, ok := portalParamOK(fields, "SUBJECT"); ok {
		a.Subject = portalStr(v)
	}
	if v, ok := portalParamOK(fields, "DESCRIPTION"); ok {
		a.Description = portalStr(v)
	}
	if v, ok := portalParamOK(fields, "TYPE_ID"); ok {
		for _, t := range portalActivityTypes {
			if portalStr(v) == strconv.Itoa(t.ID) {
				a.Kind = t.Kind
			}
		}
	}
	if v, ok := portalParamOK(fields, "PROVIDER_ID"); ok {
		switch strings.ToUpper(portalStr(v)) {
		case "CRM_MEETING":
			a.Kind = "meeting"
		case "VOXIMPLANT_CALL", "CRM_CALL_TRACKER":
			a.Kind = "call"
		case "CRM_TODO":
			a.Kind = "todo"
		}
	}
	if v, ok := portalParamOK(fields, "RESPONSIBLE_ID"); ok {
		if a.Responsible.ID, err = s.portalNum(c.r.Context(), "users", v); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"START_TIME", "END_TIME", "DEADLINE"} { // the later wins: DEADLINE over END_TIME
		if v, ok := portalParamOK(fields, name); ok && portalStr(v) != "" {
			t, err := portalDateTime(portalStr(v))
			if err != nil {
				return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
			}
			a.Deadline = *t
		}
	}
	if v, ok := portalParamOK(fields, "COMPLETED"); ok {
		d := strings.EqualFold(portalStr(v), "Y")
		done = &d
	}
	return done, nil
}

// crm.activity.add: fields with OWNER_TYPE_ID and OWNER_ID of a card the caller may change → the deed's number.
func (s *Server) portalActivityAdd(c portalCall) (portalOut, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	entity, card, err := s.portalOwner(c, portalParam(fields, "OWNER_TYPE_ID"), portalParam(fields, "OWNER_ID"), "write")
	if err != nil {
		return portalOut{}, err
	}
	a := crm.Activity{Entity: entity, EntityID: card, Kind: "todo", Deadline: time.Now()}
	done, err := s.portalDeedFields(c, &a, fields)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	id, err := s.CRM.AddActivity(ctx, c.p.UserID, a)
	if err != nil {
		return portalOut{}, err
	}
	if done != nil && *done {
		if _, err := s.CRM.CompleteActivity(ctx, c.p.UserID, id); err != nil {
			return portalOut{}, err
		}
	}
	var num int64
	err = s.DB.QueryRow(ctx, `SELECT num FROM crm_activities WHERE id = $1`, id).Scan(&num)
	return portalOut{Result: num}, err
}

// crm.activity.update: id, fields — only the fields given; COMPLETED=Y completes the deed, N reopens it.
func (s *Server) portalActivityUpdate(c portalCall) (portalOut, error) {
	a, err := s.portalDeed(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	done, err := s.portalDeedFields(c, &a, fields)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	wasDone := a.DoneAt != nil
	if err := s.CRM.UpdateActivity(ctx, a, done == nil || *done); err != nil {
		return portalOut{}, err
	}
	if done != nil && *done && !wasDone {
		if _, err := s.CRM.CompleteActivity(ctx, c.p.UserID, a.ID); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}

func (s *Server) portalActivityDelete(c portalCall) (portalOut, error) {
	a, err := s.portalDeed(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	if err := s.CRM.DeleteActivity(c.r.Context(), a.ID); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// ---------- crm.timeline.comment.*: comments of a card's timeline ----------

var portalCommentFields = []portalField{{Name: "ID", Type: "integer", ReadOnly: true}, {Name: "CREATED", Type: "datetime", ReadOnly: true},
	{Name: "ENTITY_ID", Type: "integer", Required: true}, {Name: "ENTITY_TYPE", Type: "string", Required: true},
	{Name: "AUTHOR_ID", Type: "user", ReadOnly: true}, {Name: "COMMENT", Type: "string", Required: true},
	{Name: "FILES", Type: "attached_diskfile", Multiple: true, ReadOnly: true}}

func (s *Server) portalTimelineFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalFields(c, "portal.comment.", portalCommentFields...)}, nil
}

const portalCommentSQL = `SELECT t.id, t.created_at, t.entity, %s, u.num, t.body FROM crm_timeline t JOIN users u ON u.id = t.author_id
	WHERE t.kind = 'comment' AND `

// portalTimelineRows are comments of the condition (over t), ordered.
func (s *Server) portalTimelineRows(ctx context.Context, cond, order string, args ...any) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, strings.Replace(portalCommentSQL, "%s", portalOwnerSQL("t.entity", "t.entity_id"), 1)+cond+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var id, author int64
		var card *int64
		var at time.Time
		var entity, body string
		err := r.Scan(&id, &at, &entity, &card, &author, &body)
		m := map[string]any{"ID": strconv.FormatInt(id, 10), "CREATED": at.In(time.Local).Format(time.RFC3339), "ENTITY_TYPE": entity,
			"ENTITY_ID": nil, "AUTHOR_ID": strconv.FormatInt(author, 10), "COMMENT": body}
		if card != nil {
			m["ENTITY_ID"] = strconv.FormatInt(*card, 10)
		}
		return m, err
	})
}

// crm.timeline.comment.list: filter[ENTITY_TYPE] and filter[ENTITY_ID] — the comments of that card, newest first.
func (s *Server) portalTimelineList(c portalCall) (portalOut, error) {
	f := portalMap(portalParam(c.in, "filter"))
	entity, card, err := s.portalOwner(c, portalParam(f, "ENTITY_TYPE"), portalParam(f, "ENTITY_ID"), "read")
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE kind = 'comment' AND entity = $1 AND entity_id = $2`,
		entity, card).Scan(&total); err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	order := "t.id DESC"
	for k, v := range portalMap(portalParam(c.in, "order")) {
		if (strings.EqualFold(k, "ID") || strings.EqualFold(k, "CREATED")) && strings.EqualFold(portalStr(v), "asc") {
			order = "t.id"
		}
	}
	list, err := s.portalTimelineRows(ctx, `t.entity = $1 AND t.entity_id = $2`, order+` OFFSET `+strconv.Itoa(start)+` LIMIT `+
		strconv.Itoa(portalPage), entity, card)
	if err != nil {
		return portalOut{}, err
	}
	if list == nil {
		list = []map[string]any{}
	}
	return portalPaged(list, start, len(list), total), nil
}

// portalTimelineComment is the comment numbered as the parameter id, of a card the caller may read: its card and line.
func (s *Server) portalTimelineComment(c portalCall) (entity, card string, line int64, err error) {
	line, _ = strconv.ParseInt(portalStr(portalParam(c.in, "id")), 10, 64)
	err = s.DB.QueryRow(c.r.Context(), `SELECT entity, entity_id::text FROM crm_timeline WHERE id = $1 AND kind = 'comment'`, line).Scan(&entity, &card)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return "", "", 0, err
	}
	_, err = s.apiCRMItem(cardReq(c.r, portalEntities[entity].Kind, card), c.p, "read")
	return entity, card, line, err
}

func (s *Server) portalTimelineGet(c portalCall) (portalOut, error) {
	_, _, line, err := s.portalTimelineComment(c)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalTimelineRows(c.r.Context(), `t.id = $1`, `t.id`, line)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: list[0]}, nil
}

// crm.timeline.comment.add: fields ENTITY_TYPE, ENTITY_ID, COMMENT → the comment's number; its author is the caller.
func (s *Server) portalTimelineAdd(c portalCall) (portalOut, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	entity, card, err := s.portalOwner(c, portalParam(fields, "ENTITY_TYPE"), portalParam(fields, "ENTITY_ID"), "read")
	if err != nil {
		return portalOut{}, err
	}
	line, err := s.CRM.AddComment(c.r.Context(), c.p.UserID, entity, card, portalStr(portalParam(fields, "COMMENT")))
	return portalOut{Result: line}, err
}

// crm.timeline.comment.update: id, fields[COMMENT] — its author or an administrator → the comment's number.
func (s *Server) portalTimelineUpdate(c portalCall) (portalOut, error) {
	entity, card, line, err := s.portalTimelineComment(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.CRM.EditComment(c.r.Context(), c.p.UserID, entity, card, line, portalStr(portalParam(portalMap(portalParam(c.in, "fields")), "COMMENT"))); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: line}, nil
}

func (s *Server) portalTimelineDelete(c portalCall) (portalOut, error) {
	entity, card, line, err := s.portalTimelineComment(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.CRM.DeleteComment(c.r.Context(), c.p.UserID, entity, card, line); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}
