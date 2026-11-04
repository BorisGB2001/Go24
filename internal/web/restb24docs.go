package web

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
)

// REST compatible with the portal, the document generator (GO24DEV-980): crm.documentgenerator.template.* (the CRM's
// templates; their changes — by the right «templates»), crm.documentgenerator.document.* (the documents of a card:
// those who see the card and have the right «view» see them, those who may change it and have «modify» make and change
// them) and crm.documentgenerator.numerator.* («Нумераторы», the right «settings»); the rights — docgen roles. A card is entityTypeId and entityId, as in crm.item.*; templates, documents and
// numerators have numbers (migration 955). The answers are wrapped: {"template": …}, {"documents": […]}.

const portalNumeratorGen = "Legacy_Main_Numerator_Generator_SequentNumberGenerator"

func init() {
	for name, m := range map[string]portalMethod{
		"crm.documentgenerator.template.list":            {Scope: "documents:read", Run: (*Server).portalDocTemplateList},
		"crm.documentgenerator.template.get":             {Scope: "documents:read", Run: (*Server).portalDocTemplateGet},
		"crm.documentgenerator.template.getfields":       {Scope: "documents:read", Run: (*Server).portalDocTemplateFields},
		"crm.documentgenerator.template.add":             {Scope: "documents:write", Run: (*Server).portalDocTemplateAdd},
		"crm.documentgenerator.template.update":          {Scope: "documents:write", Run: (*Server).portalDocTemplateUpdate},
		"crm.documentgenerator.template.delete":          {Scope: "documents:write", Run: (*Server).portalDocTemplateDelete},
		"crm.documentgenerator.document.list":            {Scope: "documents:read", Run: (*Server).portalDocList},
		"crm.documentgenerator.document.get":             {Scope: "documents:read", Run: (*Server).portalDocGet},
		"crm.documentgenerator.document.getfields":       {Scope: "documents:read", Run: (*Server).portalDocFields},
		"crm.documentgenerator.document.add":             {Scope: "documents:write", Run: (*Server).portalDocAdd},
		"crm.documentgenerator.document.update":          {Scope: "documents:write", Run: (*Server).portalDocUpdate},
		"crm.documentgenerator.document.delete":          {Scope: "documents:write", Run: (*Server).portalDocDelete},
		"crm.documentgenerator.document.enablepublicurl": {Scope: "documents:write", Run: (*Server).portalDocPublic},
		"crm.documentgenerator.numerator.list":           {Scope: "documents:read", Run: (*Server).portalNumeratorList},
		"crm.documentgenerator.numerator.get":            {Scope: "documents:read", Run: (*Server).portalNumeratorGet},
		"crm.documentgenerator.numerator.add":            {Scope: "documents:write", Run: (*Server).portalNumeratorAdd},
		"crm.documentgenerator.numerator.update":         {Scope: "documents:write", Run: (*Server).portalNumeratorUpdate},
		"crm.documentgenerator.numerator.delete":         {Scope: "documents:write", Run: (*Server).portalNumeratorDelete},
	} {
		portalMethods[name] = m
	}
}

func portalISO(t time.Time) string { return t.In(time.Local).Format(time.RFC3339) }

// portalDocNum is the go24 id of a template, a document or a numerator by its number (a go24 uuid is taken too).
func (s *Server) portalDocNum(c portalCall, table string, v any) (string, error) {
	x := strings.TrimSpace(portalStr(v))
	var id string
	err := s.DB.QueryRow(c.r.Context(), `SELECT id::text FROM `+table+` WHERE num::text = $1 OR id::text = $1`, x).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || x == "" {
		return "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return id, err
}

// portalRowNum is the number of a row of the table.
func (s *Server) portalRowNum(ctx context.Context, table, id string) int64 {
	var n int64
	_ = s.DB.QueryRow(ctx, `SELECT num FROM `+table+` WHERE id::text = $1`, id).Scan(&n)
	return n
}

// portalFiltered: the row (in camelCase) passes the filter of a list, the names in any case.
func portalFiltered(row map[string]any, filter map[string]any) bool {
	up := make(map[string]any, len(row))
	for k, v := range row {
		up[strings.ToUpper(k)] = v
	}
	return portalRowMatch(up, filter)
}

// portalListPage cuts the page of «start» out of the rows, under the key the portal gives the list.
func portalListPage(c portalCall, key string, rows []map[string]any) portalOut {
	start := portalStart(c.in)
	page := rows[min(start, len(rows)):min(start+portalPage, len(rows))]
	return portalPaged(map[string]any{key: page}, start, len(page), len(rows))
}

// ---------- the card ----------

// portalDocCard is the card of entityTypeId (1–4, a smart process's from 128) and entityId, for the action with it: the
// kind of its pages (deals…) and its id.
func (s *Server) portalDocCard(c portalCall, typ, num any, action string) (kind, id string, err error) {
	n, err := strconv.ParseInt(strings.TrimSpace(portalStr(typ)), 10, 64)
	if err != nil {
		return "", "", s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_entity_type", portalStr(typ))
	}
	e, err := s.portalItemEntity(c, n)
	if err != nil {
		return "", "", err
	}
	if id, err = s.portalCardID(c, e, num); err != nil {
		return "", "", err
	}
	if _, err = s.apiCRMItem(cardReq(c.r, e.Kind, id), c.p, action); err != nil {
		return "", "", err
	}
	return e.Kind, id, nil
}

// portalCardNums are entityTypeId and entityId of a card of go24 (a deal of a smart process's pipeline — of its type).
func (s *Server) portalCardNums(ctx context.Context, entity, id string) (typ, num int64) {
	for _, o := range portalOwners {
		if o.Entity == entity {
			typ = int64(o.ID)
		}
	}
	table := map[string]string{"deal": "crm_deals", "lead": "crm_leads", "contact": "crm_contacts", "company": "crm_companies"}[entity]
	if table == "" {
		return 0, 0
	}
	num = s.portalRowNum(ctx, table, id)
	if entity == "deal" {
		var t int64
		if s.DB.QueryRow(ctx, `SELECT ct.num FROM crm_deals x JOIN crm_pipelines p ON p.id = x.pipeline_id JOIN crm_types ct ON ct.id = p.type_id
			WHERE x.id::text = $1`, id).Scan(&t) == nil {
			typ = t
		}
	}
	return typ, num
}

// ---------- templates ----------

// portalTypeNums: a smart process's id → its entityTypeId.
func (s *Server) portalTypeNums(ctx context.Context) (map[string]int64, error) {
	rows, err := s.DB.Query(ctx, `SELECT id::text, num FROM crm_types`)
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

// portalTemplateType is the entityTypeId of the template's cards; 0 — not of cards (HR documents, invoices).
func portalTemplateType(t crm.DocTemplate, types map[string]int64) int64 {
	if t.TypeID != "" {
		return types[t.TypeID]
	}
	for _, o := range portalOwners {
		if o.Entity == t.Entity {
			return int64(o.ID)
		}
	}
	return 0
}

// portalAccess are the access codes of «Кому доступен»: UA — all, U<number> — an employee, DR<number> — a department
// with its sub-departments.
func (s *Server) portalAccess(ctx context.Context, refs []crm.Ref) []string {
	if len(refs) == 0 {
		return []string{"UA"}
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if d, ok := strings.CutPrefix(ref.ID, "dept:"); ok {
			out = append(out, "DR"+strconv.FormatInt(s.portalRowNum(ctx, "departments", d), 10))
		} else {
			out = append(out, "U"+strconv.FormatInt(s.portalRowNum(ctx, "users", ref.ID), 10))
		}
	}
	return out
}

// portalAccessIn is «Кому доступен» of the codes (UA, U1, D2, DR2): the ids SetDocTemplateAccess takes; none — all.
func (s *Server) portalAccessIn(c portalCall, codes []string) ([]string, error) {
	var out []string
	for _, code := range codes {
		code = strings.ToUpper(strings.TrimSpace(code))
		table, n := "users", ""
		switch {
		case code == "UA" || code == "AU" || code == "G2":
			return []string{}, nil
		case strings.HasPrefix(code, "DR"):
			table, n = "departments", code[2:]
		case strings.HasPrefix(code, "D"):
			table, n = "departments", code[1:]
		case strings.HasPrefix(code, "U"):
			n = code[1:]
		default:
			return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "users")
		}
		id, err := s.portalNum(c.r.Context(), table, n)
		if err != nil || id == "" {
			return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "users")
		}
		if table == "departments" {
			id = "dept:" + id
		}
		out = append(out, id)
	}
	return out, nil
}

// portalTemplate is a template gives it; download — its DOCX by a signed link.
func (s *Server) portalTemplate(c portalCall, t crm.DocTemplate, typ int64, access []crm.Ref) map[string]any {
	ctx := c.r.Context()
	link := s.portalDocLink(c, "t", t.ID)
	return map[string]any{"id": s.portalRowNum(ctx, "crm_doc_templates", t.ID), "name": t.Name, "code": "", "region": "ru",
		"active": portalYN(t.Active), "withStamps": portalYN(t.WithStamps), "isDeleted": "N", "isDefault": "N", "moduleId": "crm",
		"sort": t.Sort, "fileName": t.FileName, "numeratorId": s.portalRowNum(ctx, "doc_numerators", t.NumeratorID),
		"entityTypeId": []string{strconv.FormatInt(typ, 10)}, "users": s.portalAccess(ctx, access), "createTime": portalISO(t.CreatedAt),
		"updateTime": portalISO(t.Changed()), "createdBy": s.portalRowNum(ctx, "users", t.CreatedBy),
		"updatedBy": s.portalRowNum(ctx, "users", cmp.Or(t.UpdatedBy, t.CreatedBy)),
		"download": link, "downloadMachine": link}
}

// crm.documentgenerator.template.list: the templates of cards the caller may make documents by; filter by id, name,
// entityTypeId, numeratorId; start → {"templates": […]}.
func (s *Server) portalDocTemplateList(c portalCall) (portalOut, error) {
	list, err := s.docTemplatesSeen(c.r, s.apiSess(c.r, c.p), "", "")
	if err != nil {
		return portalOut{}, err
	}
	types, err := s.portalTypeNums(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	access, err := s.CRM.DocTemplateAccess(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(c.in, "filter"))
	rows := []map[string]any{}
	for _, t := range list {
		typ := portalTemplateType(t, types)
		if typ == 0 {
			continue
		}
		row := s.portalTemplate(c, t, typ, access[t.ID])
		match := maps.Clone(row)
		match["entityTypeId"] = typ
		if portalFiltered(match, filter) {
			rows = append(rows, row)
		}
	}
	slices.SortStableFunc(rows, func(a, b map[string]any) int { return cmp.Compare(a["id"].(int64), b["id"].(int64)) })
	return portalListPage(c, "templates", rows), nil
}

// portalDocTemplateOf is the template of «id» the caller may use, of cards.
func (s *Server) portalDocTemplateOf(c portalCall, v any) (crm.DocTemplate, int64, error) {
	id, err := s.portalDocNum(c, "crm_doc_templates", v)
	if err != nil {
		return crm.DocTemplate{}, 0, err
	}
	if err := s.docTemplateAllowed(c.r, s.apiSess(c.r, c.p), id); err != nil {
		return crm.DocTemplate{}, 0, err
	}
	t, err := s.CRM.DocTemplate(c.r.Context(), id)
	if err != nil {
		return t, 0, err
	}
	types, err := s.portalTypeNums(c.r.Context())
	if err != nil {
		return t, 0, err
	}
	typ := portalTemplateType(t, types)
	if typ == 0 {
		return t, 0, crm.ErrNotFound
	}
	return t, typ, nil
}

func (s *Server) portalTemplateOut(c portalCall, t crm.DocTemplate, typ int64) (portalOut, error) {
	access, err := s.CRM.DocTemplateAccess(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"template": s.portalTemplate(c, t, typ, access[t.ID])}}, nil
}

// crm.documentgenerator.template.get: id → {"template": …}.
func (s *Server) portalDocTemplateGet(c portalCall) (portalOut, error) {
	t, typ, err := s.portalDocTemplateOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalTemplateOut(c, t, typ)
}

// portalDocFieldsOut are placeholders with their values, as templateFields and documentFields; the settings of
// the template's fields give their titles, values by default, required and hideRow.
func portalDocFieldsOut(names []string, values map[string]string, set map[string]crm.DocField) map[string]any {
	out := map[string]any{}
	for _, n := range names {
		f := set[n]
		title := f.Title
		if title == "" {
			title = n
		}
		v := values[n]
		if v == "" {
			v = f.Value
		}
		out[n] = map[string]any{"title": title, "value": v, "default": f.Value, "required": portalYN(f.Required),
			"hideRow": portalYN(f.HideRow), "group": []string{}, "type": "STRING"}
	}
	return out
}

// crm.documentgenerator.template.getfields: id, and the card (entityTypeId, entityId) to take the values of, values
// over them → {"templateFields": {name: {title, value}}}.
func (s *Server) portalDocTemplateFields(c portalCall) (portalOut, error) {
	t, _, err := s.portalDocTemplateOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	values := map[string]string{}
	if portalStr(portalParam(c.in, "entityId")) != "" {
		kind, card, err := s.portalDocCard(c, portalParam(c.in, "entityTypeId"), portalParam(c.in, "entityId"), "read")
		if err != nil {
			return portalOut{}, err
		}
		if values, _, _, err = s.cardValues(c.r, kind, card); err != nil {
			return portalOut{}, err
		}
	}
	maps.Copy(values, portalDocValues(c.in))
	_, tpl, err := s.docTemplateOf(c.r, t.Kind(), t.ID)
	if err != nil {
		return portalOut{}, err
	}
	names, err := docgen.Names(tpl)
	if err != nil {
		return portalOut{}, i18n.E("err.doc_not_docx")
	}
	set, err := s.CRM.DocTemplateFields(c.r.Context(), t.ID)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"templateFields": portalDocFieldsOut(names, values, set)}}, nil
}

// portalDocRight: the caller has one of the rights of the generator (templates, settings, view, modify).
func (s *Server) portalDocRight(c portalCall, rights ...string) (auth.Session, error) {
	sess := s.apiSess(c.r, c.p)
	perms := s.docPerms(c.r.Context(), sess)
	for _, r := range rights {
		if perms.Can(r) {
			return sess, nil
		}
	}
	return sess, s.portalErr(c.r, http.StatusForbidden, "ACCESS_DENIED", "err.forbidden")
}

// portalDocFile is the DOCX of fields[file]: base64, [name, base64] or {fileData: [name, base64]}; ok false — none or
// not base64.
func portalDocFile(v any) (name string, data []byte, ok bool) {
	if m := portalMap(v); m != nil {
		if fd, has := portalParamOK(m, "fileData"); has {
			v = fd
		} else {
			v = []any{m["0"], m["1"]}
		}
	}
	b64 := portalStr(v)
	if l, isList := v.([]any); isList {
		b64 = ""
		if len(l) > 1 {
			name, b64 = portalStr(l[0]), portalStr(l[1])
		}
	}
	if _, rest, cut := strings.Cut(b64, ";base64,"); cut { // a data: address
		b64 = rest
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	return name, data, err == nil && len(data) > 0
}

// crm.documentgenerator.template.add: fields[name], fields[file] (the DOCX), fields[entityTypeId] (one kind of cards),
// fields[numeratorId], fields[users] → {"template": …} (the right «templates»).
func (s *Server) portalDocTemplateAdd(c portalCall) (portalOut, error) {
	sess, err := s.portalDocRight(c, "templates")
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	name, data, ok := portalDocFile(portalParam(fields, "file"))
	if !ok {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "file")
	}
	types := portalList(portalParam(fields, "entityTypeId"))
	if len(types) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "entityTypeId")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(types[0]), 10, 64)
	if err != nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_entity_type", types[0])
	}
	e, err := s.portalItemEntity(c, n)
	if err != nil {
		return portalOut{}, err
	}
	if name == "" || !strings.HasSuffix(strings.ToLower(name), ".docx") {
		name = portalStr(portalParam(fields, "name")) + ".docx"
	}
	id, err := s.addTemplate(c.r.Context(), sess.UserID, portalStr(portalParam(fields, "name")), crm.DocKind(e.Entity, e.Type), name, data)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalDocTemplateSet(c, id, fields); err != nil {
		_ = s.CRM.DeleteDocTemplate(c.r.Context(), id)
		return portalOut{}, err
	}
	t, typ, err := s.portalDocTemplateOf(c, id)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalTemplateOut(c, t, typ)
}

// portalDocTemplateSet changes what fields give of the template: numeratorId, users, active, sort, withStamps (and name,
// file of update); it notes who changed it.
func (s *Server) portalDocTemplateSet(c portalCall, id string, fields map[string]any) error {
	ctx := c.r.Context()
	_, a := portalParamOK(fields, "active")
	_, o := portalParamOK(fields, "sort")
	_, w := portalParamOK(fields, "withStamps")
	if a || o || w {
		t, err := s.CRM.DocTemplate(ctx, id)
		if err != nil {
			return err
		}
		set := crm.DocTemplateSettings{Active: portalYes(fields, "active", t.Active), Sort: t.Sort,
			WithStamps: portalYes(fields, "withStamps", t.WithStamps)}
		if v := strings.TrimSpace(portalStr(portalParam(fields, "sort"))); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "sort")
			}
			set.Sort = n
		}
		if err := s.CRM.SetDocTemplateSettings(ctx, id, c.p.UserID, set); err != nil {
			return err
		}
	}
	if v := portalParam(fields, "numeratorId"); portalStr(v) != "" {
		num, err := s.portalDocNum(c, "doc_numerators", v)
		if err == nil {
			err = s.CRM.SetDocTemplateNumerator(ctx, id, num)
		}
		if err != nil {
			return err
		}
	}
	if v, ok := portalParamOK(fields, "users"); ok {
		to, err := s.portalAccessIn(c, portalList(v))
		if err == nil {
			err = s.CRM.SetDocTemplateAccess(ctx, id, to)
		}
		if err != nil {
			return err
		}
	}
	return s.CRM.DocTemplateChanged(ctx, id, c.p.UserID)
}

// crm.documentgenerator.template.update: id, fields[name], fields[file], fields[numeratorId], fields[users] →
// {"template": …} (the right «templates»); the kind of cards stays.
func (s *Server) portalDocTemplateUpdate(c portalCall) (portalOut, error) {
	sess, err := s.portalDocRight(c, "templates")
	if err != nil {
		return portalOut{}, err
	}
	t, _, err := s.portalDocTemplateOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	ctx := c.r.Context()
	if v, ok := portalParamOK(fields, "name"); ok {
		if err := s.CRM.RenameDocTemplate(ctx, t.ID, portalStr(v)); err != nil {
			return portalOut{}, err
		}
	}
	if v, ok := portalParamOK(fields, "file"); ok {
		_, data, ok := portalDocFile(v)
		if !ok || len(data) > maxTemplateSize {
			return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", "file")
		}
		if _, err := docgen.Fill(data, nil); err != nil {
			return portalOut{}, i18n.E("err.doc_not_docx")
		}
		fileID, err := s.storeTemplateFile(ctx, sess.UserID, t.FileName, data)
		if err == nil {
			err = s.CRM.SetDocTemplateFile(ctx, t.ID, fileID)
		}
		if err != nil {
			return portalOut{}, err
		}
	}
	if err := s.portalDocTemplateSet(c, t.ID, fields); err != nil {
		return portalOut{}, err
	}
	t, typ, err := s.portalDocTemplateOf(c, t.ID)
	if err != nil {
		return portalOut{}, err
	}
	return s.portalTemplateOut(c, t, typ)
}

// crm.documentgenerator.template.delete: id → null (the right «templates»); its documents stay.
func (s *Server) portalDocTemplateDelete(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "templates"); err != nil {
		return portalOut{}, err
	}
	t, _, err := s.portalDocTemplateOf(c, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{}, s.CRM.DeleteDocTemplate(c.r.Context(), t.ID)
}

// ---------- documents ----------

// portalDocValues are «values» of the call: the placeholders' values over the card's.
func portalDocValues(in map[string]any) map[string]string {
	out := map[string]string{}
	for k, v := range portalMap(portalParam(in, "values")) {
		out[k] = portalStr(v)
	}
	return out
}

// portalDoc is a document gives it: downloadUrl (DOCX) and pdfUrl — signed links.
func (s *Server) portalDoc(c portalCall, d crm.Document, values bool) map[string]any {
	ctx := c.r.Context()
	typ, num := s.portalCardNums(ctx, d.Entity, d.EntityID)
	out := map[string]any{"id": s.portalRowNum(ctx, "crm_documents", d.ID), "title": strings.TrimSuffix(d.FileName, ".docx"),
		"number": d.Number, "templateId": nil, "entityTypeId": typ, "entityId": num, "provider": "",
		"createTime": portalISO(d.CreatedAt), "updateTime": portalISO(d.UpdatedAt), "createdBy": nil,
		"stampsEnabled": d.Stamps, "isTransformationError": false, "downloadUrl": s.portalDocLink(c, "d", d.ID),
		"pdfUrl": s.portalDocLink(c, "p", d.ID), "imageUrl": nil, "publicUrl": nil}
	if d.TemplateID != "" {
		out["templateId"] = s.portalRowNum(ctx, "crm_doc_templates", d.TemplateID)
	}
	if u := s.portalRowNum(ctx, "users", d.AuthorID); u > 0 {
		out["createdBy"] = u
	}
	if d.PublicToken != "" {
		out["publicUrl"] = s.BaseURL + "/d/" + d.PublicToken
	}
	if values {
		out["values"] = d.Values
	}
	return out
}

// portalDocOf is the document of «id» for the action with its card (read, write) by the right of the generator (view,
// modify).
func (s *Server) portalDocOf(c portalCall, action string) (crm.Document, error) {
	if _, err := s.portalDocRight(c, map[string]string{"read": "view", "write": "modify"}[action]); err != nil {
		return crm.Document{}, err
	}
	id, err := s.portalDocNum(c, "crm_documents", portalParam(c.in, "id"))
	if err != nil {
		return crm.Document{}, err
	}
	d, err := s.CRM.DocumentByID(c.r.Context(), id)
	if err == nil && !crm.Entities[d.Entity] {
		err = crm.ErrNotFound // an invoice's print form
	}
	if err == nil {
		err = s.crmCheck(c.r, s.apiSess(c.r, c.p), crmKind(d.Entity), d.EntityID, action)
	}
	return d, err
}

func (s *Server) portalDocOut(c portalCall, id string) (portalOut, error) {
	d, err := s.CRM.DocumentByID(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"document": s.portalDoc(c, d, true)}}, nil
}

// crm.documentgenerator.document.list: filter[entityTypeId] and filter[entityId] — the card (required), and by
// templateId, number, title; start → {"documents": […]}, the newest first.
func (s *Server) portalDocList(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "view"); err != nil {
		return portalOut{}, err
	}
	filter := portalMap(portalParam(c.in, "filter"))
	kind, card, err := s.portalDocCard(c, portalParam(filter, "entityTypeId"), portalParam(filter, "entityId"), "read")
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.CRM.Documents(c.r.Context(), docEntity(kind), card)
	if err != nil {
		return portalOut{}, err
	}
	rest := maps.Clone(filter)
	for k := range rest {
		if f := strings.TrimLeft(k, "!=<>%"); strings.EqualFold(f, "entityTypeId") || strings.EqualFold(f, "entityId") {
			delete(rest, k)
		}
	}
	rows := []map[string]any{}
	for _, d := range list {
		if row := s.portalDoc(c, d, false); portalFiltered(row, rest) {
			rows = append(rows, row)
		}
	}
	return portalListPage(c, "documents", rows), nil
}

// crm.documentgenerator.document.get: id → {"document": …} with its values.
func (s *Server) portalDocGet(c portalCall) (portalOut, error) {
	d, err := s.portalDocOf(c, "read")
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"document": s.portalDoc(c, d, true)}}, nil
}

// crm.documentgenerator.document.getfields: id, values → {"documentFields": {name: {title, value}}} — the
// placeholders of its template with the values it was made with.
func (s *Server) portalDocFields(c portalCall) (portalOut, error) {
	d, err := s.portalDocOf(c, "read")
	if err != nil {
		return portalOut{}, err
	}
	values := maps.Clone(d.Values)
	if values == nil {
		values = map[string]string{}
	}
	maps.Copy(values, portalDocValues(c.in))
	names := slices.Sorted(maps.Keys(values))
	if d.TemplateID != "" {
		if _, tpl, err := s.docTemplateFile(c.r, crmKind(d.Entity), d.EntityID, d.TemplateID); err == nil {
			if n, err := docgen.Names(tpl); err == nil {
				names = n
			}
		}
	}
	var set map[string]crm.DocField
	if d.TemplateID != "" {
		if set, err = s.CRM.DocTemplateFields(c.r.Context(), d.TemplateID); err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: map[string]any{"documentFields": portalDocFieldsOut(names, values, set)}}, nil
}

// crm.documentgenerator.document.add: templateId, entityTypeId, entityId, values, stampsEnabled → {"document": …}.
func (s *Server) portalDocAdd(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "modify"); err != nil {
		return portalOut{}, err
	}
	kind, card, err := s.portalDocCard(c, portalParam(c.in, "entityTypeId"), portalParam(c.in, "entityId"), "read")
	if err != nil {
		return portalOut{}, err
	}
	tpl, err := s.portalDocNum(c, "crm_doc_templates", portalParam(c.in, "templateId"))
	if err != nil {
		return portalOut{}, err
	}
	d, err := s.makeDocument(c.r, s.apiSess(c.r, c.p), kind, card, tpl, portalDocValues(c.in), portalStamps(c.in))
	if err != nil {
		return portalOut{}, err
	}
	return s.portalDocOut(c, d.ID)
}

// portalStamps is stampsEnabled of a new document; not given — as the template's withStamps.
func portalStamps(in map[string]any) *bool {
	if v, ok := portalParamOK(in, "stampsEnabled"); !ok || portalStr(v) == "" {
		return nil
	}
	on := portalYes(in, "stampsEnabled", true)
	return &on
}

// crm.documentgenerator.document.update: id, values, stampsEnabled → {"document": …} made again; the number stays.
func (s *Server) portalDocUpdate(c portalCall) (portalOut, error) {
	d, err := s.portalDocOf(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	d.Stamps = portalYes(c.in, "stampsEnabled", d.Stamps)
	if err := s.remakeDocument(c.r, s.apiSess(c.r, c.p), d, portalDocValues(c.in)); err != nil {
		return portalOut{}, err
	}
	return s.portalDocOut(c, d.ID)
}

// crm.documentgenerator.document.delete: id → null.
func (s *Server) portalDocDelete(c portalCall) (portalOut, error) {
	d, err := s.portalDocOf(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{}, s.CRM.DeleteDocument(c.r.Context(), d.ID)
}

// crm.documentgenerator.document.enablepublicurl: id, status (1 — on, 0 — off) → {"publicUrl": … or null}.
func (s *Server) portalDocPublic(c portalCall) (portalOut, error) {
	d, err := s.portalDocOf(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	on := portalFlag(portalParam(c.in, "status"))
	token, err := s.CRM.SetDocumentPublic(c.r.Context(), d.ID, on)
	if err != nil {
		return portalOut{}, err
	}
	s.Log.Info("crm document link", "user_id", c.p.UserID, "doc", d.ID, "on", on, "api", true)
	var link any
	if on && token != "" {
		link = s.BaseURL + "/d/" + token
	}
	return portalOut{Result: map[string]any{"publicUrl": link}}, nil
}

// ---------- numerators ----------

// portalNumerator is a numerator gives it: the generator of sequent numbers with its settings.
func (s *Server) portalNumerator(ctx context.Context, n crm.Numerator) map[string]any {
	periodic := n.Reset
	if periodic == "never" {
		periodic = ""
	}
	return map[string]any{"id": s.portalRowNum(ctx, "doc_numerators", n.ID), "name": n.Name, "template": n.Format,
		"settings": map[string]any{portalNumeratorGen: map[string]any{"start": n.Start, "step": n.Step, "periodicBy": periodic,
			"timezone": "", "isDirectNumeration": false, "length": n.Pad}}}
}

// portalNumeratorOf is the numerator of «id» for those with one of the rights.
func (s *Server) portalNumeratorOf(c portalCall, rights ...string) (crm.Numerator, error) {
	if _, err := s.portalDocRight(c, rights...); err != nil {
		return crm.Numerator{}, err
	}
	id, err := s.portalDocNum(c, "doc_numerators", portalParam(c.in, "id"))
	if err != nil {
		return crm.Numerator{}, err
	}
	return s.CRM.Numerator(c.r.Context(), id)
}

// crm.documentgenerator.numerator.list: start → {"numerators": […]} (the rights «templates» or «settings»).
func (s *Server) portalNumeratorList(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "templates", "settings"); err != nil {
		return portalOut{}, err
	}
	list, err := s.CRM.Numerators(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	rows := make([]map[string]any, 0, len(list))
	for _, n := range list {
		rows = append(rows, s.portalNumerator(c.r.Context(), n))
	}
	slices.SortStableFunc(rows, func(a, b map[string]any) int { return cmp.Compare(a["id"].(int64), b["id"].(int64)) })
	return portalListPage(c, "numerators", rows), nil
}

func (s *Server) portalNumeratorGet(c portalCall) (portalOut, error) {
	n, err := s.portalNumeratorOf(c, "templates", "settings")
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"numerator": s.portalNumerator(c.r.Context(), n)}}, nil
}

// portalNumeratorSet puts fields[name], fields[template] and fields[settings][…SequentNumberGenerator][start, step,
// periodicBy, length] into the numerator and saves it → {"numerator": …}.
func (s *Server) portalNumeratorSet(c portalCall, n crm.Numerator) (portalOut, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	if v, ok := portalParamOK(fields, "name"); ok {
		n.Name = portalStr(v)
	}
	if v, ok := portalParamOK(fields, "template"); ok {
		n.Format = portalStr(v)
	}
	gen := portalMap(portalParam(portalMap(portalParam(fields, "settings")), portalNumeratorGen))
	num := func(name string, to *int64) error {
		v, ok := portalParamOK(gen, name)
		if !ok {
			return nil
		}
		x, err := strconv.ParseInt(strings.TrimSpace(portalStr(v)), 10, 64)
		if err != nil {
			return s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_field", name)
		}
		*to = x
		return nil
	}
	step, pad := int64(n.Step), int64(n.Pad)
	if err := errors.Join(num("start", &n.Start), num("step", &step), num("length", &pad)); err != nil {
		return portalOut{}, err
	}
	n.Step, n.Pad = int(step), int(pad)
	if v, ok := portalParamOK(gen, "periodicBy"); ok {
		n.Reset = strings.ToLower(portalStr(v))
		if n.Reset == "" || n.Reset == "default" {
			n.Reset = "never"
		}
	}
	id, err := s.CRM.SaveNumerator(c.r.Context(), n)
	if err == nil {
		n, err = s.CRM.Numerator(c.r.Context(), id)
	}
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"numerator": s.portalNumerator(c.r.Context(), n)}}, nil
}

// crm.documentgenerator.numerator.add: fields → {"numerator": …} (the right «settings»).
func (s *Server) portalNumeratorAdd(c portalCall) (portalOut, error) {
	if _, err := s.portalDocRight(c, "settings"); err != nil {
		return portalOut{}, err
	}
	return s.portalNumeratorSet(c, crm.Numerator{Format: "{NUMBER}", Start: 1, Step: 1, Reset: "never"})
}

// crm.documentgenerator.numerator.update: id, fields → {"numerator": …} (the right «settings»).
func (s *Server) portalNumeratorUpdate(c portalCall) (portalOut, error) {
	n, err := s.portalNumeratorOf(c, "settings")
	if err != nil {
		return portalOut{}, err
	}
	return s.portalNumeratorSet(c, n)
}

// crm.documentgenerator.numerator.delete: id → null; a numerator a template numbers by stays.
func (s *Server) portalNumeratorDelete(c portalCall) (portalOut, error) {
	n, err := s.portalNumeratorOf(c, "settings")
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{}, s.CRM.DeleteNumerator(c.r.Context(), n.ID)
}

// ---------- files ----------

// portalDocLink is a signed link to a file of the generator for the caller: a template's DOCX (which "t"), a document's
// DOCX ("d") or its PDF ("p").
func (s *Server) portalDocLink(c portalCall, which, id string) string {
	return s.portalDiskLink("docfile", portalDiskTicket{User: c.p.UserID, File: id, Which: which})
}

// portalDocFileGet: GET /rest/docfile?token= — the file of download, downloadUrl, pdfUrl, while the employee still may
// use the template or see the card.
func (s *Server) portalDocFileGet(w http.ResponseWriter, r *http.Request) {
	r, t, err := s.portalDiskOpen(r, "docfile")
	fileID := ""
	if err == nil {
		sess := auth.Session{UserID: t.User, Admin: s.apiAdmin(r.Context(), t.User)}
		switch t.Which {
		case "t":
			var tpl crm.DocTemplate
			if err = s.docTemplateAllowed(r, sess, t.File); err == nil {
				tpl, err = s.CRM.DocTemplate(r.Context(), t.File)
				fileID = tpl.FileID
			}
		default:
			var d crm.Document
			if d, err = s.CRM.DocumentByID(r.Context(), t.File); err == nil {
				if err = s.docRight(r.Context(), sess, "view"); err == nil {
					err = s.crmCheck(r, sess, crmKind(d.Entity), d.EntityID, "read")
				}
			}
			if fileID = d.FileID; err == nil && t.Which == "p" {
				fileID, err = s.documentPDF(r, d)
			}
		}
	}
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), fileID)
	if err != nil {
		s.portalFail(w, r, err)
		return
	}
	s.sendFile(w, r, m, "attachment")
}
