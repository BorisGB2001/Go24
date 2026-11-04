package web

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
)

// REST compatible with the portal, links of the CRM cards (GO24DEV-957): crm.deal.contact.items.* (the contacts of a
// deal, the main one first), crm.contact.company.items.* and crm.company.contact.items.* (a contact has one company in
// go24: the main one), crm.duplicate.findbycomm (cards by phone or e-mail), crm.multifield.fields.
// Changes go the way of crmUpdate: the same rights, robots and history.

// portalLink is a kind of link: the card that owns it and the cards it lists.
type portalLink struct {
	Owner, Item string // entities: deal and contact
	Key         string // the field of an item: CONTACT_ID
}

var portalLinks = map[string]portalLink{
	"deal.contact":    {"deal", "contact", "CONTACT_ID"},
	"contact.company": {"contact", "company", "COMPANY_ID"},
	"company.contact": {"company", "contact", "CONTACT_ID"},
}

func init() {
	for name, l := range portalLinks {
		for action, m := range map[string]portalMethod{
			"fields":       {Scope: "crm:read", Run: portalLinkMethod(l, (*Server).portalLinkFields)},
			"items.get":    {Scope: "crm:read", Run: portalLinkMethod(l, (*Server).portalLinkGet)},
			"items.set":    {Scope: "crm:write", Run: portalLinkMethod(l, (*Server).portalLinkSet)},
			"items.delete": {Scope: "crm:write", Run: portalLinkMethod(l, (*Server).portalLinkClear)},
			"add":          {Scope: "crm:write", Run: portalLinkMethod(l, (*Server).portalLinkAdd)},
			"delete":       {Scope: "crm:write", Run: portalLinkMethod(l, (*Server).portalLinkDelete)},
		} {
			portalMethods["crm."+name+"."+action] = m
		}
	}
	portalMethods["crm.duplicate.findbycomm"] = portalMethod{Scope: "crm:read", Run: (*Server).portalFindByComm}
	portalMethods["crm.multifield.fields"] = portalMethod{Scope: "crm:read", Run: (*Server).portalMultiFields}
}

func portalLinkMethod(l portalLink, run func(*Server, portalCall, portalLink) (portalOut, error)) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) { return run(s, c, l) }
}

// portalLinkItems are the go24 ids of the items of the card, the main one first.
func (s *Server) portalLinkItems(ctx context.Context, l portalLink, id string) ([]string, error) {
	q := map[string]string{
		"deal.contact": `SELECT dc.contact_id::text FROM crm_deal_contacts dc JOIN crm_deals x ON x.id = dc.deal_id WHERE dc.deal_id = $1
			ORDER BY dc.contact_id IS DISTINCT FROM x.contact_id, dc.sort`,
		"contact.company": `SELECT company_id::text FROM crm_contacts WHERE id = $1 AND company_id IS NOT NULL`,
		"company.contact": `SELECT id::text FROM crm_contacts WHERE company_id = $1 ORDER BY num`,
	}[l.Owner+"."+l.Item]
	rows, err := s.DB.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// portalLinkOwner is the go24 id of the card (id) the caller may read or write.
func (s *Server) portalLinkOwner(c portalCall, l portalLink, action string) (string, error) {
	e := portalEntities[l.Owner]
	id, err := s.portalCardID(c, e, portalParam(c.in, "id"))
	if err != nil {
		return "", err
	}
	_, err = s.apiCRMItem(cardReq(c.r, e.Kind, id), c.p, action)
	return id, err
}

// portalLinkSave makes items the items of the card (the main one first).
func (s *Server) portalLinkSave(c portalCall, l portalLink, id string, items []string) error {
	ctx := c.r.Context()
	switch l.Owner {
	case "deal":
		body, _ := json.Marshal(map[string]any{"contact_ids": append([]string{}, items...)})
		return s.crmUpdate(cardReq(c.r, "deals", id), c.p, body)
	case "contact":
		co := ""
		if len(items) > 0 {
			co = items[0]
		}
		body, _ := json.Marshal(map[string]any{"company_id": co})
		return s.crmUpdate(cardReq(c.r, "contacts", id), c.p, body)
	}
	cur, err := s.portalLinkItems(ctx, l, id) // a company: the company of each contact changes
	if err != nil {
		return err
	}
	for _, ct := range cur {
		if !slices.Contains(items, ct) {
			if err := s.crmUpdate(cardReq(c.r, "contacts", ct), c.p, []byte(`{"company_id": ""}`)); err != nil {
				return err
			}
		}
	}
	for _, ct := range items {
		if !slices.Contains(cur, ct) {
			body, _ := json.Marshal(map[string]any{"company_id": id})
			if err := s.crmUpdate(cardReq(c.r, "contacts", ct), c.p, body); err != nil {
				return err
			}
		}
	}
	return nil
}

// portalLinkList are the items of items.set: a list of {CONTACT_ID, SORT, IS_PRIMARY}, or {"0": {...}, "1": {...}} as a
// form sends it.
func portalLinkList(v any) []map[string]any {
	var out []map[string]any
	switch x := v.(type) {
	case []any:
		for _, y := range x {
			if m, ok := y.(map[string]any); ok {
				out = append(out, m)
			}
		}
	case map[string]any:
		keys := slices.Sorted(maps.Keys(x))
		slices.SortStableFunc(keys, func(a, b string) int { i, _ := strconv.Atoi(a); j, _ := strconv.Atoi(b); return i - j })
		for _, k := range keys {
			if m, ok := x[k].(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// portalLinkIn is the go24 id of the item of an {CONTACT_ID, SORT, IS_PRIMARY} and whether it is the main one.
func (s *Server) portalLinkIn(ctx context.Context, l portalLink, it map[string]any) (string, bool, error) {
	uid, err := s.portalNum(ctx, map[string]string{"contact": "crm_contacts", "company": "crm_companies"}[l.Item], portalParam(it, l.Key))
	if err == nil && uid == "" {
		err = i18n.E("err.portal_link", l.Key)
	}
	return uid, strings.EqualFold(portalStr(portalParam(it, "IS_PRIMARY")), "Y"), err
}

// crm.*.items.get: id → [{CONTACT_ID, SORT, ROLE_ID, IS_PRIMARY}].
func (s *Server) portalLinkGet(c portalCall, l portalLink) (portalOut, error) {
	id, err := s.portalLinkOwner(c, l, "read")
	if err != nil {
		return portalOut{}, err
	}
	ctx := c.r.Context()
	items, err := s.portalLinkItems(ctx, l, id)
	if err != nil {
		return portalOut{}, err
	}
	table := map[string]string{"contact": "crm_contacts", "company": "crm_companies"}[l.Item]
	out := []map[string]any{}
	for i, uid := range items {
		var num int64
		if err := s.DB.QueryRow(ctx, `SELECT num FROM `+table+` WHERE id = $1`, uid).Scan(&num); err != nil {
			return portalOut{}, err
		}
		out = append(out, map[string]any{l.Key: num, "SORT": (i + 1) * 10, "ROLE_ID": 0,
			"IS_PRIMARY": map[bool]string{true: "Y", false: "N"}[i == 0 && l.Owner != "company"]})
	}
	return portalOut{Result: out}, nil
}

// crm.*.items.set: id, items — the items of the card instead of its own; the main one is IS_PRIMARY=Y, or the one of
// the least SORT.
func (s *Server) portalLinkSet(c portalCall, l portalLink) (portalOut, error) {
	id, err := s.portalLinkOwner(c, l, "write")
	if err != nil {
		return portalOut{}, err
	}
	list := portalLinkList(portalParam(c.in, "items"))
	slices.SortStableFunc(list, func(a, b map[string]any) int {
		x, _ := strconv.Atoi(portalStr(portalParam(a, "SORT")))
		y, _ := strconv.Atoi(portalStr(portalParam(b, "SORT")))
		return x - y
	})
	var items []string
	for _, it := range list {
		uid, primary, err := s.portalLinkIn(c.r.Context(), l, it)
		if err != nil {
			return portalOut{}, err
		}
		if slices.Contains(items, uid) {
			continue
		}
		if primary {
			items = append([]string{uid}, items...)
		} else {
			items = append(items, uid)
		}
	}
	return portalOut{Result: true}, s.portalLinkSave(c, l, id, items)
}

// crm.*.items.delete: id — the card without items.
func (s *Server) portalLinkClear(c portalCall, l portalLink) (portalOut, error) {
	id, err := s.portalLinkOwner(c, l, "write")
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.portalLinkSave(c, l, id, nil)
}

// crm.*.add: id, fields {CONTACT_ID, IS_PRIMARY} — one more item (the main one when IS_PRIMARY=Y; a contact's company
// is replaced).
func (s *Server) portalLinkAdd(c portalCall, l portalLink) (portalOut, error) {
	id, err := s.portalLinkOwner(c, l, "write")
	if err != nil {
		return portalOut{}, err
	}
	uid, primary, err := s.portalLinkIn(c.r.Context(), l, portalMap(portalParam(c.in, "fields")))
	if err != nil {
		return portalOut{}, err
	}
	items, err := s.portalLinkItems(c.r.Context(), l, id)
	if err != nil {
		return portalOut{}, err
	}
	switch {
	case l.Owner == "contact" || primary:
		items = append([]string{uid}, slices.DeleteFunc(items, func(x string) bool { return x == uid })...)
	case slices.Contains(items, uid):
		return portalOut{Result: true}, nil
	default:
		items = append(items, uid)
	}
	return portalOut{Result: true}, s.portalLinkSave(c, l, id, items)
}

// crm.*.delete: id, fields {CONTACT_ID} — the item off the card.
func (s *Server) portalLinkDelete(c portalCall, l portalLink) (portalOut, error) {
	id, err := s.portalLinkOwner(c, l, "write")
	if err != nil {
		return portalOut{}, err
	}
	uid, _, err := s.portalLinkIn(c.r.Context(), l, portalMap(portalParam(c.in, "fields")))
	if err != nil {
		return portalOut{}, err
	}
	items, err := s.portalLinkItems(c.r.Context(), l, id)
	if err != nil || !slices.Contains(items, uid) {
		return portalOut{Result: err == nil}, err
	}
	return portalOut{Result: true}, s.portalLinkSave(c, l, id, slices.DeleteFunc(items, func(x string) bool { return x == uid }))
}

// crm.*.fields of a link.
func (s *Server) portalLinkFields(c portalCall, l portalLink) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	for _, f := range []struct {
		name, typ          string
		required, readOnly bool
	}{{l.Key, "integer", true, false}, {"SORT", "integer", false, false}, {"ROLE_ID", "integer", false, true},
		{"IS_PRIMARY", "char", false, false}} {
		out[f.name] = map[string]any{"type": f.typ, "isRequired": f.required, "isReadOnly": f.readOnly, "isImmutable": false,
			"isMultiple": false, "isDynamic": false, "title": s.I18n.T(lang, "portal.field."+f.name)}
	}
	return portalOut{Result: out}, nil
}

// crm.duplicate.findbycomm: type (PHONE, EMAIL), values (up to 20), entity_type (LEAD, CONTACT, COMPANY; none — all
// of them) → {CONTACT: [numbers]…} of the cards the caller may read; none found — [].
func (s *Server) portalFindByComm(c portalCall) (portalOut, error) {
	ctx := c.r.Context()
	kind := strings.ToLower(portalStr(portalParam(c.in, "type")))
	if kind != "phone" && kind != "email" {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_comm_type")
	}
	vals := portalList(portalParam(c.in, "values"))
	if len(vals) == 0 || len(vals) > 20 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_comm_values")
	}
	var keys []string
	for _, v := range vals {
		if k := crm.CommKey(kind, v); k != "" {
			keys = append(keys, k)
		}
	}
	types := []string{"LEAD", "CONTACT", "COMPANY"}
	if t := strings.ToUpper(portalStr(portalParam(c.in, "entity_type"))); t != "" {
		if !slices.Contains(types, t) {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_entity_type", t)
		}
		types = []string{t}
	}
	out := map[string][]int64{}
	for _, t := range types {
		e := portalEntities[strings.ToLower(t)]
		scope, err := s.CRM.ReadScope(ctx, c.p.UserID, e.Entity, "x", 1)
		if err != nil {
			return portalOut{}, err
		}
		rows, err := s.DB.Query(ctx, `SELECT x.num FROM `+e.From+` WHERE (`+scope+`) AND EXISTS (SELECT 1 FROM crm_comms m
			WHERE m.entity = $2 AND m.entity_id = x.id AND m.kind = $3 AND m.key = ANY($4)) ORDER BY x.num`, c.p.UserID, e.Entity, kind, keys)
		if err != nil {
			return portalOut{}, err
		}
		nums, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return portalOut{}, err
		}
		if len(nums) > 0 {
			out[t] = nums
		}
	}
	if len(out) == 0 {
		return portalOut{Result: []any{}}, nil
	}
	return portalOut{Result: out}, nil
}

// crm.multifield.fields: the fields of a phone, an e-mail, a site.
func (s *Server) portalMultiFields(c portalCall) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	for _, n := range []string{"ID", "TYPE_ID", "VALUE", "VALUE_TYPE"} {
		typ := map[bool]string{true: "integer", false: "string"}[n == "ID"]
		out[n] = map[string]any{"type": typ, "isRequired": n == "VALUE", "isReadOnly": n == "ID", "isImmutable": false,
			"isMultiple": false, "isDynamic": false, "title": s.I18n.T(lang, "portal.multifield."+n)}
	}
	return portalOut{Result: out}, nil
}
