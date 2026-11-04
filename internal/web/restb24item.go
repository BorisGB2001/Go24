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

// REST compatible with the portal, the universal methods of the CRM (GO24DEV-952): crm.item.* — the cards of any kind
// by entityTypeId (1 leads, 2 deals, 3 contacts, 4 companies, from 128 the smart processes) with the fields named in
// camelCase (stageId, assignedById, createdTime); crm.type.* — the smart processes. They go the way of crm.deal.* and
// the others (portalCRMList, portalCRMAdd…): the names are turned to theirs and back. A smart process's cards are deals of
// its pipelines, numbered as deals; its stages are DT<entityTypeId>_<CATEGORY_ID>:<code>.

const portalFirstType = 128 // the first number of a smart process

func init() {
	for name, m := range map[string]portalMethod{
		"crm.item.fields":            {Scope: "crm:read", Run: (*Server).portalItemFields},
		"crm.item.list":              {Scope: "crm:read", Run: (*Server).portalItemList},
		"crm.item.get":               {Scope: "crm:read", Run: (*Server).portalItemGet},
		"crm.item.add":               {Scope: "crm:write", Run: (*Server).portalItemAdd},
		"crm.item.update":            {Scope: "crm:write", Run: (*Server).portalItemUpdate},
		"crm.item.delete":            {Scope: "crm:write", Run: (*Server).portalItemDelete},
		"crm.type.fields":            {Scope: "crm:read", Run: (*Server).portalTypeFields},
		"crm.type.list":              {Scope: "crm:read", Run: (*Server).portalTypeList},
		"crm.type.get":               {Scope: "crm:read", Run: (*Server).portalTypeGet},
		"crm.type.getbyentitytypeid": {Scope: "crm:read", Run: (*Server).portalTypeGet},
		"crm.type.add":               {Scope: "crm:write", Run: (*Server).portalTypeAdd},
		"crm.type.update":            {Scope: "crm:write", Run: (*Server).portalTypeUpdate},
		"crm.type.delete":            {Scope: "crm:write", Run: (*Server).portalTypeDelete},
	} {
		portalMethods[name] = m
	}
}

// portalTypeNum is the entityTypeId of the kind.
func portalTypeNum(e portalEntity) int64 {
	if e.TypeNum > 0 {
		return e.TypeNum
	}
	for _, o := range portalOwners {
		if o.Entity == e.Entity {
			return int64(o.ID)
		}
	}
	return 0
}

// portalItemEntity is the kind of cards numbered n: one of the four, or a smart process (its deals, without the deal's
// own TYPE_ID).
func (s *Server) portalItemEntity(c portalCall, n int64) (portalEntity, error) {
	for _, o := range portalOwners {
		if int64(o.ID) == n {
			return portalEntities[o.Entity], nil
		}
	}
	types, err := s.portalSmartTypes(c.r.Context(), "num = $1", n)
	if err != nil {
		return portalEntity{}, err
	}
	if len(types) == 0 {
		return portalEntity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_entity_type", n)
	}
	e := portalEntities["deal"]
	e.Type, e.TypeNum = types[0].ID, n
	e.From = strings.Replace(e.From, "xp.type_id IS NULL", "xp.type_id = '"+e.Type+"'", 1) // a uuid read from the base
	var fields []portalField
	for _, f := range e.Fields {
		if f.Name != "TYPE_ID" {
			fields = append(fields, f)
		}
	}
	e.Fields = fields
	return e, nil
}

// portalItemOf is the kind of entityTypeId of the call.
func (s *Server) portalItemOf(c portalCall) (portalEntity, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(portalParam(c.in, "entityTypeId"))), 10, 64)
	if err != nil {
		return portalEntity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_entity_type", portalStr(portalParam(c.in, "entityTypeId")))
	}
	return s.portalItemEntity(c, n)
}

// portalItemNames: the fields whose name in crm.item.* is not their name of crm.*.* in camelCase.
var portalItemNames = map[string]string{"CREATED_BY_ID": "createdBy", "DATE_CREATE": "createdTime", "DATE_MODIFY": "updatedTime",
	"STATUS_ID": "stageId", "STATUS_SEMANTIC_ID": "stageSemanticId", "COMPANY_TYPE": "typeId"}

// portalItemName is the name of the field in crm.item.*: ASSIGNED_BY_ID → assignedById.
func portalItemName(name string) string {
	if x, ok := portalItemNames[name]; ok {
		return x
	}
	return portalCamel(name)
}

// portalMultis are the fields crm.item.* gives together as «fm».
var portalMultis = []string{"PHONE", "EMAIL", "WEB"}

// portalUpper is the name of crm.*.* of a field of crm.item.* (any case; the name of crm.*.* is taken too); "" — none.
func portalUpper(e portalEntity, name string) string {
	for _, f := range e.Fields {
		if strings.EqualFold(name, portalItemName(f.Name)) || strings.EqualFold(name, f.Name) {
			return f.Name
		}
	}
	return ""
}

// portalItemIn turns the parameters of crm.item.* into those of crm.*.*: the names of filter, order, select and fields.
func portalItemIn(e portalEntity, in map[string]any) map[string]any {
	out := map[string]any{"id": portalParam(in, "id"), "start": in["start"]}
	if f := portalMap(portalParam(in, "filter")); f != nil {
		filter := map[string]any{}
		for k, v := range f {
			op := ""
			for _, o := range portalOps {
				if strings.HasPrefix(k, o) {
					op, k = o, k[len(o):]
					break
				}
			}
			if name := portalUpper(e, k); name != "" {
				filter[op+name] = v
			}
		}
		out["filter"] = filter
	}
	if o := portalMap(portalParam(in, "order")); o != nil {
		order := map[string]any{}
		for k, v := range o {
			if name := portalUpper(e, k); name != "" {
				order[name] = v
			}
		}
		out["order"] = order
	}
	if sel := portalList(portalParam(in, "select")); len(sel) > 0 {
		var names []any
		for _, k := range sel {
			switch name := portalUpper(e, k); {
			case k == "*":
				names = append(names, k)
			case strings.EqualFold(k, "fm"):
				for _, m := range portalMultis {
					names = append(names, m)
				}
			case name != "":
				names = append(names, name)
			}
		}
		out["select"] = names
	}
	if f := portalMap(portalParam(in, "fields")); f != nil {
		fields := map[string]any{}
		for k, v := range f {
			if strings.EqualFold(k, "fm") {
				for _, it := range portalItems(v) {
					typ := strings.ToUpper(portalStr(portalParam(it, "typeId")))
					list, _ := fields[typ].([]any)
					fields[typ] = append(list, map[string]any{"ID": portalParam(it, "id"), "VALUE": portalParam(it, "value"),
						"VALUE_TYPE": portalParam(it, "valueType"), "DELETE": portalParam(it, "delete")})
				}
				continue
			}
			if name := portalUpper(e, k); name != "" {
				fields[name] = v
			}
		}
		out["fields"] = fields
	}
	return out
}

// portalItemOut is a card of crm.*.* as crm.item.* gives it: camelCase, numbers as numbers, PHONE, EMAIL and WEB as fm.
func portalItemOut(e portalEntity, card map[string]any) map[string]any {
	out := map[string]any{}
	var fm []map[string]any
	for name, v := range card {
		if items, ok := v.([]map[string]any); ok && slices.Contains(portalMultis, name) {
			for _, it := range items {
				id, _ := strconv.ParseInt(portalStr(it["ID"]), 10, 64)
				fm = append(fm, map[string]any{"id": id, "typeId": name, "valueType": it["VALUE_TYPE"], "value": it["VALUE"]})
			}
			continue
		}
		f, _ := e.field(name)
		out[portalItemName(name)] = portalItemValue(f, v)
	}
	if fm != nil {
		out["fm"] = fm
	}
	return out
}

// portalItemValue: numbers and the ids of employees and cards are numbers in crm.item.*.
func portalItemValue(f portalField, v any) any {
	num := func(x any) any {
		switch f.Type {
		case "integer", "user", "crm_company", "crm_contact", "crm_category":
			if n, err := strconv.ParseInt(portalStr(x), 10, 64); err == nil {
				return n
			}
		case "double":
			if n, err := strconv.ParseFloat(portalStr(x), 64); err == nil {
				return n
			}
		}
		return x
	}
	if list, ok := v.([]string); ok {
		out := make([]any, len(list))
		for i, x := range list {
			out[i] = num(x)
		}
		return out
	}
	if list, ok := v.([]any); ok {
		out := make([]any, len(list))
		for i, x := range list {
			out[i] = num(x)
		}
		return out
	}
	if v == nil {
		return nil
	}
	return num(v)
}

// portalItemCall is the call of crm.*.* the call of crm.item.* stands for.
func (s *Server) portalItemCall(c portalCall) (portalCall, portalEntity, error) {
	e, err := s.portalItemOf(c)
	if err != nil {
		return c, e, err
	}
	c.in = portalItemIn(e, c.in)
	return c, e, nil
}

func (s *Server) portalItemFields(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	all, err := s.portalCRMFields(c, e)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for name, d := range all.Result.(map[string]any) {
		if d := d.(map[string]any); !slices.Contains(portalMultis, name) {
			d["upperName"] = name
			out[portalItemName(name)] = d
		}
	}
	if _, ok := e.field("PHONE"); ok || e.Entity == "company" {
		out["fm"] = map[string]any{"type": "crm_multifield", "isRequired": false, "isReadOnly": false, "isImmutable": false,
			"isMultiple": true, "isDynamic": false, "title": s.tr(c.r, "portal.field.FM"), "upperName": "FM"}
	}
	return portalOut{Result: map[string]any{"fields": out}}, nil
}

// crm.item.list: entityTypeId, filter, order, select, start → {"items": [...]} of 50.
func (s *Server) portalItemList(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalCRMList(c, e)
	if err != nil {
		return portalOut{}, err
	}
	items := []map[string]any{}
	for _, card := range out.Result.([]map[string]any) {
		items = append(items, portalItemOut(e, card))
	}
	out.Result = map[string]any{"items": items}
	return out, nil
}

// portalItemCard is {"item": …} of the card numbered n.
func (s *Server) portalItemCard(c portalCall, e portalEntity, n any) (portalOut, error) {
	c.in = map[string]any{"id": n}
	out, err := s.portalCRMGet(c, e)
	if err != nil {
		return portalOut{}, err
	}
	card, _ := out.Result.(map[string]any)
	return portalOut{Result: map[string]any{"item": portalItemOut(e, card)}}, nil
}

func (s *Server) portalItemGet(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalItemCard(c, e, c.in["id"])
}

// crm.item.add: entityTypeId, fields → {"item": …} of the new card.
func (s *Server) portalItemAdd(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	out, err := s.portalCRMAdd(c, e)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalItemCard(c, e, out.Result)
}

// crm.item.update: entityTypeId, id, fields → {"item": …} as it is now.
func (s *Server) portalItemUpdate(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.portalCRMUpdate(c, e); err != nil {
		return portalOut{}, err
	}
	return s.portalItemCard(c, e, c.in["id"])
}

// crm.item.delete: entityTypeId, id — the card goes to the recycle bin; the result is empty.
func (s *Server) portalItemDelete(c portalCall) (portalOut, error) {
	c, e, err := s.portalItemCall(c)
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.portalCRMDelete(c, e); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: []any{}}, nil
}

// ---------- crm.type.*: the smart processes ----------

var portalTypeFieldList = []portalField{{Name: "id", Type: "integer", ReadOnly: true}, {Name: "entityTypeId", Type: "integer", ReadOnly: true},
	{Name: "title", Type: "string", Required: true}, {Name: "code", Type: "string", ReadOnly: true},
	{Name: "createdTime", Type: "datetime", ReadOnly: true}, {Name: "isCategoriesEnabled", Type: "boolean", ReadOnly: true},
	{Name: "isStagesEnabled", Type: "boolean", ReadOnly: true}, {Name: "isClientEnabled", Type: "boolean", ReadOnly: true},
	{Name: "isLinkWithProductsEnabled", Type: "boolean", ReadOnly: true}, {Name: "isObserversEnabled", Type: "boolean", ReadOnly: true}}

func (s *Server) portalTypeFields(c portalCall) (portalOut, error) {
	return portalOut{Result: map[string]any{"fields": s.portalFields(c, "portal.type.", portalTypeFieldList...)}}, nil
}

type portalSmartType struct {
	ID      string
	Num     int64
	Name    string
	Created time.Time
}

// out: id is entityTypeId too (go24 has one number), what go24's smart processes have — all on.
func (t portalSmartType) out() map[string]any {
	return map[string]any{"id": t.Num, "entityTypeId": t.Num, "title": t.Name, "code": "", "createdTime": t.Created.In(time.Local).Format(time.RFC3339),
		"isCategoriesEnabled": "Y", "isStagesEnabled": "Y", "isClientEnabled": "Y", "isLinkWithProductsEnabled": "Y",
		"isObserversEnabled": "Y", "isRecyclebinEnabled": "Y", "isAutomationEnabled": "Y", "isBizProcEnabled": "Y"}
}

func (s *Server) portalSmartTypes(ctx context.Context, cond string, args ...any) ([]portalSmartType, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, num, name, created_at FROM crm_types WHERE `+cond+` ORDER BY sort, num`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[portalSmartType])
}

// crm.type.list: filter (title, entityTypeId), start → {"types": [...]}.
func (s *Server) portalTypeList(c portalCall) (portalOut, error) {
	list, err := s.portalSmartTypes(c.r.Context(), "true")
	if err != nil {
		return portalOut{}, err
	}
	all := []map[string]any{}
	for _, t := range list {
		if m := t.out(); portalMatch(m, portalMap(portalParam(c.in, "filter"))) {
			all = append(all, m)
		}
	}
	start := min(portalStart(c.in), len(all))
	page := all[start:min(start+portalPage, len(all))]
	return portalPaged(map[string]any{"types": page}, start, len(page), len(all)), nil
}

// portalSmartTypeOf is the smart process of id (entityTypeId) of the call.
func (s *Server) portalSmartTypeOf(c portalCall) (portalSmartType, error) {
	v, _ := portalParamOK(c.in, "entityTypeId")
	if v == nil {
		v = portalParam(c.in, "id")
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
	list, err := s.portalSmartTypes(c.r.Context(), "num = $1", n)
	if err == nil && len(list) == 0 {
		err = s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return portalSmartType{}, err
	}
	return list[0], nil
}

func (s *Server) portalTypeGet(c portalCall) (portalOut, error) {
	t, err := s.portalSmartTypeOf(c)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"type": t.out()}}, nil
}

// portalTypeAdmin: the smart processes are set up by the administrators, as in the CRM's settings.
func (s *Server) portalTypeAdmin(c portalCall) error {
	if !s.apiAdmin(c.r.Context(), c.p.UserID) {
		return s.portalErr(c.r, http.StatusForbidden, "ACCESS_DENIED", "err.forbidden")
	}
	return nil
}

// crm.type.add: fields {title} → {"type": …}; the new one has its pipeline and stages, as one made in the CRM.
func (s *Server) portalTypeAdd(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	id, err := s.CRM.AddType(c.r.Context(), portalStr(portalParam(fields, "title")), "")
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalSmartTypes(c.r.Context(), "id = $1", id)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"type": list[0].out()}}, nil
}

// crm.type.update: id, fields {title} → {"type": …}.
func (s *Server) portalTypeUpdate(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	t, err := s.portalSmartTypeOf(c)
	if err != nil {
		return portalOut{}, err
	}
	cur, err := s.CRM.Type(c.r.Context(), t.ID)
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if v, ok := portalParamOK(fields, "title"); ok {
		cur.Name = portalStr(v)
	}
	if err := s.CRM.UpdateType(c.r.Context(), t.ID, cur.Name, cur.Color); err != nil {
		return portalOut{}, err
	}
	t.Name = cur.Name
	return portalOut{Result: map[string]any{"type": t.out()}}, nil
}

// crm.type.delete: id — a smart process without cards goes with its pipelines and stages.
func (s *Server) portalTypeDelete(c portalCall) (portalOut, error) {
	if err := s.portalTypeAdmin(c); err != nil {
		return portalOut{}, err
	}
	t, err := s.portalSmartTypeOf(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.CRM.DeleteType(c.r.Context(), t.ID); err != nil {
		if errors.Is(err, crm.ErrNotFound) {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
		}
		return portalOut{}, err
	}
	return portalOut{Result: []any{}}, nil
}
