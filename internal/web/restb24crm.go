package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
)

// REST compatible with the portal, CRM (GO24DEV-761): crm.deal.*, crm.lead.*, crm.contact.*, crm.company.* — fields,
// list, get, add, update, delete with the fields. Cards, pipelines (CATEGORY_ID) and custom stages have
// numbers (migration 619); a stage is its the portal code (NEW, C2:WON, UC_15). Changes go the way of go24 REST
// (crmCreate, crmUpdate, crmTrash): the same rights, robots and events.

// portalField is a field of a card names it.
type portalField struct {
	Name       string
	Type       string // as crm.*.fields gives it: integer, string, double, date, datetime, char, user, crm_status…
	Col        string // the SQL of the value (the card is x); "" — PHONE and EMAIL, read apart
	StatusType string // crm_status: the list of values (DEAL_STAGE, SOURCE…)
	ReadOnly   bool
	Required   bool
	Multiple   bool      // PHONE, EMAIL, WEB, CONTACT_IDS: given by get and when selected
	UF         *portalUF // a user field, UF_CRM_* (restb24crmuf.go)
}

// portalEntity is a kind of card for the portal.
type portalEntity struct {
	Entity, Kind string // go24's: deal and deals
	From         string // the card is x, its stage st
	Fields       []portalField
	Type         string // a smart process's cards (deals of its pipelines): its id and number (entityTypeId)
	TypeNum      int64
}

func (e portalEntity) field(name string) (portalField, bool) {
	i := slices.IndexFunc(e.Fields, func(f portalField) bool { return f.Name == name })
	if i < 0 {
		return portalField{}, false
	}
	return e.Fields[i], true
}

var portalCommon = []portalField{
	{Name: "ASSIGNED_BY_ID", Type: "user", Col: "ur.num"},
	{Name: "CREATED_BY_ID", Type: "user", Col: "uc.num", ReadOnly: true},
	{Name: "DATE_CREATE", Type: "datetime", Col: "x.created_at", ReadOnly: true},
	{Name: "DATE_MODIFY", Type: "datetime", Col: "x.updated_at", ReadOnly: true},
	{Name: "COMMENTS", Type: "string", Col: "x.comment"},
}

var portalPhones = []portalField{
	{Name: "HAS_PHONE", Type: "char", ReadOnly: true, Col: `CASE WHEN EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity_id = x.id
		AND m.kind = 'phone') THEN 'Y' ELSE 'N' END`},
	{Name: "HAS_EMAIL", Type: "char", ReadOnly: true, Col: `CASE WHEN EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity_id = x.id
		AND m.kind = 'email') THEN 'Y' ELSE 'N' END`},
	{Name: "PHONE", Type: "crm_multifield", Multiple: true},
	{Name: "EMAIL", Type: "crm_multifield", Multiple: true},
}

func portalUTM(table string) []portalField {
	var out []portalField
	for _, f := range []string{"source", "medium", "campaign", "content", "term"} {
		out = append(out, portalField{Name: "UTM_" + strings.ToUpper(f), Type: "string",
			Col: "coalesce((SELECT u." + f + " FROM mkt_utm u WHERE u." + table + "_id = x.id), '')"})
	}
	return out
}

const portalSemantic = `CASE st.semantic WHEN 'success' THEN 'S' WHEN 'failure' THEN 'F' ELSE 'P' END`

var portalEntities = map[string]portalEntity{
	"deal": {Entity: "deal", Kind: "deals",
		From: `crm_deals x JOIN crm_pipelines xp ON xp.id = x.pipeline_id AND xp.type_id IS NULL JOIN crm_stages st ON st.id = x.stage_id
			JOIN users ur ON ur.id = x.responsible_id JOIN users uc ON uc.id = x.created_by
			LEFT JOIN crm_companies xc ON xc.id = x.company_id LEFT JOIN crm_contacts xk ON xk.id = x.contact_id
			LEFT JOIN crm_dict jt ON jt.id = x.deal_type_id LEFT JOIN crm_dict js ON js.id = x.source_id`,
		Fields: slices.Concat([]portalField{
			{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
			{Name: "TITLE", Type: "string", Col: "x.title", Required: true},
			{Name: "TYPE_ID", Type: "crm_status", StatusType: "DEAL_TYPE", Col: "coalesce(jt.code, jt.id::text)"},
			{Name: "CATEGORY_ID", Type: "crm_category", Col: "xp.num"},
			{Name: "STAGE_ID", Type: "crm_status", StatusType: "DEAL_STAGE", Col: "x.stage_id::text"},
			{Name: "STAGE_SEMANTIC_ID", Type: "string", Col: portalSemantic, ReadOnly: true},
			{Name: "CLOSED", Type: "char", Col: "CASE WHEN st.semantic <> 'process' THEN 'Y' ELSE 'N' END", ReadOnly: true},
			{Name: "OPPORTUNITY", Type: "double", Col: "x.amount::float8"},
			{Name: "CURRENCY_ID", Type: "crm_currency", Col: "x.currency"},
			{Name: "COMPANY_ID", Type: "crm_company", Col: "xc.num"},
			{Name: "CONTACT_ID", Type: "crm_contact", Col: "xk.num"},
			{Name: "CONTACT_IDS", Type: "crm_contact", Multiple: true, Col: `(SELECT coalesce(array_agg(c.num ORDER BY c.id <> x.contact_id, dc.sort), '{}')
				FROM crm_deal_contacts dc JOIN crm_contacts c ON c.id = dc.contact_id WHERE dc.deal_id = x.id)`},
			{Name: "BEGINDATE", Type: "date", Col: "x.created_at", ReadOnly: true},
			{Name: "CLOSEDATE", Type: "date", Col: "x.close_date"},
			{Name: "SOURCE_ID", Type: "crm_status", StatusType: "SOURCE", Col: "coalesce(js.code, js.id::text)"},
		}, portalCommon, portalUTM("deal")),
	},
	"lead": {Entity: "lead", Kind: "leads",
		From: `crm_leads x JOIN crm_stages st ON st.id = x.stage_id JOIN users ur ON ur.id = x.responsible_id
			JOIN users uc ON uc.id = x.created_by LEFT JOIN crm_companies xc ON xc.id = x.company_id
			LEFT JOIN crm_contacts xk ON xk.id = x.contact_id LEFT JOIN crm_dict js ON js.id = x.source_id`,
		Fields: slices.Concat([]portalField{
			{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
			{Name: "TITLE", Type: "string", Col: "x.title", Required: true},
			{Name: "NAME", Type: "string", Col: "x.name"},
			{Name: "SECOND_NAME", Type: "string", Col: "''"},
			{Name: "LAST_NAME", Type: "string", Col: "''"},
			{Name: "COMPANY_TITLE", Type: "string", Col: "x.company_title"},
			{Name: "SOURCE_ID", Type: "crm_status", StatusType: "SOURCE", Col: "coalesce(js.code, js.id::text)"},
			{Name: "SOURCE_DESCRIPTION", Type: "string", Col: "x.source"},
			{Name: "STATUS_ID", Type: "crm_status", StatusType: "STATUS", Col: "x.stage_id::text"},
			{Name: "STATUS_SEMANTIC_ID", Type: "string", Col: portalSemantic, ReadOnly: true},
			{Name: "OPPORTUNITY", Type: "double", Col: "x.amount::float8"},
			{Name: "CURRENCY_ID", Type: "crm_currency", Col: "x.currency"},
			{Name: "IS_RETURN_CUSTOMER", Type: "char", Col: "CASE WHEN x.is_repeat THEN 'Y' ELSE 'N' END", ReadOnly: true},
			{Name: "CONTACT_ID", Type: "crm_contact", Col: "xk.num", ReadOnly: true},
			{Name: "COMPANY_ID", Type: "crm_company", Col: "xc.num", ReadOnly: true},
		}, portalCommon, portalPhones, portalUTM("lead")),
	},
	"contact": {Entity: "contact", Kind: "contacts",
		From: `crm_contacts x JOIN users ur ON ur.id = x.responsible_id JOIN users uc ON uc.id = x.created_by
			LEFT JOIN crm_companies xc ON xc.id = x.company_id LEFT JOIN crm_dict jt ON jt.id = x.contact_type_id
			LEFT JOIN crm_dict js ON js.id = x.source_id`,
		Fields: slices.Concat([]portalField{
			{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
			{Name: "NAME", Type: "string", Col: "x.name", Required: true},
			{Name: "SECOND_NAME", Type: "string", Col: "''"},
			{Name: "LAST_NAME", Type: "string", Col: "''"},
			{Name: "POST", Type: "string", Col: "x.position"},
			{Name: "TYPE_ID", Type: "crm_status", StatusType: "CONTACT_TYPE", Col: "coalesce(jt.code, jt.id::text)"},
			{Name: "SOURCE_ID", Type: "crm_status", StatusType: "SOURCE", Col: "coalesce(js.code, js.id::text)"},
			{Name: "COMPANY_ID", Type: "crm_company", Col: "xc.num"},
		}, portalCommon, portalPhones),
	},
	"company": {Entity: "company", Kind: "companies",
		From: `crm_companies x JOIN users ur ON ur.id = x.responsible_id JOIN users uc ON uc.id = x.created_by
			LEFT JOIN crm_dict jt ON jt.id = x.company_type_id LEFT JOIN crm_dict ji ON ji.id = x.industry_id`,
		Fields: slices.Concat([]portalField{
			{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
			{Name: "TITLE", Type: "string", Col: "x.title", Required: true},
			{Name: "COMPANY_TYPE", Type: "crm_status", StatusType: "COMPANY_TYPE", Col: "coalesce(jt.code, jt.id::text)"},
			{Name: "INDUSTRY", Type: "crm_status", StatusType: "INDUSTRY", Col: "coalesce(ji.code, ji.id::text)"},
			{Name: "ADDRESS", Type: "string", Col: "x.address"},
			{Name: "WEB", Type: "crm_multifield", Col: "x.website", Multiple: true},
		}, portalCommon, portalPhones),
	},
}

func init() {
	for entity := range portalEntities {
		for action, m := range map[string]portalMethod{
			"fields": {Scope: "crm:read", Run: portalCRM(entity, (*Server).portalCRMFields)},
			"list":   {Scope: "crm:read", Run: portalCRM(entity, (*Server).portalCRMList)},
			"get":    {Scope: "crm:read", Run: portalCRM(entity, (*Server).portalCRMGet)},
			"add":    {Scope: "crm:write", Run: portalCRM(entity, (*Server).portalCRMAdd)},
			"update": {Scope: "crm:write", Run: portalCRM(entity, (*Server).portalCRMUpdate)},
			"delete": {Scope: "crm:write", Run: portalCRM(entity, (*Server).portalCRMDelete)},
		} {
			portalMethods["crm."+entity+"."+action] = m
		}
	}
}

func portalCRM(entity string, run func(*Server, portalCall, portalEntity) (portalOut, error)) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) {
		sight, err := s.portalSight(c)
		if err != nil {
			return portalOut{}, err
		}
		e, err := s.portalWithUF(c.r.Context(), portalEntities[entity], sight)
		if err != nil {
			return portalOut{}, err
		}
		return run(s, c, e)
	}
}

// portalParam is a parameter by its name in any case (id, ID; fields, FIELDS).
func portalParam(in map[string]any, name string) any {
	for k, v := range in {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return nil
}

// cardReq is the request as the go24 REST of the card (its kind and id) for crmCreate, crmUpdate, crmTrash.
func cardReq(r *http.Request, kind, id string) *http.Request {
	r = r.WithContext(r.Context())
	r.SetPathValue("kind", kind)
	r.SetPathValue("id", id)
	return r
}

// ---------- stages ----------

// portalStage is a stage of deals (of a pipeline) or of leads.
type portalStage struct {
	ID, Code   string
	Num        int64
	Category   int64 // the pipeline's number; a lead's — 0
	PipelineID string
	Entity     string
	TypeNum    int64 // a smart process's stage: the number of the process
}

// portalStageCodes: built-in stages whose the portal code is not their go24 code in capitals.
var portalStageCodes = map[string]string{"prepayment": "PREPAYMENT_INVOICE", "lead_new": "NEW", "lead_in_process": "IN_PROCESS",
	"lead_processed": "PROCESSED", "lead_converted": "CONVERTED", "lead_junk": "JUNK", "sp_new": "NEW", "sp_preparation": "PREPARATION",
	"sp_client": "CLIENT", "sp_success": "SUCCESS", "sp_fail": "FAIL"}

// Portal is the stage's id: the built-in code (NEW, WON), a custom one UC_<number>; a deal's stage of a
// pipeline other than the main one — with its prefix C<CATEGORY_ID>:, a smart process's — DT<entityTypeId>_<CATEGORY_ID>:.
func (st portalStage) Portal() string {
	id := portalStageCodes[st.Code]
	switch {
	case st.Code == "":
		id = "UC_" + strconv.FormatInt(st.Num, 10)
	case id == "":
		id = strings.ToUpper(st.Code)
	}
	if st.TypeNum > 0 {
		return "DT" + strconv.FormatInt(st.TypeNum, 10) + "_" + strconv.FormatInt(st.Category, 10) + ":" + id
	}
	if st.Entity == "deal" && st.Category > 0 {
		id = "C" + strconv.FormatInt(st.Category, 10) + ":" + id
	}
	return id
}

// portalStages are the stages of the entity's cards: of leads, of the deal pipelines or of a smart process's ones.
func (s *Server) portalStages(ctx context.Context, e portalEntity) ([]portalStage, error) {
	rows, err := s.DB.Query(ctx, `SELECT st.id::text, coalesce(st.code, ''), st.num, coalesce(p.num, 0), coalesce(p.id::text, ''), st.entity, $3::bigint
		FROM crm_stages st LEFT JOIN crm_pipelines p ON p.id = st.pipeline_id
		WHERE st.entity = $1 AND p.type_id IS NOT DISTINCT FROM nullif($2, '')::uuid ORDER BY p.num, st.sort, st.num`, e.Entity, e.Type, e.TypeNum)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[portalStage])
}

// portalStageOf is the stage by its the portal id; a deal's — of the pipeline given ("" — of the one the prefix names).
func portalStageOf(stages []portalStage, code, pipelineID string) (portalStage, bool) {
	for _, st := range stages {
		if strings.EqualFold(st.Portal(), code) && (pipelineID == "" || st.PipelineID == pipelineID) {
			return st, true
		}
		if pipelineID != "" && st.PipelineID == pipelineID { // C2:NEW given for a deal of the pipeline 2, or NEW
			if _, bare, ok := strings.Cut(st.Portal(), ":"); ok && strings.EqualFold(bare, code) {
				return st, true
			}
		}
	}
	return portalStage{}, false
}

// ---------- reading ----------

// portalRows reads cards of the entity: the condition is SQL over x with args (the reader's scope is in it), select —
// the fields to give. A card's fields are its map in the format.
func (s *Server) portalRows(ctx context.Context, e portalEntity, cond, order string, args []any, start, limit int, sel []portalField) ([]map[string]any, error) {
	cols := []string{"x.id::text"}
	for _, f := range sel {
		if f.Col != "" {
			cols = append(cols, f.Col)
		}
	}
	q := `SELECT ` + strings.Join(cols, ", ") + ` FROM ` + e.From + ` WHERE ` + cond + ` ORDER BY ` + order
	if limit > 0 {
		q += ` OFFSET ` + strconv.Itoa(start) + ` LIMIT ` + strconv.Itoa(limit)
	}
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stages []portalStage
	if e.Entity == "deal" || e.Entity == "lead" {
		if stages, err = s.portalStages(ctx, e); err != nil {
			return nil, err
		}
	}
	var out []map[string]any
	var ids []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		ids = append(ids, vals[0].(string))
		card, i := map[string]any{}, 1
		for _, f := range sel {
			if f.Col == "" {
				continue
			}
			v := portalValue(f, vals[i])
			i++
			if f.Name == "STAGE_ID" || f.Name == "STATUS_ID" {
				if st := slices.IndexFunc(stages, func(st portalStage) bool { return st.ID == v }); st >= 0 {
					v = stages[st].Portal()
				}
			}
			if f.Name == "WEB" {
				if v = nil; vals[i-1] != "" {
					v = []map[string]any{{"ID": "1", "VALUE_TYPE": "WORK", "VALUE": vals[i-1], "TYPE_ID": "WEB"}}
				}
			}
			if v != nil || !f.Multiple {
				card[f.Name] = v
			}
		}
		out = append(out, card)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var comms []string
	for _, f := range sel {
		if f.Name == "PHONE" || f.Name == "EMAIL" {
			comms = append(comms, strings.ToLower(f.Name))
		}
	}
	if len(comms) > 0 && len(ids) > 0 {
		err = s.portalComms(ctx, e.Entity, ids, comms, out)
	}
	return out, err
}

// portalValue is a column's value gives it: numbers and sums as text, dates with the zone.
func portalValue(f portalField, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', 2, 64)
	case time.Time:
		if f.Type == "date" {
			if f.Col == "x.created_at" {
				x = x.In(time.Local)
			}
			return time.Date(x.Year(), x.Month(), x.Day(), 0, 0, 0, 0, time.Local).Format(time.RFC3339)
		}
		return x.In(time.Local).Format(time.RFC3339)
	case []any:
		out := []string{}
		for _, y := range x {
			switch z := y.(type) {
			case int64:
				out = append(out, strconv.FormatInt(z, 10))
			case string:
				out = append(out, z)
			}
		}
		return out
	}
	return v
}

// portalComms adds PHONE and EMAIL (kinds) to the cards: [{ID, VALUE_TYPE, VALUE, TYPE_ID}], ID — the place of the
// phone (e-mail) on the card from 1, as update changes it.
func (s *Server) portalComms(ctx context.Context, entity string, ids, kinds []string, cards []map[string]any) error {
	rows, err := s.DB.Query(ctx, `SELECT entity_id::text, kind, type, value FROM crm_comms WHERE entity = $1 AND entity_id::text = ANY($2)
		AND kind = ANY($3) ORDER BY kind DESC, sort`, entity, ids, kinds)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, typ, value string
		if err := rows.Scan(&id, &kind, &typ, &value); err != nil {
			return err
		}
		card, name := cards[slices.Index(ids, id)], strings.ToUpper(kind)
		list, _ := card[name].([]map[string]any)
		card[name] = append(list, map[string]any{"ID": strconv.Itoa(len(list) + 1), "VALUE_TYPE": strings.ToUpper(typ),
			"VALUE": value, "TYPE_ID": name})
	}
	return rows.Err()
}

// portalSelect are the fields to give: select[] (ID always, «*» — all but the multiple and the user fields, «UF_*» —
// the user fields); none given — as «*».
func portalSelect(e portalEntity, sel []string) []portalField {
	var out []portalField
	all, uf := len(sel) == 0 || slices.Contains(sel, "*"), slices.Contains(sel, "UF_*")
	for _, f := range e.Fields {
		if f.Name == "ID" || (all && !f.Multiple && f.UF == nil) || (uf && f.UF != nil) || slices.ContainsFunc(sel, func(x string) bool { return strings.EqualFold(x, f.Name) }) {
			out = append(out, f)
		}
	}
	return out
}

// portalOps are the prefixes of filter keys, longer first.
var portalOps = []string{"!=%", "!%", "!=", "!@", ">=", "<=", "=%", "%=", "!", "=", ">", "<", "%", "@"}

// portalWhere is the condition of a filter: FIELD (equals; a list — any of), =, !, >, <, >=, <=, % (contains),
// =% (LIKE as given), @ (in a list), !@; PHONE and EMAIL — the card has the phone (e-mail).
func (s *Server) portalWhere(ctx context.Context, e portalEntity, filter map[string]any, arg func(any) string) ([]string, error) {
	var where []string
	var stages []portalStage
	for key, v := range filter {
		op := ""
		for _, o := range portalOps {
			if strings.HasPrefix(key, o) {
				op, key = o, key[len(o):]
				break
			}
		}
		key = strings.ToUpper(key)
		vals := portalList(v)
		neg := strings.HasPrefix(op, "!")
		if key == "PHONE" || key == "EMAIL" {
			kind := strings.ToLower(key)
			var keys []string
			for _, x := range vals {
				if k := crm.CommKey(kind, x); k != "" {
					keys = append(keys, k)
				}
			}
			cond := `EXISTS (SELECT 1 FROM crm_comms m WHERE m.entity_id = x.id AND m.kind = '` + kind + `' AND m.key = ANY(` + arg(keys) + `::text[]))`
			if neg {
				cond = "NOT " + cond
			}
			where = append(where, cond)
			continue
		}
		f, ok := e.field(key)
		if !ok || f.Col == "" || f.Multiple {
			continue //: a field it does not know is not a condition
		}
		if f.Name == "STAGE_ID" || f.Name == "STATUS_ID" {
			if stages == nil {
				var err error
				if stages, err = s.portalStages(ctx, e); err != nil {
					return nil, err
				}
			}
			for i, x := range vals {
				vals[i] = "-"
				if st, ok := portalStageOf(stages, x, ""); ok {
					vals[i] = st.ID
				}
			}
		}
		cast := map[string]string{"integer": "bigint", "user": "bigint", "crm_company": "bigint", "crm_contact": "bigint",
			"crm_category": "bigint", "crm_enum_ownertype": "bigint", "crm_enum_activitytype": "bigint",
			"crm_enum_activitystatus": "bigint", "enumeration": "bigint", "double": "float8", "date": "date", "datetime": "timestamptz"}[f.Type]
		col := f.Col
		if f.Type == "date" {
			col = "(" + col + ")::date"
		}
		if cast == "" {
			cast = "text"
		}
		var cond string
		switch strings.TrimPrefix(op, "!") {
		case "%", "=%", "%=":
			pat := strings.Join(vals, "")
			if op == "%" || op == "!%" {
				pat = like(pat)
			}
			cond = "(" + col + ")::text ILIKE " + arg(pat)
		case ">", "<", ">=", "<=":
			if len(vals) == 0 || vals[0] == "" {
				continue
			}
			cond = col + " " + op + " " + arg(vals[0]) + "::" + cast
		default: // =, !, @, !@, none
			if len(vals) == 0 || (len(vals) == 1 && vals[0] == "") { // empty: not set
				cond = "(" + col + " IS NULL OR (" + col + ")::text IN ('', '0'))"
				break
			}
			if cast == "text" && op != "=" && op != "!=" {
				cond = "lower(" + col + ") = ANY(" + arg(lowerAll(vals)) + "::text[])"
			} else {
				cond = col + " = ANY(" + arg(vals) + "::" + cast + "[])"
			}
		}
		if neg {
			cond = "(" + col + " IS NULL OR NOT " + cond + ")"
		}
		where = append(where, cond)
	}
	return where, nil
}

// like is the pattern «contains» of ILIKE.
func like(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// portalOrder is ORDER BY of order[FIELD]=ASC|DESC; by ID by default.
func portalOrder(e portalEntity, order map[string]any) string {
	var out []string
	for k, v := range order {
		f, ok := e.field(strings.ToUpper(k))
		if !ok || f.Col == "" || f.Multiple {
			continue
		}
		dir := "ASC"
		if strings.EqualFold(portalStr(v), "desc") {
			dir = "DESC"
		}
		out = append(out, f.Col+" "+dir)
	}
	slices.Sort(out) // the map has no order: the same call gives the same order
	return strings.Join(append(out, "x.num"), ", ")
}

// crm.*.list: filter, order, select, start — a page of 50 the caller may read.
func (s *Server) portalCRMList(c portalCall, e portalEntity) (portalOut, error) {
	ctx := c.r.Context()
	args := []any{c.p.UserID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var scope string
	var err error
	switch e.Entity {
	case "activity":
		scope, err = s.portalActivityScope(ctx, c.p.UserID)
	case "product", "productsection": // the catalog: to everyone of the CRM
		scope = "$1::uuid IS NOT NULL"
	default:
		scope, err = s.CRM.ReadScope(ctx, c.p.UserID, e.Entity, "x", 1)
	}
	if err != nil {
		return portalOut{}, err
	}
	where, err := s.portalWhere(ctx, e, portalMap(portalParam(c.in, "filter")), arg)
	if err != nil {
		return portalOut{}, err
	}
	cond := strings.Join(append([]string{"(" + scope + ")"}, where...), " AND ")
	var total int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM `+e.From+` WHERE `+cond, args...).Scan(&total); err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	cards, err := s.portalRows(ctx, e, cond, portalOrder(e, portalMap(portalParam(c.in, "order"))), args, start, portalPage,
		portalSelect(e, portalList(portalParam(c.in, "select"))))
	if err != nil {
		return portalOut{}, err
	}
	if cards == nil {
		cards = []map[string]any{}
	}
	return portalPaged(cards, start, len(cards), total), nil
}

// portalCardID is the go24 id of the card numbered id (a parameter); no such card — not found.
func (s *Server) portalCardID(c portalCall, e portalEntity, id any) (string, error) {
	n, err := strconv.ParseInt(portalStr(id), 10, 64)
	if err != nil {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_id")
	}
	var uid string
	err = s.DB.QueryRow(c.r.Context(), `SELECT x.id::text FROM `+e.From+` WHERE x.num = $1`, n).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return uid, err
}

// crm.*.get: the card by id with all its fields.
func (s *Server) portalCRMGet(c portalCall, e portalEntity) (portalOut, error) {
	id, err := s.portalCardID(c, e, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.apiCRMItem(cardReq(c.r, e.Kind, id), c.p, "read"); err != nil {
		return portalOut{}, err
	}
	cards, err := s.portalRows(c.r.Context(), e, "x.id = $1", "x.num", []any{id}, 0, 0, e.Fields)
	if err != nil || len(cards) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: cards[0]}, nil
}

// crm.*.fields: the fields with their types describes them.
func (s *Server) portalCRMFields(c portalCall, e portalEntity) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	for _, f := range e.Fields {
		d := map[string]any{"type": f.Type, "isRequired": f.Required, "isReadOnly": f.ReadOnly, "isImmutable": f.Name == "CATEGORY_ID",
			"isMultiple": f.Multiple, "isDynamic": false, "title": s.I18n.T(lang, "portal.field."+f.Name)}
		if f.StatusType != "" {
			d["statusType"] = f.StatusType
		}
		if f.UF != nil {
			d["isDynamic"], d["title"], d["listLabel"], d["formLabel"], d["filterLabel"] = true, f.UF.Name, f.UF.Name, f.UF.Name, f.UF.Name
			if f.UF.Kind == "list" {
				items := []map[string]any{}
				for i, o := range f.UF.Options {
					items = append(items, map[string]any{"ID": strconv.Itoa(i + 1), "VALUE": o})
				}
				d["items"] = items
			}
		}
		out[f.Name] = d
	}
	return portalOut{Result: out}, nil
}

// ---------- changing ----------

// portalNum is the go24 id of a card, an employee or a pipeline by its number (a go24 id is taken as it is); 0 and ""
// — none.
func (s *Server) portalNum(ctx context.Context, table string, v any) (string, error) {
	x := strings.TrimSpace(portalStr(v))
	if x == "" || x == "0" && table != "crm_pipelines" {
		return "", nil
	}
	n, err := strconv.ParseInt(x, 10, 64)
	if err != nil {
		return x, nil
	}
	var id string
	q := `SELECT id::text FROM ` + table + ` WHERE num = $1`
	if table == "crm_pipelines" {
		q += ` AND type_id IS NULL`
	}
	if err := s.DB.QueryRow(ctx, q, n).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return "", i18n.E("err.not_found")
	} else if err != nil {
		return "", err
	}
	return id, nil
}

// portalItems are the values of a multiple field: a list of {ID, VALUE, VALUE_TYPE, DELETE}, or a value alone.
func portalItems(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []any:
		for _, y := range x {
			out = append(out, portalItems(y)...)
		}
	case map[string]any:
		if slices.ContainsFunc([]string{"VALUE", "ID", "DELETE", "VALUE_TYPE"}, func(k string) bool { _, ok := portalParamOK(x, k); return ok }) {
			out = append(out, x)
			break
		}
		for _, y := range x { // {"0": {...}, "1": {...}}
			out = append(out, portalItems(y)...)
		}
	case nil:
	default:
		out = append(out, map[string]any{"VALUE": x})
	}
	return out
}

func portalParamOK(in map[string]any, name string) (any, bool) {
	for k, v := range in {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return nil, false
}

// portalMulti applies PHONE or EMAIL to the card's ones: an item with ID changes that one (DELETE=Y or an
// empty VALUE removes it), without — adds one.
func portalMulti(cur []crm.Comm, v any) []crm.Comm {
	out := slices.Clone(cur)
	drop := map[int]bool{}
	for _, it := range portalItems(v) {
		val := strings.TrimSpace(portalStr(portalParam(it, "VALUE")))
		typ := strings.ToLower(portalStr(portalParam(it, "VALUE_TYPE")))
		if n, err := strconv.Atoi(portalStr(portalParam(it, "ID"))); err == nil && n >= 1 && n <= len(cur) {
			if val == "" || strings.EqualFold(portalStr(portalParam(it, "DELETE")), "Y") {
				drop[n-1] = true
				continue
			}
			out[n-1].Value = val
			if typ != "" {
				out[n-1].Type = typ
			}
			continue
		}
		if val != "" {
			out = append(out, crm.Comm{Type: typ, Value: val})
		}
	}
	kept := []crm.Comm{}
	for i, x := range out {
		if !drop[i] {
			kept = append(kept, x)
		}
	}
	return kept
}

// portalDate reads a date: ISO 8601 with or without the time, or 31.12.2026.
func portalDate(v string) (*time.Time, error) {
	if v = strings.TrimSpace(v); v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "02.01.2006"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
			return &t, nil
		}
	}
	return nil, i18n.E("err.bad_date")
}

// portalInput turns the fields into a go24 REST body (apiCRMInput): only the fields given; the card id — of
// the card changed ("" — a new one). A field go24 does not have is skipped skips unknown ones.
func (s *Server) portalInput(ctx context.Context, e portalEntity, id string, fields map[string]any) (map[string]any, error) {
	in := map[string]any{}
	get := func(name string) (any, bool) { return portalParamOK(fields, name) }
	plain := map[string]string{"TITLE": "title", "COMMENTS": "comment", "CURRENCY_ID": "currency", "SOURCE_ID": "origin",
		"SOURCE_DESCRIPTION": "source", "COMPANY_TITLE": "company_title", "POST": "position", "ADDRESS": "address",
		"COMPANY_TYPE": "company_type", "INDUSTRY": "industry"}
	for b, g := range plain {
		if v, ok := get(b); ok {
			if _, has := e.field(b); has {
				in[g] = portalStr(v)
			}
		}
	}
	if v, ok := get("TYPE_ID"); ok && (e.Entity == "deal" || e.Entity == "contact") {
		in[e.Entity+"_type"] = portalStr(v)
	}
	if v, ok := get("OPPORTUNITY"); ok && e.Entity != "contact" && e.Entity != "company" {
		f, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(portalStr(v)), ",", "."), 64)
		if err != nil && portalStr(v) != "" {
			return nil, i18n.E("err.bad_number", portalStr(v))
		}
		in["amount"] = f
	}
	if v, ok := get("CLOSEDATE"); ok && e.Entity == "deal" {
		d, err := portalDate(portalStr(v))
		if err != nil {
			return nil, err
		}
		in["close_date"] = d
	}
	refs := map[string][2]string{"ASSIGNED_BY_ID": {"users", "responsible_id"}, "COMPANY_ID": {"crm_companies", "company_id"},
		"CONTACT_ID": {"crm_contacts", "contact_id"}}
	for b, ref := range refs {
		v, ok := get(b)
		if f, has := e.field(b); !ok || !has || f.ReadOnly {
			continue
		}
		uid, err := s.portalNum(ctx, ref[0], v)
		if err != nil {
			return nil, err
		}
		in[ref[1]] = uid
	}
	if v, ok := get("CONTACT_IDS"); ok && e.Entity == "deal" {
		ids := []string{}
		for _, x := range portalList(v) {
			uid, err := s.portalNum(ctx, "crm_contacts", x)
			if err != nil {
				return nil, err
			}
			if uid != "" {
				ids = append(ids, uid)
			}
		}
		in["contact_ids"] = ids
	}
	if e.Entity == "lead" || e.Entity == "contact" { // go24 keeps the whole name
		var parts []string
		given := false
		for _, b := range []string{"NAME", "SECOND_NAME", "LAST_NAME"} {
			if v, ok := get(b); ok {
				given = true
				if x := strings.TrimSpace(portalStr(v)); x != "" {
					parts = append(parts, x)
				}
			}
		}
		if given {
			in["name"] = strings.Join(parts, " ")
		}
	}
	if v, ok := get("WEB"); ok && e.Entity == "company" {
		web := ""
		if list := portalMulti(nil, v); len(list) > 0 {
			web = list[0].Value
		}
		in["website"] = web
	}
	if e.Entity != "deal" {
		var cur []crm.Comm
		for _, kind := range []string{"phone", "email"} {
			v, ok := get(strings.ToUpper(kind))
			if !ok {
				continue
			}
			if cur == nil && id != "" {
				var err error
				if cur, err = s.CRM.Comms(ctx, e.Entity, id); err != nil {
					return nil, err
				}
			}
			in[kind+"s"] = portalMulti(crm.CommsOf(cur, kind), v)
		}
	}
	if err := s.portalStageIn(ctx, e, id, fields, in); err != nil {
		return nil, err
	}
	if e.Entity == "deal" || e.Entity == "lead" {
		var utm apiUTM
		given := false
		for b, dst := range map[string]*string{"UTM_SOURCE": &utm.Source, "UTM_MEDIUM": &utm.Medium, "UTM_CAMPAIGN": &utm.Campaign,
			"UTM_CONTENT": &utm.Content, "UTM_TERM": &utm.Term} {
			if v, ok := get(b); ok {
				given, *dst = true, portalStr(v)
			}
		}
		if given && id != "" && s.Marketing != nil { // the tags not given stay
			old, inherited, err := s.Marketing.UTMOf(ctx, e.Entity, id)
			if err != nil {
				return nil, err
			}
			for _, p := range []struct {
				dst *string
				old string
				b   string
			}{{&utm.Source, old.Source, "UTM_SOURCE"},
				{&utm.Medium, old.Medium, "UTM_MEDIUM"}, {&utm.Campaign, old.Campaign, "UTM_CAMPAIGN"},
				{&utm.Content, old.Content, "UTM_CONTENT"}, {&utm.Term, old.Term, "UTM_TERM"}} {
				if _, ok := get(p.b); !ok && !inherited {
					*p.dst = p.old
				}
			}
		}
		if given {
			in["utm"] = utm
		}
	}
	return in, nil
}

// portalStageIn: CATEGORY_ID (a new deal only —, the pipeline of a deal is not changed by update) and
// STAGE_ID of a deal, STATUS_ID of a lead.
func (s *Server) portalStageIn(ctx context.Context, e portalEntity, id string, fields map[string]any, in map[string]any) error {
	if e.Entity != "deal" && e.Entity != "lead" {
		return nil
	}
	pipeline := ""
	if v, ok := portalParamOK(fields, "CATEGORY_ID"); ok && e.Entity == "deal" && id == "" {
		var err error
		if pipeline, err = s.portalNum(ctx, "crm_pipelines", v); err != nil {
			return err
		}
		var same bool // a pipeline of the deals, or of the smart process
		if err := s.DB.QueryRow(ctx, `SELECT type_id IS NOT DISTINCT FROM nullif($2, '')::uuid FROM crm_pipelines WHERE id = $1`,
			pipeline, e.Type).Scan(&same); err != nil || !same {
			return i18n.E("err.not_found")
		}
		in["pipeline_id"] = pipeline
	}
	first := "" // where a new card goes: the main pipeline, a smart process's first one
	if e.Type != "" && id == "" {
		if err := s.DB.QueryRow(ctx, `SELECT id::text FROM crm_pipelines WHERE type_id = $1 ORDER BY num LIMIT 1`, e.Type).
			Scan(&first); err != nil {
			return err
		}
		if pipeline == "" {
			in["pipeline_id"] = first
		}
	}
	name := map[string]string{"deal": "STAGE_ID", "lead": "STATUS_ID"}[e.Entity]
	v, ok := portalParamOK(fields, name)
	if !ok || portalStr(v) == "" {
		return nil
	}
	if e.Entity == "deal" && id != "" {
		if err := s.DB.QueryRow(ctx, `SELECT pipeline_id::text FROM crm_deals WHERE id = $1`, id).Scan(&pipeline); err != nil {
			return err
		}
	}
	stages, err := s.portalStages(ctx, e)
	if err != nil {
		return err
	}
	st, ok := portalStageOf(stages, portalStr(v), pipeline)
	if !ok && e.Entity == "deal" && pipeline == "" { // NEW for a new deal: of the main pipeline
		st, ok = portalStageOf(stages, portalStr(v), first)
	}
	if !ok {
		return i18n.E("err.crm_stage")
	}
	in["stage_id"] = st.ID
	if e.Entity == "deal" && id == "" {
		in["pipeline_id"] = st.PipelineID
	}
	return nil
}

// portalBody is the go24 REST body of the fields.
func (s *Server) portalBody(c portalCall, e portalEntity, id string) ([]byte, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	in, err := s.portalInput(c.r.Context(), e, id, fields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(in)
}

// crm.*.add: fields → the number of the new card.
func (s *Server) portalCRMAdd(c portalCall, e portalEntity) (portalOut, error) {
	body, err := s.portalBody(c, e, "")
	if err != nil {
		return portalOut{}, err
	}
	var in apiCRMInput
	if err := json.Unmarshal(body, &in); err != nil {
		return portalOut{}, i18n.E("err.bad_json", err.Error())
	}
	uf, err := portalUFIn(e, portalMap(portalParam(c.in, "fields")))
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.crmCreate(cardReq(c.r, e.Kind, ""), c.p, in)
	if err != nil {
		return portalOut{}, err
	}
	if len(uf) > 0 {
		if err := s.CRM.SetFields(c.r.Context(), c.p.UserID, e.Entity, id, uf); err != nil {
			return portalOut{}, err
		}
	}
	var num int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT num FROM crm_`+e.Kind+` WHERE id = $1`, id).Scan(&num)
	return portalOut{Result: num}, err
}

// crm.*.update: id, fields — only the fields given change.
func (s *Server) portalCRMUpdate(c portalCall, e portalEntity) (portalOut, error) {
	id, err := s.portalCardID(c, e, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	body, err := s.portalBody(c, e, id)
	if err != nil {
		return portalOut{}, err
	}
	uf, err := portalUFIn(e, portalMap(portalParam(c.in, "fields")))
	if err != nil {
		return portalOut{}, err
	}
	if len(uf) == 0 || string(body) != "{}" {
		if err := s.crmUpdate(cardReq(c.r, e.Kind, id), c.p, body); err != nil {
			return portalOut{}, err
		}
	} else if _, err := s.apiCRMItem(cardReq(c.r, e.Kind, id), c.p, "write"); err != nil { // only the user fields
		return portalOut{}, err
	}
	if len(uf) > 0 {
		if err := s.CRM.SetFields(c.r.Context(), c.p.UserID, e.Entity, id, uf); err != nil {
			return portalOut{}, err
		}
		if err := s.CRM.CheckInfoRequests(c.r.Context(), c.p.UserID, e.Entity, id); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, nil
}

// crm.*.delete: id — the card goes to the CRM's recycle bin.
func (s *Server) portalCRMDelete(c portalCall, e portalEntity) (portalOut, error) {
	id, err := s.portalCardID(c, e, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if err := s.crmTrash(cardReq(c.r, e.Kind, id), c.p); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}
