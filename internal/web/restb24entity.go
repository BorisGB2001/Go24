package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/apps"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal, the data of applications (GO24DEV-979): app.option.get/set — the application's settings
// (set by administrators), user.option.get/set — the caller's; entity.* — the storage of the application (entities,
// sections, items, properties) with the rights of access codes; access.name and user.access — the codes U<number>,
// D<number>, DR<number> (with subdepartments), G1 (administrators), G2, AU, UA (everyone). An incoming webhook keeps its
// data in its own application (apps.WebhookApp) keeps a webhook's.

func init() {
	for name, m := range map[string]portalMethod{
		"app.option.get":              {Run: (*Server).portalOptionGet},
		"app.option.set":              {Run: (*Server).portalOptionSet},
		"user.option.get":             {Run: (*Server).portalOptionGet},
		"user.option.set":             {Run: (*Server).portalOptionSet},
		"access.name":                 {Run: (*Server).portalAccessName},
		"user.access":                 {Run: (*Server).portalUserAccess},
		"entity.add":                  {Run: (*Server).portalEntityAdd},
		"entity.get":                  {Run: (*Server).portalEntityGet},
		"entity.update":               {Run: (*Server).portalEntityUpdate},
		"entity.delete":               {Run: (*Server).portalEntityDelete},
		"entity.rights":               {Run: (*Server).portalEntityRights},
		"entity.item.property.add":    {Run: (*Server).portalEntityPropAdd},
		"entity.item.property.get":    {Run: (*Server).portalEntityPropGet},
		"entity.item.property.update": {Run: (*Server).portalEntityPropUpdate},
		"entity.item.property.delete": {Run: (*Server).portalEntityPropDelete},
		"entity.section.add":          {Run: (*Server).portalEntitySectionAdd},
		"entity.section.get":          {Run: (*Server).portalEntitySectionGet},
		"entity.section.update":       {Run: (*Server).portalEntitySectionUpdate},
		"entity.section.delete":       {Run: (*Server).portalEntitySectionDelete},
		"entity.item.add":             {Run: (*Server).portalEntityItemAdd},
		"entity.item.get":             {Run: (*Server).portalEntityItemGet},
		"entity.item.update":          {Run: (*Server).portalEntityItemUpdate},
		"entity.item.delete":          {Run: (*Server).portalEntityItemDelete},
	} {
		portalMethods[name] = m
	}
}

const portalEntityFileTTL = 24 * time.Hour // how long the address of an item's file works

// portalDataApp is the application whose data the call reads: of the token, or the webhook's own.
func (s *Server) portalDataApp(c portalCall) (apps.App, error) {
	if !strings.HasPrefix(c.p.KeyID, "app:") {
		if s.Apps == nil {
			return apps.App{}, i18n.E("err.not_found")
		}
		return s.Apps.WebhookApp(c.r.Context(), c.p.KeyID)
	}
	return s.portalAppOf(c, false)
}

// ---------- settings ----------

// app.option.get, user.option.get: option= — its value (null — none), without — all {name: value}.
func (s *Server) portalOptionGet(c portalCall) (portalOut, error) {
	a, err := s.portalDataApp(c)
	if err != nil {
		return portalOut{}, err
	}
	user := ""
	if strings.HasPrefix(c.method, "user.") {
		user = c.p.UserID
	}
	list, err := s.Apps.Options(c.r.Context(), a.ID, user)
	if err != nil {
		return portalOut{}, err
	}
	if name := portalStr(portalParam(c.in, "option")); name != "" {
		v, ok := list[name]
		if !ok {
			return portalOut{Result: nil}, nil
		}
		return portalOut{Result: v}, nil
	}
	return portalOut{Result: list}, nil
}

// app.option.set (administrators), user.option.set: options — {name: value}; null removes the setting.
func (s *Server) portalOptionSet(c portalCall) (portalOut, error) {
	a, err := s.portalDataApp(c)
	if err != nil {
		return portalOut{}, err
	}
	user := c.p.UserID
	if strings.HasPrefix(c.method, "app.") {
		if !s.apiAdmin(c.r.Context(), c.p.UserID) {
			return portalOut{}, tasks.ErrForbidden
		}
		user = ""
	}
	opts := portalMap(portalParam(c.in, "options"))
	if len(opts) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "options")
	}
	set := map[string]json.RawMessage{}
	for k, v := range opts {
		raw, err := json.Marshal(v)
		if err != nil {
			return portalOut{}, err
		}
		set[k] = raw
	}
	if err := s.Apps.SetOptions(c.r.Context(), a.ID, user, set); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// ---------- access codes ----------

// portalAccessCodes are the access codes the employee holds: U<number>, D<number> of their departments (the head's too),
// DR<number> of those and the ones above them, G1 of an administrator, and G2, AU, UA of everyone.
func (s *Server) portalAccessCodes(ctx context.Context, userID string) ([]string, error) {
	codes := []string{"G2", "AU", "UA"}
	var num int64
	if err := s.DB.QueryRow(ctx, `SELECT num FROM users WHERE id::text = $1`, userID).Scan(&num); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return codes, nil
		}
		return nil, err
	}
	codes = append(codes, "U"+strconv.FormatInt(num, 10))
	if s.apiAdmin(ctx, userID) {
		codes = append(codes, "G1")
	}
	rows, err := s.DB.Query(ctx, `WITH RECURSIVE mine AS (
			SELECT d.id, d.num, d.parent_id FROM departments d WHERE d.head_id::text = $1
				OR EXISTS (SELECT 1 FROM user_departments ud WHERE ud.department_id = d.id AND ud.user_id::text = $1)),
		up AS (SELECT id, num, parent_id FROM mine UNION SELECT p.id, p.num, p.parent_id FROM departments p JOIN up ON p.id = up.parent_id)
		SELECT 'D' || num FROM mine UNION SELECT 'DR' || num FROM up`, userID)
	if err != nil {
		return nil, err
	}
	more, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return append(codes, more...), err
}

// portalAccessList is a list of access codes of a parameter, in upper case.
func portalAccessList(v any) []string {
	var out []string
	for _, x := range portalList(v) {
		if x = strings.ToUpper(strings.TrimSpace(x)); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// user.access: ACCESS — codes → true, when the caller holds one of them.
func (s *Server) portalUserAccess(c portalCall) (portalOut, error) {
	want := portalAccessList(portalParam(c.in, "ACCESS"))
	if len(want) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "ACCESS")
	}
	mine, err := s.portalAccessCodes(c.r.Context(), c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: slices.ContainsFunc(want, func(x string) bool { return slices.Contains(mine, x) })}, nil
}

// access.name: ACCESS — codes → {code: {provider, provider_id, name}}; the codes go24 does not know are left out.
func (s *Server) portalAccessName(c portalCall) (portalOut, error) {
	codes := portalAccessList(portalParam(c.in, "ACCESS"))
	if len(codes) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "ACCESS")
	}
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	name := func(code, provider, title string) {
		out[code] = map[string]string{"provider": s.I18n.T(lang, "access.provider."+provider), "provider_id": provider, "name": title}
	}
	for _, code := range codes {
		var title string
		var err error
		switch {
		case code == "G1":
			name(code, "group", s.I18n.T(lang, "access.g1"))
		case code == "G2" || code == "UA":
			name(code, "group", s.I18n.T(lang, "access.g2"))
		case code == "AU":
			name(code, "other", s.I18n.T(lang, "access.au"))
		case strings.HasPrefix(code, "U"):
			err = s.DB.QueryRow(c.r.Context(), `SELECT name FROM users WHERE num::text = $1`, code[1:]).Scan(&title)
			if err == nil {
				name(code, "user", title)
			}
		case strings.HasPrefix(code, "DR"):
			err = s.DB.QueryRow(c.r.Context(), `SELECT name FROM departments WHERE num::text = $1`, code[2:]).Scan(&title)
			if err == nil {
				name(code, "intranet", s.I18n.T(lang, "access.dr", title))
			}
		case strings.HasPrefix(code, "D"):
			err = s.DB.QueryRow(c.r.Context(), `SELECT name FROM departments WHERE num::text = $1`, code[1:]).Scan(&title)
			if err == nil {
				name(code, "intranet", title)
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return portalOut{}, err
		}
	}
	return portalOut{Result: out}, nil
}

// ---------- entities ----------

// portalEntityErr gives the errors of the storage the codes.
func (s *Server) portalEntityErr(c portalCall, err error) error {
	for _, x := range []struct {
		err  error
		code string
	}{{apps.ErrEntityNotFound, "ERROR_ENTITY_NOT_FOUND"}, {apps.ErrEntityExists, "ERROR_ENTITY_ALREADY_EXISTS"},
		{apps.ErrEntityPropAbsent, "ERROR_PROPERTY_NOT_FOUND"}, {apps.ErrEntityPropExists, "ERROR_PROPERTY_ALREADY_EXISTS"},
		{apps.ErrEntitySection, "ERROR_SECTION_NOT_FOUND"}, {apps.ErrEntityItem, "ERROR_ITEM_NOT_FOUND"},
		{apps.ErrEntityCode, "ERROR_ARGUMENT"}, {apps.ErrEntityName, "ERROR_ARGUMENT"}, {apps.ErrEntityProp, "ERROR_ARGUMENT"},
		{apps.ErrEntityAccess, "ERROR_ARGUMENT"}} {
		if errors.Is(err, x.err) {
			var ie *i18n.Error
			errors.As(err, &ie)
			return s.portalErr(c.r, http.StatusBadRequest, x.code, ie.Key, ie.Args...)
		}
	}
	return err
}

// portalEntity is the entity of ENTITY the caller has the level to (R, W or X); administrators may everything.
func (s *Server) portalEntity(c portalCall, level string) (apps.Entity, error) {
	a, err := s.portalDataApp(c)
	if err != nil {
		return apps.Entity{}, err
	}
	code := strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY")))
	if code == "" {
		return apps.Entity{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "ENTITY")
	}
	e, err := s.Apps.EntityByCode(c.r.Context(), a.ID, code)
	if err != nil {
		return e, s.portalEntityErr(c, err)
	}
	have, err := s.portalEntityLevel(c.r.Context(), c.p.UserID, e)
	if err != nil {
		return e, err
	}
	if have < level {
		return e, tasks.ErrForbidden
	}
	return e, nil
}

// portalEntityLevel is the caller's right to the entity: X of administrators, else the highest of their codes.
func (s *Server) portalEntityLevel(ctx context.Context, userID string, e apps.Entity) (string, error) {
	if s.apiAdmin(ctx, userID) {
		return apps.LevelFull, nil
	}
	codes, err := s.portalAccessCodes(ctx, userID)
	return apps.Level(e.Access, codes), err
}

// portalEntityAccess is ACCESS of the call ({code: R|W|X}); nil — not given.
func (s *Server) portalEntityAccess(c portalCall) (map[string]string, error) {
	v, ok := portalParamOK(c.in, "ACCESS")
	if !ok {
		return nil, nil
	}
	in := map[string]string{}
	for k, x := range portalMap(v) {
		in[k] = portalStr(x)
	}
	access, err := apps.CleanAccess(in)
	if err != nil {
		return nil, s.portalEntityErr(c, err)
	}
	return access, nil
}

// entity.add: ENTITY (letters, digits, _; up to 13), NAME, ACCESS — by an administrator.
func (s *Server) portalEntityAdd(c portalCall) (portalOut, error) {
	a, err := s.portalDataApp(c)
	if err != nil {
		return portalOut{}, err
	}
	if !s.apiAdmin(c.r.Context(), c.p.UserID) {
		return portalOut{}, tasks.ErrForbidden
	}
	access, err := s.portalEntityAccess(c)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Apps.AddEntity(c.r.Context(), a.ID, strings.TrimSpace(portalStr(portalParam(c.in, "ENTITY"))), portalStr(portalParam(c.in, "NAME")), access)
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

func portalEntityOut(e apps.Entity) map[string]any {
	return map[string]any{"ID": e.ID, "IBLOCK_TYPE_ID": "rest_entity", "ENTITY": e.Code, "NAME": e.Name}
}

// entity.get: ENTITY — that entity; without — the entities the caller may read.
func (s *Server) portalEntityGet(c portalCall) (portalOut, error) {
	if portalStr(portalParam(c.in, "ENTITY")) != "" {
		e, err := s.portalEntity(c, apps.LevelRead)
		if err != nil {
			return portalOut{}, err
		}
		return portalOut{Result: portalEntityOut(e)}, nil
	}
	a, err := s.portalDataApp(c)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.Entities(c.r.Context(), a.ID)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, e := range list {
		l, err := s.portalEntityLevel(c.r.Context(), c.p.UserID, e)
		if err != nil {
			return portalOut{}, err
		}
		if l != "" {
			out = append(out, portalEntityOut(e))
		}
	}
	return portalOut{Result: out}, nil
}

// portalStrOpt is a text parameter when given.
func portalStrOpt(in map[string]any, name string) *string {
	v, ok := portalParamOK(in, name)
	if !ok {
		return nil
	}
	x := portalStr(v)
	return &x
}

// entity.update: ENTITY, NAME, ENTITY_NEW, ACCESS — with the right X.
func (s *Server) portalEntityUpdate(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelFull)
	if err != nil {
		return portalOut{}, err
	}
	access, err := s.portalEntityAccess(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.UpdateEntity(c.r.Context(), e, portalStrOpt(c.in, "NAME"), portalStrOpt(c.in, "ENTITY_NEW"), access); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.delete: ENTITY — with its sections and items, with the right X.
func (s *Server) portalEntityDelete(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelFull)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.Apps.DeleteEntity(c.r.Context(), e.ID)
}

// entity.rights: ENTITY → {code: R|W|X}; with ACCESS — sets them first (the right X).
func (s *Server) portalEntityRights(c portalCall) (portalOut, error) {
	access, err := s.portalEntityAccess(c)
	if err != nil {
		return portalOut{}, err
	}
	need := apps.LevelRead
	if access != nil {
		need = apps.LevelFull
	}
	e, err := s.portalEntity(c, need)
	if err != nil {
		return portalOut{}, err
	}
	if access != nil {
		if err := s.Apps.UpdateEntity(c.r.Context(), e, nil, nil, access); err != nil {
			return portalOut{}, s.portalEntityErr(c, err)
		}
		e.Access = access
	}
	return portalOut{Result: e.Access}, nil
}

// ---------- properties ----------

func portalEntityPropIn(c portalCall, code string) apps.EntityProp {
	return apps.EntityProp{Code: strings.TrimSpace(portalStr(portalParam(c.in, code))), Name: portalStr(portalParam(c.in, "NAME")),
		Type: strings.ToUpper(strings.TrimSpace(portalStr(portalParam(c.in, "TYPE"))))}
}

// entity.item.property.add: ENTITY, PROPERTY, NAME, TYPE (S — a string, N — a number, F — a file) — the right X.
func (s *Server) portalEntityPropAdd(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelFull)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.AddEntityProp(c.r.Context(), e.ID, portalEntityPropIn(c, "PROPERTY")); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.item.property.get: ENTITY, PROPERTY — that one {PROPERTY, NAME, TYPE}; without — all of them.
func (s *Server) portalEntityPropGet(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelRead)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.Apps.EntityProps(c.r.Context(), e.ID)
	if err != nil {
		return portalOut{}, err
	}
	if code := strings.TrimSpace(portalStr(portalParam(c.in, "PROPERTY"))); code != "" {
		i := slices.IndexFunc(list, func(p apps.EntityProp) bool { return strings.EqualFold(p.Code, code) })
		if i < 0 {
			return portalOut{}, s.portalEntityErr(c, apps.ErrEntityPropAbsent)
		}
		return portalOut{Result: list[i]}, nil
	}
	if list == nil {
		list = []apps.EntityProp{}
	}
	return portalOut{Result: list}, nil
}

// entity.item.property.update: ENTITY, PROPERTY, PROPERTY_NEW, NAME, TYPE — the right X.
func (s *Server) portalEntityPropUpdate(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelFull)
	if err != nil {
		return portalOut{}, err
	}
	err = s.Apps.UpdateEntityProp(c.r.Context(), e.ID, strings.TrimSpace(portalStr(portalParam(c.in, "PROPERTY"))), portalEntityPropIn(c, "PROPERTY_NEW"))
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.item.property.delete: ENTITY, PROPERTY — with its values; the right X.
func (s *Server) portalEntityPropDelete(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelFull)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.DeleteEntityProp(c.r.Context(), e.ID, strings.TrimSpace(portalStr(portalParam(c.in, "PROPERTY")))); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// ---------- lists ----------

// portalEntityField is the field of a list as the storage names it: IBLOCK_SECTION_ID is SECTION, PROPERTY_<code> — P:<code>.
func portalEntityField(f string) string {
	f = strings.ToUpper(strings.TrimSpace(f))
	if f == "IBLOCK_SECTION_ID" || f == "IBLOCK_SECTION" {
		return "SECTION"
	}
	if code, ok := strings.CutPrefix(f, "PROPERTY_"); ok {
		return "P:" + code
	}
	return f
}

// portalEntityConds reads FILTER: {"<op><field>": value or [values]}, the operators =, !, !=, >, <, >=, <=,
// % (contains), !%.
func portalEntityConds(v any) []apps.EntityCond {
	var out []apps.EntityCond
	for k, x := range portalMap(v) {
		op := "="
		for _, o := range []string{">=", "<=", "!%", "!=", "%", "=", "!", ">", "<"} {
			if strings.HasPrefix(k, o) {
				op, k = o, k[len(o):]
				break
			}
		}
		if op == "!=" {
			op = "!"
		}
		var vals []string
		switch y := x.(type) {
		case []any:
			vals = portalList(y)
		case bool:
			vals = []string{map[bool]string{true: "Y", false: ""}[y]}
		case nil:
			vals = []string{""}
		default:
			vals = []string{portalStr(y)}
		}
		if len(vals) == 0 {
			vals = []string{""}
		}
		out = append(out, apps.EntityCond{Field: portalEntityField(k), Op: op, Vals: vals})
	}
	slices.SortFunc(out, func(a, b apps.EntityCond) int { return strings.Compare(a.Field+a.Op, b.Field+b.Op) })
	return out
}

// portalEntitySort reads SORT: {field: ASC|DESC}; the order of a map is not kept, so the fields go by name.
func portalEntitySort(v any) []apps.EntitySort {
	var out []apps.EntitySort
	for k, x := range portalMap(v) {
		out = append(out, apps.EntitySort{Field: portalEntityField(k), Desc: strings.EqualFold(strings.TrimSpace(portalStr(x)), "DESC")})
	}
	slices.SortFunc(out, func(a, b apps.EntitySort) int { return strings.Compare(a.Field, b.Field) })
	return out
}

// portalEntityTime is a time of a record gives it; none — null.
func portalEntityTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.In(time.Local).Format(time.RFC3339)
}

// portalEntityNum is a number of a record as text gives the numbers of iblocks; 0 — null.
func portalEntityNum(n int64) any {
	if n == 0 {
		return nil
	}
	return strconv.FormatInt(n, 10)
}

// ---------- sections ----------

// portalEntitySet reads the fields of a section or an item given in the call; the others stay as they are.
func (s *Server) portalEntitySet(c portalCall, fields []string) (apps.EntitySet, error) {
	set := apps.EntitySet{}
	for _, f := range fields {
		v, ok := portalParamOK(c.in, f)
		if !ok && f == "SECTION" {
			v, ok = portalParamOK(c.in, "IBLOCK_SECTION_ID")
		}
		if !ok {
			continue
		}
		x := strings.TrimSpace(portalStr(v))
		bad := s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", f)
		switch f {
		case "SORT":
			n, err := strconv.Atoi(x)
			if err != nil && x != "" {
				return nil, bad
			}
			if x == "" {
				n = 500
			}
			set[f] = n
		case "ACTIVE":
			set[f] = !strings.EqualFold(x, "N") && x != "0" && !strings.EqualFold(x, "false")
		case "SECTION":
			n, err := strconv.ParseInt(x, 10, 64)
			if err != nil && x != "" {
				return nil, bad
			}
			set[f] = n
		case "DATE_ACTIVE_FROM", "DATE_ACTIVE_TO":
			if x == "" {
				set[f] = nil
				continue
			}
			t, err := apps.ParseEntityTime(x)
			if err != nil {
				return nil, bad
			}
			set[f] = t
		case "NAME":
			set[f] = x
		default:
			set[f] = portalStr(v)
		}
	}
	return set, nil
}

var portalSectionFields = []string{"NAME", "CODE", "DESCRIPTION", "SORT", "ACTIVE", "SECTION"}

// portalEntityID is ID of the call — a number of a section or an item.
func (s *Server) portalEntityID(c portalCall) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(portalStr(portalParam(c.in, "ID"))), 10, 64)
	if err != nil || id <= 0 {
		return 0, s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", "ID")
	}
	return id, nil
}

// entity.section.add: ENTITY, NAME, CODE, DESCRIPTION, SORT, ACTIVE, SECTION (the one above) → its number; the right W.
func (s *Server) portalEntitySectionAdd(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	set, err := s.portalEntitySet(c, portalSectionFields)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.Apps.AddEntitySection(c.r.Context(), e.ID, c.p.UserID, set)
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: id}, nil
}

// entity.section.get: ENTITY, FILTER, SORT, start → the sections, 50 a page.
func (s *Server) portalEntitySectionGet(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelRead)
	if err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	list, total, err := s.Apps.EntitySections(c.r.Context(), e.ID, portalEntityConds(portalParam(c.in, "FILTER")),
		portalEntitySort(portalParam(c.in, "SORT")), start, portalPage)
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	out := []map[string]any{}
	for _, x := range list {
		out = append(out, map[string]any{"ID": portalEntityNum(x.ID), "CODE": x.Code, "ENTITY": e.Code, "NAME": x.Name,
			"DESCRIPTION": x.Description, "SECTION": portalEntityNum(x.Parent), "SORT": strconv.Itoa(x.Sort), "ACTIVE": portalYN(x.Active),
			"DEPTH_LEVEL": strconv.Itoa(x.Depth), "DATE_CREATE": portalEntityTime(&x.CreatedAt), "CREATED_BY": portalEntityNum(x.CreatedBy),
			"TIMESTAMP_X": portalEntityTime(&x.UpdatedAt), "MODIFIED_BY": portalEntityNum(x.UpdatedBy)})
	}
	return portalPaged(out, start, len(out), total), nil
}

// entity.section.update: ENTITY, ID and the fields to change; the right W.
func (s *Server) portalEntitySectionUpdate(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalEntityID(c)
	if err != nil {
		return portalOut{}, err
	}
	set, err := s.portalEntitySet(c, portalSectionFields)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.UpdateEntitySection(c.r.Context(), e.ID, c.p.UserID, id, set); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.section.delete: ENTITY, ID — with the sections and items in it; the right W.
func (s *Server) portalEntitySectionDelete(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalEntityID(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.DeleteEntitySection(c.r.Context(), e.ID, id); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// ---------- items ----------

var portalItemFields = []string{"NAME", "CODE", "SORT", "ACTIVE", "SECTION", "DATE_ACTIVE_FROM", "DATE_ACTIVE_TO", "PREVIEW_TEXT", "DETAIL_TEXT"}

// portalEntityFile reads a file: [name, base64] or {fileData: [name, base64]}; "", null, {remove: Y} and
// {del: Y} remove the file (nil).
func (s *Server) portalEntityFile(c portalCall, field string, v any) (*apps.EntityFileIn, error) {
	bad := s.portalErr(c.r, http.StatusBadRequest, "ERROR_ARGUMENT", "err.portal_field", field)
	if m := portalMap(v); m != nil {
		if fd, ok := portalParamOK(m, "fileData"); ok {
			v = fd
		} else if portalStr(portalParam(m, "remove")) == "Y" || portalStr(portalParam(m, "del")) == "Y" {
			return nil, nil
		}
	}
	if v == nil || portalStr(v) == "" {
		if _, isList := v.([]any); !isList {
			return nil, nil
		}
	}
	pairs, ok := portalFilePairs(v)
	if !ok || len(pairs) != 1 {
		return nil, bad
	}
	data, err := base64.StdEncoding.DecodeString(pairs[0][1])
	if err != nil {
		return nil, bad
	}
	return &apps.EntityFileIn{Name: pairs[0][0], Data: data}, nil
}

// portalEntityItemIn reads the fields, PROPERTY_VALUES and the pictures of an item.
func (s *Server) portalEntityItemIn(c portalCall, props []apps.EntityProp) (apps.EntityItemIn, error) {
	set, err := s.portalEntitySet(c, portalItemFields)
	if err != nil {
		return apps.EntityItemIn{}, err
	}
	in := apps.EntityItemIn{Set: set, Props: map[string]string{}, Files: map[string]*apps.EntityFileIn{}}
	for _, slot := range []string{"PREVIEW_PICTURE", "DETAIL_PICTURE"} {
		if v, ok := portalParamOK(c.in, slot); ok {
			if in.Files[slot], err = s.portalEntityFile(c, slot, v); err != nil {
				return in, err
			}
		}
	}
	for code, v := range portalMap(portalParam(c.in, "PROPERTY_VALUES")) {
		i := slices.IndexFunc(props, func(p apps.EntityProp) bool { return strings.EqualFold(p.Code, code) })
		switch {
		case i < 0:
		case props[i].Type == "F":
			if in.Files["P:"+props[i].Code], err = s.portalEntityFile(c, "PROPERTY_VALUES["+code+"]", v); err != nil {
				return in, err
			}
		default:
			in.Props[props[i].Code] = portalStr(v)
		}
	}
	return in, nil
}

// entity.item.add: ENTITY, NAME, the fields, PROPERTY_VALUES, PREVIEW_PICTURE, DETAIL_PICTURE → its number; the right W.
func (s *Server) portalEntityItemAdd(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	props, err := s.Apps.EntityProps(c.r.Context(), e.ID)
	if err != nil {
		return portalOut{}, err
	}
	in, err := s.portalEntityItemIn(c, props)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.Apps.AddEntityItem(c.r.Context(), e.ID, c.p.UserID, props, in)
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: id}, nil
}

// entity.item.update: ENTITY, ID and what to change — the fields, PROPERTY_VALUES (the others stay), files; the right W.
func (s *Server) portalEntityItemUpdate(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalEntityID(c)
	if err != nil {
		return portalOut{}, err
	}
	props, err := s.Apps.EntityProps(c.r.Context(), e.ID)
	if err != nil {
		return portalOut{}, err
	}
	in, err := s.portalEntityItemIn(c, props)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.UpdateEntityItem(c.r.Context(), e.ID, c.p.UserID, id, props, in); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.item.delete: ENTITY, ID; the right W.
func (s *Server) portalEntityItemDelete(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelWrite)
	if err != nil {
		return portalOut{}, err
	}
	id, err := s.portalEntityID(c)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.Apps.DeleteEntityItem(c.r.Context(), e.ID, id); err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// entity.item.get: ENTITY, FILTER (the fields, PROPERTY_<code>), SORT, start → the items, 50 a page; a file is a
// signed address that works a day.
func (s *Server) portalEntityItemGet(c portalCall) (portalOut, error) {
	e, err := s.portalEntity(c, apps.LevelRead)
	if err != nil {
		return portalOut{}, err
	}
	props, err := s.Apps.EntityProps(c.r.Context(), e.ID)
	if err != nil {
		return portalOut{}, err
	}
	start := portalStart(c.in)
	list, total, err := s.Apps.EntityItems(c.r.Context(), e.ID, props, portalEntityConds(portalParam(c.in, "FILTER")),
		portalEntitySort(portalParam(c.in, "SORT")), start, portalPage)
	if err != nil {
		return portalOut{}, s.portalEntityErr(c, err)
	}
	file := func(x apps.EntityItem, slot string) any {
		f, ok := x.Files[slot]
		if !ok {
			return nil
		}
		return s.portalEntityFileLink(c.p.UserID, f.ID)
	}
	out := []map[string]any{}
	for _, x := range list {
		values := map[string]any{}
		for _, p := range props {
			if p.Type == "F" {
				values[p.Code] = file(x, "P:"+p.Code)
			} else if v, ok := x.Props[p.Code]; ok {
				values[p.Code] = v
			} else {
				values[p.Code] = nil
			}
		}
		out = append(out, map[string]any{"ID": portalEntityNum(x.ID), "ENTITY": e.Code, "NAME": x.Name, "CODE": x.Code,
			"ACTIVE": portalYN(x.Active), "SORT": strconv.Itoa(x.Sort), "IBLOCK_SECTION_ID": portalEntityNum(x.Section),
			"DATE_ACTIVE_FROM": portalEntityTime(x.ActiveFrom), "DATE_ACTIVE_TO": portalEntityTime(x.ActiveTo),
			"PREVIEW_TEXT": x.PreviewText, "DETAIL_TEXT": x.DetailText,
			"PREVIEW_PICTURE": file(x, "PREVIEW_PICTURE"), "DETAIL_PICTURE": file(x, "DETAIL_PICTURE"), "PROPERTY_VALUES": values,
			"DATE_CREATE": portalEntityTime(&x.CreatedAt), "CREATED_BY": portalEntityNum(x.CreatedBy),
			"TIMESTAMP_X": portalEntityTime(&x.UpdatedAt), "MODIFIED_BY": portalEntityNum(x.UpdatedBy)})
	}
	return portalPaged(out, start, len(out), total), nil
}

// ---------- files ----------

// portalEntityFileTicket is what the address of an item's file gives: the file, read as user.
type portalEntityFileTicket struct {
	User string `json:"u"`
	File int64  `json:"f"`
	Exp  int64  `json:"e"`
}

// portalEntityFileLink is the signed address of a file of an item for the employee.
func (s *Server) portalEntityFileLink(user string, file int64) string {
	raw, _ := json.Marshal(portalEntityFileTicket{User: user, File: file, Exp: time.Now().Add(portalEntityFileTTL).Unix()})
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return s.BaseURL + "/rest/entityfile?token=" + payload + "." + s.Sessions.Stamp("portal-entity-file", payload)
}

// portalEntityFileGet: GET /rest/entityfile?token= — the bytes of a file of an item, while its holder is an active employee
// who may read the entity.
func (s *Server) portalEntityFileGet(w http.ResponseWriter, r *http.Request) {
	var t portalEntityFileTicket
	payload, stamp, _ := strings.Cut(r.URL.Query().Get("token"), ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || !s.Sessions.Stamped(stamp, "portal-entity-file", payload) || json.Unmarshal(raw, &t) != nil || time.Now().Unix() > t.Exp {
		s.portalFail(w, r, i18n.E("err.portal_disk_link"))
		return
	}
	var live bool
	if err := s.DB.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, t.User).Scan(&live); err != nil || !live {
		s.portalFail(w, r, i18n.E("err.portal_disk_link"))
		return
	}
	e, name, data, err := s.Apps.EntityFileData(r.Context(), t.File)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	if l, err := s.portalEntityLevel(r.Context(), t.User, e); err != nil || l == "" {
		s.portalFail(w, r, tasks.ErrForbidden)
		return
	}
	mime, _ := imFileType(name, "", true)
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	_, _ = w.Write(data)
}
