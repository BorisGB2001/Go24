package web

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// REST compatible with the portal, user fields of the CRM (GO24DEV-957): the own fields of deals, leads, contacts and
// companies are UF_CRM_<code> (or UF_CRM_<number> — a field added in go24 has no code) in crm.*.fields, get, list
// (select UF_*), add and update; crm.*.userfield.* describe and change them (an administrator). A list's value is
// the number of its option from 1, as the ID of an enumeration. The fields of employees' profiles are
// UF_USR_* the same way (GO24DEV-985): user.userfield.*, in user.get, user.current, user.search and user.update.

// portalUF is a user field with its number and code.
type portalUF struct {
	tasks.Field
	Num    int64
	Code   string
	Prefix string // UF_CRM_, UF_USR_, UF_ (tasks)
}

// Name is the field's name.
func (u portalUF) Key() string {
	switch {
	case u.Code != "":
		return u.Prefix + u.Code
	case u.Prefix == "UF_": // a task's field names one added in the settings
		return "UF_AUTO_" + strconv.FormatInt(u.Num, 10)
	}
	return u.Prefix + strconv.FormatInt(u.Num, 10)
}

// portalUFSet is the field set of an entity: deal, lead, contact, company, user (employees) or task.
func portalUFSet(entity string) (tasks.FieldSet, bool) {
	switch entity {
	case "user":
		return tasks.UserFieldSet, true
	case "task":
		return tasks.TaskFieldSet, true
	}
	set, ok := tasks.CRMFieldSets[entity]
	return set, ok
}

// portalUFPrefix is the beginning of the names of an entity's fields.
func portalUFPrefix(entity string) string {
	switch entity {
	case "user":
		return "UF_USR_"
	case "task":
		return "UF_"
	}
	return "UF_CRM_"
}

// portalUFTypes: the kind of a go24 field → USER_TYPE_ID.
var portalUFTypes = map[string]string{"string": "string", "number": "double", "date": "date", "bool": "boolean", "list": "enumeration",
	"url": "url"}

// portalUFKinds: USER_TYPE_ID of userfield.add → the kind of a go24 field.
var portalUFKinds = map[string]string{"string": "string", "integer": "number", "double": "number", "date": "date", "datetime": "date",
	"boolean": "bool", "enumeration": "list", "url": "url"}

var portalUFCode = regexp.MustCompile(`^[A-Z0-9_]{1,40}$`)

func init() {
	for _, entity := range []string{"deal", "lead", "contact", "company"} {
		for action, m := range map[string]portalMethod{
			"list":   {Scope: "crm:read", Run: portalUFMethod(entity, (*Server).portalUFList)},
			"get":    {Scope: "crm:read", Run: portalUFMethod(entity, (*Server).portalUFGet)},
			"add":    {Scope: "crm:write", Run: portalUFMethod(entity, (*Server).portalUFAdd)},
			"update": {Scope: "crm:write", Run: portalUFMethod(entity, (*Server).portalUFUpdate)},
			"delete": {Scope: "crm:write", Run: portalUFMethod(entity, (*Server).portalUFDelete)},
		} {
			portalMethods["crm."+entity+".userfield."+action] = m
		}
	}
	for action, m := range map[string]portalMethod{
		"list":   {Scope: "company:read", Run: portalUFMethod("user", (*Server).portalUFList)},
		"get":    {Scope: "company:read", Run: portalUFMethod("user", (*Server).portalUFGet)},
		"add":    {Scope: "company:write", Run: portalUFMethod("user", (*Server).portalUFAdd)},
		"update": {Scope: "company:write", Run: portalUFMethod("user", (*Server).portalUFUpdate)},
		"delete": {Scope: "company:write", Run: portalUFMethod("user", (*Server).portalUFDelete)},
	} {
		portalMethods["user.userfield."+action] = m
	}
	portalMethods["crm.userfield.types"] = portalMethod{Scope: "crm:read", Run: (*Server).portalUFTypesList}
	portalMethods["crm.userfield.fields"] = portalMethod{Scope: "crm:read", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalUFDescribe(c, "ID", "ENTITY_ID", "FIELD_NAME", "USER_TYPE_ID", "XML_ID", "SORT", "MULTIPLE", "MANDATORY",
			"SHOW_FILTER", "SHOW_IN_LIST", "EDIT_IN_LIST", "IS_SEARCHABLE", "SETTINGS", "EDIT_FORM_LABEL", "LIST_COLUMN_LABEL",
			"LIST_FILTER_LABEL", "ERROR_MESSAGE", "HELP_MESSAGE", "LIST")
	}}
	portalMethods["crm.userfield.enumeration.fields"] = portalMethod{Scope: "crm:read", Run: func(s *Server, c portalCall) (portalOut, error) {
		return s.portalUFDescribe(c, "ID", "VALUE", "SORT", "DEF", "XML_ID")
	}}
}

func portalUFMethod(entity string, run func(*Server, portalCall, string) (portalOut, error)) func(*Server, portalCall) (portalOut, error) {
	return func(s *Server, c portalCall) (portalOut, error) { return run(s, c, entity) }
}

// portalUFs are the user fields of an entity in their order, those the caller sees («Видимость поля»).
func (s *Server) portalUFs(ctx context.Context, entity string, sight crm.FieldSight) ([]portalUF, error) {
	set, ok := portalUFSet(entity)
	if !ok {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT id::text, name, kind, options, sort, required, required_stages, visible_to, num, coalesce(code, '')
		FROM `+set.Defs()+` ORDER BY sort, num`)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (portalUF, error) {
		u := portalUF{Prefix: portalUFPrefix(entity)}
		err := r.Scan(&u.ID, &u.Name, &u.Kind, &u.Options, &u.Sort, &u.Required, &u.Stages, &u.Visible, &u.Num, &u.Code)
		return u, err
	})
	return slices.DeleteFunc(all, func(u portalUF) bool { return !sight.Sees(u.Field) }), err
}

// portalWithUF is the entity with its user fields after the built-in ones.
func (s *Server) portalWithUF(ctx context.Context, e portalEntity, sight crm.FieldSight) (portalEntity, error) {
	ufs, err := s.portalUFs(ctx, e.Entity, sight)
	if err != nil || len(ufs) == 0 {
		return e, err
	}
	set, _ := portalUFSet(e.Entity)
	fields := slices.Clip(e.Fields)
	for _, u := range ufs {
		val := `(SELECT v.value FROM ` + set.Values() + ` v WHERE v.` + set.Owner() + ` = x.id
			AND v.field_id = '` + u.ID + `')`
		f := portalField{Name: u.Key(), Type: portalUFTypes[u.Kind], Col: val, Required: u.Required && len(u.Stages) == 0, UF: &u}
		switch u.Kind {
		case "number":
			f.Col = val + "::float8"
		case "date":
			f.Col = val + "::date"
		case "bool":
			f.Col = "coalesce(" + val + ", '0')"
		case "list":
			f.Col = `(SELECT array_position(f.options, v.value)::bigint FROM ` + set.Values() + ` v
				JOIN ` + set.Defs() + ` f ON f.id = v.field_id WHERE v.` + set.Owner() + ` = x.id
				AND v.field_id = '` + u.ID + `')`
		}
		fields = append(fields, f)
	}
	e.Fields = fields
	return e, nil
}

// portalUFIn are the values of the user fields given to add or update (field id → value, "" clears): a list — the ID
// of the option or its text, yes/no — Y, 1 or true.
func portalUFIn(e portalEntity, fields map[string]any) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range fields {
		f, ok := e.field(strings.ToUpper(k))
		if !ok || f.UF == nil {
			continue
		}
		x, err := portalUFValue(*f.UF, v)
		if err != nil {
			return nil, err
		}
		out[f.UF.ID] = x
	}
	return out, nil
}

// portalUFValue is a value given to a user field as go24 keeps it.
func portalUFValue(u portalUF, v any) (string, error) {
	if list, ok := v.([]any); ok { // MULTIPLE=N: the first
		v = nil
		if len(list) > 0 {
			v = list[0]
		}
	}
	x := strings.TrimSpace(portalStr(v))
	switch u.Kind {
	case "bool":
		x = map[bool]string{true: "1"}[slices.Contains([]string{"Y", "1", "TRUE", "ON"}, strings.ToUpper(x))]
	case "date":
		d, err := portalDate(x)
		if err != nil {
			return "", err
		}
		x = ""
		if d != nil {
			x = d.Format(time.DateOnly)
		}
	case "list":
		if n, err := strconv.Atoi(x); err == nil && n >= 1 && n <= len(u.Options) {
			x = u.Options[n-1]
		}
	}
	return u.Check(x)
}

// portalUFValueOut is a kept value of a user field gives it (as portalWithUF and portalValue): a number and the
// option of a list as text, a date at midnight with the zone, yes/no — 1 or 0, not filled — null.
func portalUFValueOut(u portalUF, v string) any {
	switch {
	case u.Kind == "bool":
		return map[bool]string{true: "1", false: "0"}[v == "1"]
	case v == "":
		return nil
	case u.Kind == "number":
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return strconv.FormatFloat(n, 'f', 2, 64)
		}
	case u.Kind == "date":
		if d, err := time.ParseInLocation(time.DateOnly, v, time.Local); err == nil {
			return d.Format(time.RFC3339)
		}
	case u.Kind == "list":
		if i := slices.Index(u.Options, v); i >= 0 {
			return strconv.Itoa(i + 1)
		}
	}
	return v
}

// portalUFOut is a user field as crm.*.userfield.get gives it.
func portalUFOut(entity string, u portalUF) map[string]any {
	yn := map[bool]string{true: "Y", false: "N"}
	ent := "CRM_" + strings.ToUpper(entity)
	switch entity {
	case "user":
		ent = "USER"
	case "task":
		ent = "TASKS_TASK"
	}
	out := map[string]any{"ID": strconv.FormatInt(u.Num, 10), "ENTITY_ID": ent, "FIELD_NAME": u.Key(),
		"USER_TYPE_ID": portalUFTypes[u.Kind], "XML_ID": nil, "SORT": strconv.Itoa(u.Sort), "MULTIPLE": "N", "MANDATORY": yn[u.Required],
		"SHOW_FILTER": "E", "SHOW_IN_LIST": "Y", "EDIT_IN_LIST": "Y", "IS_SEARCHABLE": "N", "SETTINGS": map[string]any{},
		"EDIT_FORM_LABEL": u.Name, "LIST_COLUMN_LABEL": u.Name, "LIST_FILTER_LABEL": u.Name, "ERROR_MESSAGE": nil, "HELP_MESSAGE": nil}
	if u.Kind == "list" {
		list := []map[string]any{}
		for i, o := range u.Options {
			list = append(list, map[string]any{"ID": strconv.Itoa(i + 1), "SORT": strconv.Itoa((i + 1) * 10), "VALUE": o, "DEF": "N",
				"XML_ID": nil})
		}
		out["LIST"] = list
	}
	return out
}

// crm.*.userfield.list: filter by ID, FIELD_NAME, USER_TYPE_ID, MANDATORY — all of them at once.
func (s *Server) portalUFList(c portalCall, entity string) (portalOut, error) {
	sight, err := s.portalSight(c)
	if err != nil {
		return portalOut{}, err
	}
	ufs, err := s.portalUFs(c.r.Context(), entity, sight)
	if err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(c.in, "filter"))
	out := []map[string]any{}
	for _, u := range ufs {
		x := portalUFOut(entity, u)
		if !slices.ContainsFunc([]string{"ID", "FIELD_NAME", "USER_TYPE_ID", "MANDATORY"}, func(k string) bool {
			v, ok := portalParamOK(filter, k)
			return ok && !strings.EqualFold(portalStr(v), portalStr(x[k]))
		}) {
			out = append(out, x)
		}
	}
	return portalPaged(out, 0, len(out), len(out)), nil
}

// portalUFOf is the user field numbered id (a parameter).
func (s *Server) portalUFOf(c portalCall, entity string) (portalUF, error) {
	n, err := strconv.ParseInt(portalStr(portalParam(c.in, "id")), 10, 64)
	if err != nil {
		return portalUF{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_uf_id")
	}
	sight, err := s.portalSight(c)
	if err != nil {
		return portalUF{}, err
	}
	ufs, err := s.portalUFs(c.r.Context(), entity, sight)
	if err != nil {
		return portalUF{}, err
	}
	i := slices.IndexFunc(ufs, func(u portalUF) bool { return u.Num == n })
	if i < 0 {
		return portalUF{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return ufs[i], nil
}

// crm.*.userfield.get: id.
func (s *Server) portalUFGet(c portalCall, entity string) (portalOut, error) {
	u, err := s.portalUFOf(c, entity)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalUFOut(entity, u)}, nil
}

// portalUFLabel is a label of userfield.add (a text or texts by language: the caller's, English, Russian, any).
func (s *Server) portalUFLabel(c portalCall, fields map[string]any) string {
	for _, k := range []string{"EDIT_FORM_LABEL", "LIST_COLUMN_LABEL", "LIST_FILTER_LABEL", "LABEL"} {
		v, ok := portalParamOK(fields, k)
		if !ok {
			continue
		}
		if m := portalMap(v); m != nil {
			lang := strings.ToLower(s.I18n.Match(c.r.Header.Get("Accept-Language")))
			for _, l := range []string{lang[:min(2, len(lang))], "en", "ru"} {
				if x := strings.TrimSpace(portalStr(m[l])); x != "" {
					return x
				}
			}
			for _, x := range m {
				if x := strings.TrimSpace(portalStr(x)); x != "" {
					return x
				}
			}
		} else if x := strings.TrimSpace(portalStr(v)); x != "" {
			return x
		}
	}
	return ""
}

// crm.*.userfield.add: fields (FIELD_NAME — the code with UF_CRM_ (UF_USR_) or without, USER_TYPE_ID, EDIT_FORM_LABEL, LIST,
// MANDATORY, SORT) → the number of the field. Several values (MULTIPLE=Y) go24 does not keep.
func (s *Server) portalUFAdd(c portalCall, entity string) (portalOut, error) {
	ctx := c.r.Context()
	if !s.apiAdmin(ctx, c.p.UserID) {
		return portalOut{}, i18n.E("err.forbidden")
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	code := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(portalStr(portalParam(fields, "FIELD_NAME")))), portalUFPrefix(entity))
	// a code of digits only would be taken for the number of a field added in go24:, it is long
	if code != "" && (!portalUFCode.MatchString(code) || strings.Trim(code, "0123456789") == "" && len(code) < 7) {
		return portalOut{}, i18n.E("err.portal_uf_code", code)
	}
	if entity == "task" && strings.HasPrefix(code, "AUTO_") { // the names of the fields added in go24
		return portalOut{}, i18n.E("err.portal_uf_code", code)
	}
	typ := strings.ToLower(portalStr(portalParam(fields, "USER_TYPE_ID")))
	kind, ok := portalUFKinds[typ]
	if !ok {
		return portalOut{}, i18n.E("err.portal_uf_type", typ)
	}
	if strings.EqualFold(portalStr(portalParam(fields, "MULTIPLE")), "Y") {
		return portalOut{}, i18n.E("err.portal_uf_multiple")
	}
	f := tasks.Field{Name: s.portalUFLabel(c, fields), Kind: kind, Required: strings.EqualFold(portalStr(portalParam(fields, "MANDATORY")), "Y"), Sort: 100}
	if f.Name == "" {
		f.Name = code
	}
	if n, err := strconv.Atoi(portalStr(portalParam(fields, "SORT"))); err == nil {
		f.Sort = n
	}
	for _, it := range portalItems(portalParam(fields, "LIST")) {
		f.Options = append(f.Options, portalStr(portalParam(it, "VALUE")))
	}
	set, _ := portalUFSet(entity)
	if code != "" {
		var taken bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+set.Defs()+` WHERE code = $1)`, code).Scan(&taken); err != nil {
			return portalOut{}, err
		}
		if taken {
			return portalOut{}, i18n.E("err.portal_uf_code_taken", code)
		}
	}
	if err := s.Tasks.AddFieldTo(ctx, set, f); err != nil {
		return portalOut{}, err
	}
	var num int64
	err := s.DB.QueryRow(ctx, `UPDATE `+set.Defs()+` SET code = nullif($2, '') WHERE lower(name) = lower($1) RETURNING num`,
		strings.TrimSpace(f.Name), code).Scan(&num)
	return portalOut{Result: num}, err
}

// crm.*.userfield.update: id, fields — EDIT_FORM_LABEL (the name), SORT, MANDATORY, LIST of a list ([{ID, VALUE}]
// renames the option with its values, {ID, DEL: Y} removes it with them, {VALUE} adds one). The code and the type
// stay.
func (s *Server) portalUFUpdate(c portalCall, entity string) (portalOut, error) {
	ctx := c.r.Context()
	if !s.apiAdmin(ctx, c.p.UserID) {
		return portalOut{}, i18n.E("err.forbidden")
	}
	u, err := s.portalUFOf(c, entity)
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	set, _ := portalUFSet(entity)
	defs, values := set.Defs(), set.Values()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return portalOut{}, err
	}
	defer tx.Rollback(ctx)
	if name := s.portalUFLabel(c, fields); name != "" && name != u.Name {
		if len([]rune(name)) > 100 {
			return portalOut{}, i18n.E("err.field")
		}
		_, err := tx.Exec(ctx, `UPDATE `+defs+` SET name = $2 WHERE id = $1`, u.ID, name)
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return portalOut{}, i18n.E("err.field_exists")
		} else if err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(fields, "SORT"); ok {
		n, err := strconv.Atoi(portalStr(v))
		if err != nil {
			return portalOut{}, i18n.E("err.bad_number", portalStr(v))
		}
		if _, err := tx.Exec(ctx, `UPDATE `+defs+` SET sort = $2 WHERE id = $1`, u.ID, n); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(fields, "MANDATORY"); ok {
		req := strings.EqualFold(portalStr(v), "Y")
		if _, err := tx.Exec(ctx, `UPDATE `+defs+` SET required = $2, required_stages = CASE WHEN $2 THEN required_stages ELSE '{}' END
			WHERE id = $1`, u.ID, req); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(fields, "LIST"); ok && u.Kind == "list" {
		opts := slices.Clone(u.Options)
		gone := map[int]bool{}
		for _, it := range portalItems(v) {
			val := strings.TrimSpace(portalStr(portalParam(it, "VALUE")))
			n, err := strconv.Atoi(portalStr(portalParam(it, "ID")))
			switch {
			case err != nil || n < 1 || n > len(u.Options): // a new one
				if val != "" && !slices.Contains(opts, val) {
					opts = append(opts, val)
				}
			case strings.EqualFold(portalStr(portalParam(it, "DEL")), "Y"):
				gone[n-1] = true
			case val != "" && val != opts[n-1] && !slices.Contains(opts, val):
				if _, err := tx.Exec(ctx, `UPDATE `+values+` SET value = $3 WHERE field_id = $1 AND value = $2`, u.ID, opts[n-1], val); err != nil {
					return portalOut{}, err
				}
				opts[n-1] = val
			}
		}
		var keep, drop []string
		for i, o := range opts {
			if gone[i] {
				drop = append(drop, o)
			} else {
				keep = append(keep, o)
			}
		}
		if len(keep) == 0 {
			return portalOut{}, i18n.E("err.field")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+values+` WHERE field_id = $1 AND value = ANY($2)`, u.ID, drop); err != nil {
			return portalOut{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE `+defs+` SET options = $2 WHERE id = $1`, u.ID, keep); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: true}, tx.Commit(ctx)
}

// crm.*.userfield.delete: id — the field with its values on every card.
func (s *Server) portalUFDelete(c portalCall, entity string) (portalOut, error) {
	if !s.apiAdmin(c.r.Context(), c.p.UserID) {
		return portalOut{}, i18n.E("err.forbidden")
	}
	u, err := s.portalUFOf(c, entity)
	if err != nil {
		return portalOut{}, err
	}
	set, _ := portalUFSet(entity)
	if err := s.Tasks.DeleteFieldOf(c.r.Context(), set, u.ID); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// crm.userfield.types: the types userfield.add takes.
func (s *Server) portalUFTypesList(c portalCall) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := []map[string]any{}
	for _, t := range []string{"string", "integer", "double", "boolean", "date", "datetime", "enumeration", "url"} {
		out = append(out, map[string]any{"ID": t, "title": s.I18n.T(lang, "portal.uf.type."+t)})
	}
	return portalOut{Result: out}, nil
}

// portalUFDescribe are the fields of a user field (of an option of a list) as crm.userfield.fields gives them.
func (s *Server) portalUFDescribe(c portalCall, names ...string) (portalOut, error) {
	lang := s.I18n.Match(c.r.Header.Get("Accept-Language"))
	out := map[string]any{}
	for _, n := range names {
		typ := map[string]string{"ID": "int", "SORT": "int", "SETTINGS": "object", "LIST": "enumeration", "MULTIPLE": "char",
			"MANDATORY": "char", "SHOW_IN_LIST": "char", "EDIT_IN_LIST": "char", "IS_SEARCHABLE": "char", "DEF": "char"}[n]
		if typ == "" {
			typ = "string"
		}
		out[n] = map[string]any{"type": typ, "title": s.I18n.T(lang, "portal.uf."+n)}
	}
	return portalOut{Result: out}, nil
}
