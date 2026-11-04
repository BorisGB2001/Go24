package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// REST compatible with the portal, the CRM's lists and pipelines changed (GO24DEV-976): crm.status.add/update/delete
// (the stages of leads, of the deal pipelines and of the smart processes' ones, the items of the dictionaries),
// crm.dealcategory.add/update/delete and crm.category.add/update/delete — by the administrators, as in the CRM's
// settings; crm.stagehistory.list — how the cards went through the stages (migration 940).

func init() {
	for name, m := range map[string]portalMethod{
		"crm.status.add":          {Scope: "crm:write", Run: (*Server).portalStatusAdd},
		"crm.status.update":       {Scope: "crm:write", Run: (*Server).portalStatusUpdate},
		"crm.status.delete":       {Scope: "crm:write", Run: (*Server).portalStatusDelete},
		"crm.dealcategory.add":    {Scope: "crm:write", Run: (*Server).portalDealCategoryAdd},
		"crm.dealcategory.update": {Scope: "crm:write", Run: (*Server).portalDealCategoryUpdate},
		"crm.dealcategory.delete": {Scope: "crm:write", Run: (*Server).portalDealCategoryDelete},
		"crm.category.fields":     {Scope: "crm:read", Run: (*Server).portalCategoryFields},
		"crm.category.add":        {Scope: "crm:write", Run: (*Server).portalCategoryAdd},
		"crm.category.update":     {Scope: "crm:write", Run: (*Server).portalCategoryUpdate},
		"crm.category.delete":     {Scope: "crm:write", Run: (*Server).portalCategoryDelete},
		"crm.stagehistory.list":   {Scope: "crm:read", Run: (*Server).portalStageHistory},
	} {
		portalMethods[name] = m
	}
}

// ---------- crm.status.add/update/delete ----------

// portalStageList is the list of stages ENTITY_ID names: entity "lead", or "deal" and the pipeline's go24 id; ok false —
// not a list of stages (a dictionary, or unknown).
func (s *Server) portalStageList(c portalCall, entityID string) (entity, pipeline string, ok bool, err error) {
	entityID = strings.ToUpper(strings.TrimSpace(entityID))
	if entityID == "STATUS" {
		return "lead", "", true, nil
	}
	typeID, num := "", "0"
	switch {
	case entityID == "DEAL_STAGE":
	case strings.HasPrefix(entityID, "DEAL_STAGE_"):
		num = strings.TrimPrefix(entityID, "DEAL_STAGE_")
	case strings.HasPrefix(entityID, "DYNAMIC_"):
		t, n, found := strings.Cut(strings.TrimPrefix(entityID, "DYNAMIC_"), "_STAGE_")
		tn, perr := strconv.ParseInt(t, 10, 64)
		if !found || perr != nil {
			return "", "", false, nil
		}
		types, err := s.portalSmartTypes(c.r.Context(), "num = $1", tn)
		if err != nil || len(types) == 0 {
			return "", "", false, err
		}
		typeID, num = types[0].ID, n
	default:
		return "", "", false, nil
	}
	p, err := s.portalPipelineIn(c, typeID, num)
	if err != nil {
		return "", "", false, err
	}
	return "deal", p.ID, true, nil
}

// portalDictKind is the go24 dictionary of ENTITY_ID ("" — none).
func portalDictKind(entityID string) string {
	for k, v := range portalDictEntities {
		if strings.EqualFold(v, strings.TrimSpace(entityID)) {
			return k
		}
	}
	return ""
}

// crm.status.add: fields {ENTITY_ID, NAME, SORT, COLOR, SEMANTICS} → the new item's ID. A stage is a working one
// (SEMANTICS "F" — a failure one) at its SORT; STATUS_ID is go24's own: UC_<ID>, as crm.status.list gives it.
func (s *Server) portalStatusAdd(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	fields := portalMap(portalParam(c.in, "fields"))
	entityID, name := portalStr(portalParam(fields, "ENTITY_ID")), portalStr(portalParam(fields, "NAME"))
	var num int64
	if kind := portalDictKind(entityID); kind != "" {
		id, err := s.CRM.AddDictItem(ctx, kind, name)
		if err != nil {
			return portalOut{}, err
		}
		err = s.DB.QueryRow(ctx, `SELECT num FROM crm_dict WHERE id::text = $1`, id).Scan(&num)
		return portalOut{Result: num}, err
	}
	entity, pipeline, ok, err := s.portalStageList(c, entityID)
	if err != nil {
		return portalOut{}, err
	}
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_status_entity", entityID)
	}
	semantic := "process"
	if strings.EqualFold(portalStr(portalParam(fields, "SEMANTICS")), "F") {
		semantic = "failure"
	}
	sort, _ := strconv.Atoi(portalStr(portalParam(fields, "SORT")))
	id, err := s.CRM.AddStageAt(ctx, entity, pipeline, name, portalStr(portalParam(fields, "COLOR")), semantic, sort)
	if err != nil {
		return portalOut{}, err
	}
	err = s.DB.QueryRow(ctx, `SELECT num FROM crm_stages WHERE id::text = $1`, id).Scan(&num)
	return portalOut{Result: num}, err
}

// portalStatusItem is the stage or the dictionary item numbered as the parameter id: its go24 id, name, color, code.
type portalStatusItem struct {
	ID, Name, Color, Code string
	Stage                 bool
}

func (s *Server) portalStatusOf(c portalCall) (portalStatusItem, error) {
	var x portalStatusItem
	n, _ := strconv.ParseInt(portalStr(portalArg(c.in, 0, "id")), 10, 64)
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text, name, color, coalesce(code, ''), true FROM crm_stages WHERE num = $1
		UNION ALL SELECT id::text, name, '', coalesce(code, ''), false FROM crm_dict WHERE num = $1`, n).
		Scan(&x.ID, &x.Name, &x.Color, &x.Code, &x.Stage)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return x, err
}

// crm.status.update: id, fields {NAME, COLOR, SORT} — a stage's name, color and place, a dictionary item's name.
func (s *Server) portalStatusUpdate(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	x, err := s.portalStatusOf(c)
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	fields := portalMap(portalParam(c.in, "fields"))
	name, named := portalParamOK(fields, "NAME")
	if !x.Stage {
		if named {
			err = s.CRM.RenameDictItem(ctx, x.ID, portalStr(name))
		}
		return portalOut{Result: true}, err
	}
	color, colored := portalParamOK(fields, "COLOR")
	if named || colored {
		n, col := x.Name, x.Color
		if named {
			n = portalStr(name)
		} else if n == "" { // a built-in stage named by go24: its name now stays
			n = s.tr(c.r, "crm.stage."+x.Code)
		}
		if colored {
			col = portalStr(color)
		}
		if err := s.CRM.UpdateStage(ctx, x.ID, n, col); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(fields, "SORT"); ok {
		sort, _ := strconv.Atoi(portalStr(v))
		err = s.CRM.SortStage(ctx, x.ID, sort)
	}
	return portalOut{Result: true}, err
}

// crm.status.delete: id, params {FORCED: "Y"} — a built-in item goes only so. A stage with cards
// stays: they are moved first.
func (s *Server) portalStatusDelete(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	x, err := s.portalStatusOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if x.Code != "" && !strings.EqualFold(portalStr(portalParam(portalMap(portalParam(c.in, "params")), "FORCED")), "Y") {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_status_system")
	}
	if x.Stage {
		err = s.CRM.DeleteStage(c.r.Context(), c.p.UserID, x.ID, "")
	} else {
		err = s.CRM.DeleteDictItem(c.r.Context(), x.ID)
	}
	return portalOut{Result: true}, err
}

// ---------- the pipelines: crm.dealcategory.* and crm.category.* changed ----------

// portalPipelineAdd adds a pipeline of the deals ("") or of a smart process, at its SORT (0 — the last).
func (s *Server) portalPipelineAdd(c portalCall, typeID, name string, sort any) (portalPipeline, error) {
	ctx := c.r.Context()
	id, err := s.CRM.AddPipelineOf(ctx, typeID, name)
	if err != nil {
		return portalPipeline{}, err
	}
	if n, _ := strconv.Atoi(portalStr(sort)); n > 0 {
		if err := s.CRM.SortPipeline(ctx, id, n); err != nil {
			return portalPipeline{}, err
		}
	}
	var num int64
	if err := s.DB.QueryRow(ctx, `SELECT num FROM crm_pipelines WHERE id::text = $1`, id).Scan(&num); err != nil {
		return portalPipeline{}, err
	}
	return s.portalPipelineIn(c, typeID, num)
}

// portalPipelineSet renames a pipeline and sets its SORT, those of the fields given.
func (s *Server) portalPipelineSet(c portalCall, p portalPipeline, fields map[string]any, nameKey, sortKey string) error {
	if v, ok := portalParamOK(fields, nameKey); ok {
		if err := s.CRM.RenamePipeline(c.r.Context(), p.ID, portalStr(v)); err != nil {
			return err
		}
	}
	if v, ok := portalParamOK(fields, sortKey); ok {
		n, _ := strconv.Atoi(portalStr(v))
		return s.CRM.SortPipeline(c.r.Context(), p.ID, n)
	}
	return nil
}

// portalPipelineDelete removes a pipeline without cards; the main one stays.
func (s *Server) portalPipelineDelete(c portalCall, typeID string, p portalPipeline) error {
	pipes, err := s.portalPipelinesOf(c.r, typeID)
	if err != nil {
		return err
	}
	if p.Num == pipes[0].Num {
		return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_category_default")
	}
	return s.CRM.DeletePipeline(c.r.Context(), p.ID)
}

// crm.dealcategory.add: fields {NAME, SORT} → the new pipeline's ID.
func (s *Server) portalDealCategoryAdd(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	p, err := s.portalPipelineAdd(c, "", portalStr(portalParam(fields, "NAME")), portalParam(fields, "SORT"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: p.Num}, nil
}

func (s *Server) portalDealCategoryUpdate(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	p, err := s.portalPipelineOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.portalPipelineSet(c, p, portalMap(portalParam(c.in, "fields")), "NAME", "SORT")
}

func (s *Server) portalDealCategoryDelete(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	p, err := s.portalPipelineOf(c, portalArg(c.in, 0, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.portalPipelineDelete(c, "", p)
}

var portalCategoryItemFields = []portalField{{Name: "id", Type: "integer", ReadOnly: true}, {Name: "name", Type: "string", Required: true},
	{Name: "sort", Type: "integer"}, {Name: "entityTypeId", Type: "integer", ReadOnly: true}, {Name: "isDefault", Type: "char", ReadOnly: true}}

func (s *Server) portalCategoryFields(c portalCall) (portalOut, error) {
	return portalOut{Result: map[string]any{"fields": s.portalFields(c, "portal.category.", portalCategoryItemFields...)}}, nil
}

// crm.category.add: entityTypeId, fields {name, sort} → {"category": …}; it has the stages of a new pipeline.
func (s *Server) portalCategoryAdd(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	e, err := s.portalCategoryType(c)
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	p, err := s.portalPipelineAdd(c, e.Type, portalStr(portalParam(fields, "name")), portalParam(fields, "sort"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"category": portalCategory(p, portalTypeNum(e), false)}}, nil
}

func (s *Server) portalCategoryUpdate(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	e, err := s.portalCategoryType(c)
	if err != nil {
		return portalOut{}, err
	}
	p, err := s.portalPipelineIn(c, e.Type, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalPipelineSet(c, p, portalMap(portalParam(c.in, "fields")), "name", "sort"); err != nil {
		return portalOut{}, err
	}
	return s.portalCategoryGet(c)
}

func (s *Server) portalCategoryDelete(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	e, err := s.portalCategoryType(c)
	if err != nil {
		return portalOut{}, err
	}
	p, err := s.portalPipelineIn(c, e.Type, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: []any{}}, s.portalPipelineDelete(c, e.Type, p)
}

// ---------- crm.stagehistory.list ----------

// portalHistoryOf is the stage history of the kind of entityTypeId (1 leads, 2 deals, 128 and above smart processes) as
// a list of crm.*.list: the row is x — with its card's responsible and pipeline, as the reading condition asks.
func (s *Server) portalHistoryOf(c portalCall) (portalEntity, error) {
	t := portalStr(portalParam(c.in, "entityTypeId"))
	n, _ := strconv.ParseInt(t, 10, 64)
	if n != 1 && n != 2 && n < portalFirstType {
		return portalEntity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_entity_type", t)
	}
	item, err := s.portalItemEntity(c, n)
	if err != nil {
		return portalEntity{}, err
	}
	e := portalEntity{Entity: item.Entity, Kind: item.Kind, Type: item.Type, TypeNum: item.TypeNum}
	fields := []portalField{
		{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
		{Name: "TYPE_ID", Type: "integer", Col: "x.kind::bigint", ReadOnly: true},
		{Name: "OWNER_ID", Type: "integer", Col: "x.owner", ReadOnly: true},
		{Name: "CREATED_TIME", Type: "datetime", Col: "x.created_at", ReadOnly: true},
	}
	if e.Entity == "lead" {
		e.From = `(SELECT h.num, h.card_id AS id, h.stage_id, h.kind, h.created_at, c.num AS owner, c.responsible_id
			FROM crm_stage_history h JOIN crm_leads c ON c.id = h.card_id WHERE h.entity = 'lead') x JOIN crm_stages st ON st.id = x.stage_id`
		e.Fields = append(fields, portalField{Name: "STATUS_SEMANTIC_ID", Type: "string", Col: portalSemantic, ReadOnly: true},
			portalField{Name: "STATUS_ID", Type: "crm_status", Col: "x.stage_id::text", ReadOnly: true})
		return e, nil
	}
	typ := "xp.type_id IS NULL"
	if e.Type != "" {
		typ = "xp.type_id = '" + e.Type + "'" // a uuid read from the base
	}
	e.From = `(SELECT h.num, h.card_id AS id, h.pipeline_id AS history_pipeline, h.stage_id, h.kind, h.created_at, c.num AS owner,
			c.responsible_id, c.pipeline_id
		FROM crm_stage_history h JOIN crm_deals c ON c.id = h.card_id WHERE h.entity = 'deal') x
		JOIN crm_pipelines xp ON xp.id = x.history_pipeline AND ` + typ + ` JOIN crm_stages st ON st.id = x.stage_id`
	e.Fields = append(fields, portalField{Name: "CATEGORY_ID", Type: "integer", Col: "xp.num", ReadOnly: true},
		portalField{Name: "STAGE_SEMANTIC_ID", Type: "string", Col: portalSemantic, ReadOnly: true},
		portalField{Name: "STAGE_ID", Type: "crm_status", Col: "x.stage_id::text", ReadOnly: true})
	return e, nil
}

// crm.stagehistory.list: entityTypeId, filter, order, select, start → {items: [{ID, TYPE_ID (1 created, 2 a working
// stage, 3 a final one, 5 another pipeline), OWNER_ID, CREATED_TIME, CATEGORY_ID, STAGE_SEMANTIC_ID, STAGE_ID}]} of
// the cards the caller may read; numbers are numbers here.
func (s *Server) portalStageHistory(c portalCall) (portalOut, error) {
	e, err := s.portalHistoryOf(c)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalCRMList(c, e)
	if err != nil {
		return portalOut{}, err
	}
	items := out.Result.([]map[string]any)
	for _, it := range items {
		for _, k := range []string{"ID", "TYPE_ID", "OWNER_ID", "CATEGORY_ID"} {
			if v, ok := it[k].(string); ok {
				it[k], _ = strconv.ParseInt(v, 10, 64)
			}
		}
	}
	out.Result = map[string]any{"items": items}
	return out, nil
}
