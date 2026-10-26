package web

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// ---------- CRM: /crm/deals, /crm/leads, /crm/contacts, /crm/companies and their cards ----------

// crmEntities: the URL's plural → the store's entity.
var crmEntities = map[string]string{"deals": "deal", "leads": "lead", "contacts": "contact", "companies": "company"}

func (s *Server) crmList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.PathValue("kind")
	if crmEntities[kind] == "" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	var typ *crm.Type
	var err error
	if kind == "deals" {
		typ, err = s.crmTypeOf(r, q.Get("pipeline"), q.Get("type"))
	}
	var presets []crm.Preset
	scope := crm.PresetScope(crmEntities[kind], typeID(typ))
	if err == nil && (kind == "deals" || kind == "leads") { // «Пресеты фильтра»: the list opens with the default one
		presets, err = s.CRM.Presets(r.Context(), sess.UserID, scope)
		for _, p := range presets {
			if p.Default && !crmFiltered(q) && q.Get("reset") != "1" {
				pq, _ := url.ParseQuery(p.Query)
				for k, v := range pq {
					q[k] = v
				}
			}
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	f := crmFilter(q, sess.UserID)
	data := map[string]any{"Kind": kind, "Q": q.Get("q"), "View": q.Get("view"), "F": f, "Query": crmQuery(q),
		"Presets": presets, "BuiltIn": crm.BuiltInPresets, "PresetQuery": crmPresetQuery(q), "PresetKind": kind, "Scope": scope}
	if typ != nil {
		data["PresetKind"] = "items"
	}
	nav := "crm_" + kind
	var ids []string // the listed cards
	rates, err := s.CRM.Rates(r.Context()) // the kanban sums are in the base currency
	if err == nil {
		data["Dicts"], err = s.CRM.Dicts(r.Context()) // the filter's sources, the lists' types
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	switch kind {
	case "deals": // deals, or the cards of a smart process
		var pipes []crm.Pipeline
		if typ != nil {
			data["Type"], nav = typ, "crm_type_"+typ.ID
		}
		pipes, err = s.CRM.PipelinesOf(r.Context(), typeID(typ))
		if err == nil && len(pipes) > 0 {
			cur := pipes[0]
			for _, p := range pipes {
				if p.ID == q.Get("pipeline") {
					cur = p
				}
			}
			var deals []crm.Deal
			if deals, err = s.CRM.DealList(r.Context(), sess.UserID, cur.ID, f); err == nil {
				data["Pipelines"], data["Pipeline"], data["Stages"], data["Columns"] = pipes, cur, cur.Stages, crmColumns(cur.Stages, deals, func(d crm.Deal) (string, float64) { return d.Stage.ID, crm.InBase(rates, d.Amount, d.Currency) })
				data["Items"], data["Sums"] = deals, crmSums(deals, func(d crm.Deal) (string, float64) { return d.ID, crm.InBase(rates, d.Amount, d.Currency) })
				if q.Get("view") != "list" {
					data["Clients"], err = s.crmClients(r, sess)
				}
				for _, d := range deals {
					ids = append(ids, d.ID)
				}
				if err == nil {
					data["Next"], err = s.CRM.NextActivities(r.Context(), "deal", ids)
				}
			}
		}
	case "leads":
		var stages []crm.Stage
		if stages, err = s.CRM.LeadStages(r.Context()); err == nil {
			var leads []crm.Lead
			if leads, err = s.CRM.LeadList(r.Context(), sess.UserID, f); err == nil {
				data["Stages"], data["Columns"] = stages, crmColumns(stages, leads, func(l crm.Lead) (string, float64) { return l.Stage.ID, crm.InBase(rates, l.Amount, l.Currency) })
				data["Items"], data["Sums"] = leads, crmSums(leads, func(l crm.Lead) (string, float64) { return l.ID, crm.InBase(rates, l.Amount, l.Currency) })
				for _, l := range leads {
					ids = append(ids, l.ID)
				}
				data["Next"], err = s.CRM.NextActivities(r.Context(), "lead", ids)
			}
		}
	case "contacts":
		var list []crm.Contact
		if list, err = s.CRM.Contacts(r.Context(), sess.UserID, q.Get("q"), ""); err == nil {
			data["Items"] = list
			for _, c := range list {
				ids = append(ids, c.ID)
			}
		}
	case "companies":
		var list []crm.Company
		if list, err = s.CRM.Companies(r.Context(), sess.UserID, q.Get("q")); err == nil {
			data["Items"] = list
			for _, c := range list {
				ids = append(ids, c.ID)
			}
		}
	}
	if err == nil && (q.Get("view") == "list" || kind == "contacts" || kind == "companies") {
		err = s.crmListCols(r, sess, data, crm.ColumnScope(crmEntities[kind], typeID(typ)), ids)
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	data["Users"], data["Me"], data["Colors"] = users, sess.UserID, tasks.StageColors
	data["NewFields"], err = s.crmNewFields(r, crmEntities[kind], data["Stages"])
	if err == nil {
		err = s.crmTabs(r, sess, data)
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	pg := page{TitleKey: "crm." + kind, Nav: nav, Data: data}
	if t, ok := data["Type"].(*crm.Type); ok {
		pg.TitleKey, pg.Title = "", t.Name
	}
	s.render(w, r, "crm", pg)
}

// crmNewFields are the own fields a new card must have filled: required always or at its first stage.
func (s *Server) crmNewFields(r *http.Request, entity string, stages any) ([]tasks.Field, error) {
	list, err := s.Tasks.FieldsOf(r.Context(), tasks.CRMFieldSets[entity])
	if err != nil {
		return nil, err
	}
	first := ""
	if st, ok := stages.([]crm.Stage); ok && len(st) > 0 {
		first = st[0].ID
	}
	var out []tasks.Field
	for _, f := range list {
		if f.RequiredAt(first) {
			out = append(out, f)
		}
	}
	return out, nil
}

// crmCol is a column of a CRM list: a built-in one (its name is "crm.f."+Key) or a custom field ("f:"+its id).
type crmCol struct {
	Key   string
	Field *tasks.Field
	On    bool   // shown; the hidden ones are offered by the gear
	Sort  string // the sort of the column, "" — none
}

// crmSorts: the columns of deal and lead lists one may sort by (crm.Filter's).
var crmSorts = map[string]bool{"title": true, "stage": true, "amount": true, "responsible": true, "created": true}

// crmListCols puts the person's columns of the list (as «Настроить колонки»: the chosen ones in their
// order, then the others hidden), the custom fields' values and, when shown, the observers of the cards into data.
func (s *Server) crmListCols(r *http.Request, sess auth.Session, data map[string]any, scope string, ids []string) error {
	entity := scope
	if strings.HasPrefix(scope, "type:") {
		entity = "deal"
	}
	fields, err := s.Tasks.FieldsOf(r.Context(), tasks.CRMFieldSets[entity])
	if err != nil {
		return err
	}
	fids := make([]string, len(fields))
	for i, f := range fields {
		fids[i] = f.ID
	}
	on, err := s.CRM.Columns(r.Context(), sess.UserID, scope, fids)
	if err != nil {
		return err
	}
	byKey := map[string]crmCol{}
	all := []string{}
	for _, c := range crm.ListColumns[crm.ColumnKind(scope)] {
		byKey[c] = crmCol{Key: c}
		all = append(all, c)
	}
	for i := range fields {
		k := "f:" + fields[i].ID
		byKey[k] = crmCol{Key: k, Field: &fields[i]}
		all = append(all, k)
	}
	sortable := entity == "deal" || entity == "lead"
	cols := []crmCol{}
	add := func(k string, shown bool) {
		c := byKey[k]
		c.On = shown
		if sortable && crmSorts[k] {
			c.Sort = k
		}
		cols = append(cols, c)
		delete(byKey, k)
	}
	for _, k := range on {
		add(k, true)
	}
	for _, k := range all {
		if _, left := byKey[k]; left {
			add(k, false)
		}
	}
	shown := map[string]bool{}
	for _, k := range on {
		shown[k] = true
	}
	data["ListCols"], data["ColScope"], data["ColOn"] = cols, scope, shown
	if len(fields) > 0 {
		vals, err := s.Tasks.ValuesOf(r.Context(), tasks.CRMFieldSets[entity], ids)
		if err != nil {
			return err
		}
		for _, f := range fields { // dates as people read them
			if f.Kind == "date" {
				for _, v := range vals {
					if x, ok := v[f.ID]; ok {
						v[f.ID] = tasks.FieldValue{Field: f, Value: x}.Text(s.tr(r, "format.date"))
					}
				}
			}
		}
		data["Values"] = vals
	}
	for _, k := range on {
		if k == "observers" {
			data["Observers"], err = s.CRM.ObserversOf(r.Context(), entity, ids)
		}
	}
	return err
}

// crmSetColumns saves the person's columns of a CRM list (POST scope, cols in their order; reset — the default ones).
func (s *Server) crmSetColumns(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	back := crmBack(r.FormValue("back"))
	scope := r.FormValue("scope")
	entity := scope
	if strings.HasPrefix(scope, "type:") {
		entity = "deal"
	}
	set, ok := tasks.CRMFieldSets[entity]
	if !ok {
		http.NotFound(w, r)
		return
	}
	fields, err := s.Tasks.FieldsOf(r.Context(), set)
	if err == nil {
		fids := make([]string, len(fields))
		for i, f := range fields {
			fids[i] = f.ID
		}
		cols := r.Form["cols"]
		if r.FormValue("reset") != "" {
			cols = nil
		} else if cols == nil {
			cols = []string{} // all unticked: only the title
		}
		err = s.CRM.SetColumns(r.Context(), sess.UserID, scope, cols, fids)
	}
	if err != nil {
		if wantsJSON(r) {
			s.jsonErr(w, r, err)
		} else {
			s.taskErr(w, r, err, back)
		}
		return
	}
	if wantsJSON(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// typeID is the id of a smart process, "" for deals.
func typeID(t *crm.Type) string {
	if t == nil {
		return ""
	}
	return t.ID
}

// crmFilter reads the list's filter from the address; «me» (of a preset) is the person looking.
func crmFilter(q url.Values, me string) crm.Filter {
	f := crm.Filter{Q: q.Get("q"), Stage: q.Get("stage"), Responsible: q.Get("responsible"), Observer: q.Get("observer"),
		Source: q.Get("source"), Sort: q.Get("sort"), Asc: q.Get("asc") == "1"}
	if v := q.Get("semantic"); v == "process" || v == "success" || v == "failure" || v == "closed" {
		f.Semantic = v
	}
	if f.Responsible == "me" {
		f.Responsible = me
	}
	if f.Observer == "me" {
		f.Observer = me
	}
	day := func(k string) *time.Time {
		if t, err := time.ParseInLocation("2006-01-02", q.Get(k), time.Local); err == nil {
			return &t
		}
		return nil
	}
	sum := func(k string) *float64 {
		if strings.TrimSpace(q.Get(k)) == "" {
			return nil
		}
		v := formNumber(q.Get(k))
		return &v
	}
	f.From, f.To, f.AmountMin, f.AmountMax = day("from"), day("to"), sum("min"), sum("max")
	return f
}

// crmQuery is the address's filter and view without the sort, for the sort links and the pipeline chips.
func crmQuery(q url.Values) string {
	keep := url.Values{}
	for _, k := range []string{"q", "stage", "semantic", "responsible", "observer", "source", "from", "to", "min", "max", "view"} {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	return keep.Encode()
}

// crmPresetKeys are what a filter preset keeps: the filter without the search, the order and the view.
var crmPresetKeys = []string{"stage", "semantic", "responsible", "observer", "source", "from", "to", "min", "max"}

// crmPresetQuery is the address's filter as a preset keeps it; a chip whose preset has it is on.
func crmPresetQuery(q url.Values) string {
	keep := url.Values{}
	for _, k := range crmPresetKeys {
		if v := q.Get(k); v != "" {
			keep.Set(k, v)
		}
	}
	return keep.Encode()
}

// crmFiltered: the address has a filter of its own (the default preset is not applied then).
func crmFiltered(q url.Values) bool { return crmPresetQuery(q) != "" }

// crmPresetSave keeps the list's current filter under a name: POST scope, name, query, default, back.
func (s *Server) crmPresetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := crmBack(r.FormValue("back"))
	scope := r.FormValue("scope")
	if scope != "deal" && scope != "lead" && !strings.HasPrefix(scope, "type:") {
		http.NotFound(w, r)
		return
	}
	q, _ := url.ParseQuery(r.FormValue("query"))
	if err := s.CRM.SavePreset(r.Context(), sess.UserID, scope, r.FormValue("name"), crmPresetQuery(q), r.FormValue("default") == "1"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// crmPresetAction deletes the person's preset (op=delete) or makes it the default of its list and back (op=default).
func (s *Server) crmPresetAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := crmBack(r.FormValue("back"))
	var err error
	switch r.FormValue("op") {
	case "delete":
		err = s.CRM.DeletePreset(r.Context(), sess.UserID, r.PathValue("id"))
	case "default":
		err = s.CRM.SetDefaultPreset(r.Context(), sess.UserID, r.PathValue("id"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// crmBack is a return address within the CRM, the deals otherwise.
func crmBack(back string) string {
	if !strings.HasPrefix(back, "/crm/") || strings.HasPrefix(back, "//") {
		return "/crm/deals"
	}
	return back
}

// crmColumn is a kanban column: a stage with its cards and their sum.
type crmColumn struct {
	Stage crm.Stage
	Items []any
	Sum   float64
}

func crmColumns[T any](stages []crm.Stage, items []T, of func(T) (string, float64)) []crmColumn {
	cols := make([]crmColumn, len(stages))
	idx := map[string]int{}
	for i, st := range stages {
		cols[i].Stage, idx[st.ID] = st, i
	}
	for _, it := range items {
		id, sum := of(it)
		if i, ok := idx[id]; ok {
			cols[i].Items = append(cols[i].Items, it)
			cols[i].Sum += sum
		}
	}
	return cols
}

// crmSums are the cards' amounts in the base currency by id: a moved card carries its part of the column's sum.
func crmSums[T any](items []T, of func(T) (string, float64)) map[string]float64 {
	out := make(map[string]float64, len(items))
	for _, it := range items {
		id, sum := of(it)
		out[id] = sum
	}
	return out
}

// crmClients are the names of the contacts and companies the user sees: the hints of the «+» form's client.
func (s *Server) crmClients(r *http.Request, sess auth.Session) ([]string, error) {
	contacts, err := s.CRM.Contacts(r.Context(), sess.UserID, "", "")
	if err != nil {
		return nil, err
	}
	companies, err := s.CRM.Companies(r.Context(), sess.UserID, "")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range companies {
		out = append(out, c.Title)
	}
	for _, c := range contacts {
		out = append(out, c.Name)
	}
	return out, nil
}

// crmCard is a card: its fields (and the form to change them), the linked cards and the timeline.
func (s *Server) crmCard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	entity := crmEntities[kind]
	if entity == "" {
		http.NotFound(w, r)
		return
	}
	data := map[string]any{"Kind": kind, "Entity": entity, "Me": sess.UserID, "Edit": r.URL.Query().Get("edit") == "1", "Admin": tasks.IsAdmin(r.Context())}
	nav := "crm_" + kind
	var err error
	var title string
	switch entity {
	case "deal":
		var d crm.Deal
		if d, err = s.CRM.Deal(r.Context(), id); err == nil {
			var pipes []crm.Pipeline
			if d.TypeID != "" {
				var t crm.Type
				if t, err = s.CRM.Type(r.Context(), d.TypeID); err == nil {
					data["Type"], nav = &t, "crm_type_"+t.ID
				}
			}
			if err == nil {
				pipes, err = s.CRM.PipelinesOf(r.Context(), d.TypeID)
			}
			var others []crm.Pipeline // «Сменить воронку»
			for _, p := range pipes {
				if p.ID == d.PipelineID {
					data["Stages"] = p.Stages
				} else {
					others = append(others, p)
				}
			}
			data["OtherPipes"] = others
			data["Item"], title = d, d.Title
			if d.TypeID == "" && err == nil { // «Регулярная сделка»
				data["Recurrence"], err = s.CRM.Recurrence(r.Context(), d.ID)
				data["RecurUnits"], data["Today"] = crm.RecurUnits, time.Now().Format(time.DateOnly)
			}
		}
	case "lead":
		var l crm.Lead
		if l, err = s.CRM.Lead(r.Context(), id); err == nil {
			data["Stages"], err = s.CRM.LeadStages(r.Context())
			data["Item"], title = l, l.Title
		}
	case "contact":
		var x crm.Contact
		if x, err = s.CRM.Contact(r.Context(), id); err == nil {
			data["Deals"], err = s.CRM.Deals(r.Context(), sess.UserID, "", "", "", id)
			data["Item"], title = x, x.Name
		}
	case "company":
		var c crm.Company
		if c, err = s.CRM.Company(r.Context(), id); err == nil {
			if data["Contacts"], err = s.CRM.Contacts(r.Context(), sess.UserID, "", id); err == nil {
				data["Deals"], err = s.CRM.Deals(r.Context(), sess.UserID, "", "", id, "")
			}
			data["Item"], title = c, c.Title
		}
	}
	if list, ok := data["Deals"].([]crm.Deal); ok && err == nil { // the client's deals, and apart the cards of each smart process
		data["Deals"], data["SmartItems"], err = s.crmSplitDeals(r, list)
	}
	if err == nil {
		err = s.CRM.CheckCard(r.Context(), sess.UserID, entity, "read", id, crmResponsible(data["Item"]))
	}
	if err == nil {
		resp := crmResponsible(data["Item"])
		data["CanWrite"], err = s.CRM.Can(r.Context(), sess.UserID, entity, "write", resp)
		if err == nil {
			data["CanDelete"], err = s.CRM.Can(r.Context(), sess.UserID, entity, "delete", resp)
		}
	}
	if err == nil && s.Mail != nil { // the answers in the card's conversations join it (maillinks.go)
		if cerr := s.Mail.CRMCatchUp(r.Context(), entity, id); cerr != nil {
			s.Log.Warn("crm mail catch-up", "err", cerr)
		}
	}
	if err == nil {
		data["Timeline"], err = s.CRM.Timeline(r.Context(), entity, id)
	}
	if err == nil && (entity == "deal" || entity == "lead") { // «Товары»: the card's rows and the catalog to add from
		var rows []crm.ProductRow
		if rows, err = s.CRM.ProductRows(r.Context(), entity, id); err == nil {
			data["Rows"] = rows
			data["RowsTotal"], data["RowsVAT"] = crm.RowsTotal(rows)
			cur := ""
			switch it := data["Item"].(type) {
			case crm.Deal:
				cur = it.Currency
			case crm.Lead:
				cur = it.Currency
			}
			data["Catalog"], err = s.CRM.ProductsIn(r.Context(), cur)
		}
		if err == nil && entity == "deal" {
			err = s.crmDealStock(r, sess, data, id, rows)
		}
		if err == nil {
			data["Currencies"], err = s.CRM.Currencies(r.Context())
		}
	}
	if err == nil { // the administrator's own fields of this kind of card
		data["Fields"], err = s.Tasks.FieldValuesOf(r.Context(), tasks.CRMFieldSets[entity], id)
	}
	if err == nil { // «Дела»: the open ones, and the form planning one in an hour by default
		data["Activities"], err = s.CRM.Activities(r.Context(), entity, id)
		data["ActKinds"], data["ActDeadline"] = crm.ActivityKinds, time.Now().Add(time.Hour).Truncate(time.Hour).Format("2006-01-02T15:04")
	}
	if err == nil {
		if sess.Admin {
			data["Docs"], err = s.CRM.DocTemplates(r.Context(), entity)
		} else { // «Кому доступен»
			data["Docs"], err = s.CRM.DocTemplatesFor(r.Context(), entity, sess.UserID)
		}
	}
	if err == nil { // «Документы» made for the card
		data["Documents"], err = s.CRM.Documents(r.Context(), entity, id)
		data["PDF"], data["Base"] = s.Office != nil, s.BaseURL
	}
	if err == nil && s.Sign != nil { // «Подпись»: the latest sending of each document
		data["Signs"], err = s.Sign.OfCard(r.Context(), entity, id)
	}
	if err == nil && data["CanWrite"] == true { // «Письмо» to the client
		data["MailForm"], err = s.crmMailForm(r, sess, entity, id)
	}
	if err == nil && s.BP != nil { // business processes of this kind of card, and those of this card
		var tpls []bp.Template
		if tpls, err = s.BP.CardTemplates(r.Context(), entity, id); err == nil {
			data["BPTemplates"], err = s.BP.Startable(bpAdmin(r, sess), sess.UserID, tpls)
		}
		if err == nil {
			data["BPInstances"], err = s.BP.Instances(r.Context(), sess.UserID, entity, id)
		}
	}
	if err == nil { // tasks made from this card (or linked to it), as the viewer may see them
		data["Tasks"], err = s.Tasks.ListTasks(r.Context(), tasks.Filter{Viewer: sess.UserID, CRMLink: "/crm/" + kind + "/" + id})
		data["Path"] = "/crm/" + kind + "/" + id
	}
	if err == nil {
		data["Users"], err = s.Tasks.ListUsers(r.Context())
	}
	if err == nil { // «Справочники»: source, types, industry
		data["Dicts"], err = s.CRM.Dicts(r.Context())
	}
	if err == nil && (entity == "deal" || entity == "contact") {
		data["Companies"], err = s.CRM.Companies(r.Context(), sess.UserID, "")
	}
	if err == nil && entity != "deal" { // «Контроль дубликатов»: cards with the same phone, e-mail, name or ИНН
		var name, phone, email string
		switch x := data["Item"].(type) {
		case crm.Lead:
			phone, email = x.Phone, x.Email
		case crm.Contact:
			name, phone, email = x.Name, x.Phone, x.Email
		case crm.Company:
			name, phone, email = x.Title, x.Phone, x.Email
		}
		data["Duplicates"], err = s.CRM.Duplicates(r.Context(), entity, id, name, phone, email)
	}
	if err == nil {
		err = s.cardUTM(r.Context(), entity, id, data)
	}
	if err == nil && (entity == "company" || entity == "contact") { // «Реквизиты», and a new one named as the card
		data["Requisites"], err = s.CRM.Requisites(r.Context(), entity, id)
		data["ReqPresets"], data["NewRequisite"] = crm.RequisitePresets, crm.Requisite{Preset: map[string]string{"company": "org", "contact": "person"}[entity], Name: title}
	}
	if err == nil && s.OpenLines != nil && (entity == "lead" || entity == "contact") { // «Открыть чат» of the open line's dialogue
		data["Chat"], err = s.OpenLines.ChatOf(r.Context(), entity, id)
	}
	if err == nil && (entity == "deal" || entity == "lead") {
		data["Invoices"], err = s.CRM.Invoices(r.Context(), "", id, "", "")
	}
	if err == nil && s.Booking != nil && entity != "lead" { // «Онлайн-запись»: the client's bookings and «Записать»
		data["Bookings"], err = s.Booking.OfCard(r.Context(), entity, id)
	}
	if err == nil && entity == "deal" {
		data["AllContacts"], err = s.CRM.Contacts(r.Context(), sess.UserID, "", "")
	}
	if l, ok := data["Item"].(crm.Lead); ok && err == nil && data["CanWrite"] == true && l.DealID == "" { // «Конвертация»
		var c crm.ConvertTo
		if c, err = s.CRM.ConvertDefaults(r.Context(), l); err == nil {
			data["Convert"] = c
			data["DealPipes"], err = s.CRM.Pipelines(r.Context())
		}
		if err == nil {
			data["AllContacts"], err = s.CRM.Contacts(r.Context(), sess.UserID, "", "")
		}
		if err == nil {
			data["Companies"], err = s.CRM.Companies(r.Context(), sess.UserID, "")
		}
	}
	if err == nil { // «Места встраивания» of the applications: their tabs of this kind of card
		d, _ := data["Item"].(crm.Deal)
		data["AppTabs"] = s.appTabs(r, sess.UserID, apps.CardPlace(entity, d.TypeID))
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	s.render(w, r, "crmcard", page{Title: title, Nav: nav, Data: data})
}

// crmResponsible is the responsible of a card (for the rights).
func crmResponsible(item any) string {
	switch c := item.(type) {
	case crm.Deal:
		return c.Responsible.ID
	case crm.Lead:
		return c.Responsible.ID
	case crm.Contact:
		return c.Responsible.ID
	case crm.Company:
		return c.Responsible.ID
	}
	return ""
}

// crmCheck: may the person do the action on the card (kind, id)?
func (s *Server) crmCheck(r *http.Request, sess auth.Session, kind, id, action string) error {
	var item any
	var err error
	switch crmEntities[kind] {
	case "deal":
		item, err = s.CRM.Deal(r.Context(), id)
	case "lead":
		item, err = s.CRM.Lead(r.Context(), id)
	case "contact":
		item, err = s.CRM.Contact(r.Context(), id)
	case "company":
		item, err = s.CRM.Company(r.Context(), id)
	default:
		return crm.ErrNotFound
	}
	if err != nil {
		return err
	}
	return s.CRM.CheckCard(r.Context(), sess.UserID, crmEntities[kind], action, id, crmResponsible(item))
}

// crmSave creates (id "new") or changes a card from its form: writing it now and with the responsible chosen.
func (s *Server) crmSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if id == "new" {
		id = ""
	}
	if crmEntities[kind] == "" {
		http.NotFound(w, r)
		return
	}
	newResp := r.FormValue("responsible")
	if newResp == "" {
		newResp = sess.UserID
	}
	err := s.CRM.Check(r.Context(), sess.UserID, crmEntities[kind], "write", newResp)
	if err == nil && id != "" {
		err = s.crmCheck(r, sess, kind, id, "write")
	}
	if err != nil {
		s.crmErr(w, r, err, "/crm/"+kind)
		return
	}
	f := func(k string) string { return r.FormValue(k) }
	values, err := s.crmFormValues(r, crmEntities[kind])
	if err == nil { // «Обязательные поля»: those required at the card's stage (or always) must be filled first
		var stage string
		if stage, err = s.CRM.CardStage(r.Context(), crmEntities[kind], id, f("pipeline"), f("stage")); err == nil {
			err = s.CRM.CheckRequired(r.Context(), crmEntities[kind], id, stage, values)
		}
	}
	if err != nil {
		back := "/crm/" + kind
		if id != "" {
			back += "/" + id + "?edit=1"
		}
		s.crmErr(w, r, err, back)
		return
	}
	amount := func() float64 {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(f("amount"), " ", ""), ",", "."), 64)
		return v
	}
	resp := crm.Ref{ID: f("responsible")}
	var observers []crm.Ref // nil keeps them: the form without the field
	if f("with_observers") == "1" {
		observers = []crm.Ref{}
		for _, o := range r.Form["observers"] {
			observers = append(observers, crm.Ref{ID: o})
		}
	}
	switch crmEntities[kind] {
	case "deal":
		var close *time.Time
		if t, perr := time.ParseInLocation("2006-01-02", f("close_date"), time.Local); perr == nil {
			close = &t
		}
		contacts := []crm.Ref{} // the form's contacts in their order: none chosen clears them
		for _, c := range r.Form["contact"] {
			contacts = append(contacts, crm.Ref{ID: c})
		}
		company := f("company")
		if name := strings.TrimSpace(f("client")); name != "" && id == "" { // the kanban's «+»: the client by name
			var contact string
			if company, contact, err = s.crmClient(r, sess, name); err != nil {
				s.crmErr(w, r, err, "/crm/"+kind)
				return
			}
			if contact != "" {
				contacts = append(contacts, crm.Ref{ID: contact})
			}
		}
		id, err = s.CRM.SaveDeal(r.Context(), sess.UserID, crm.Deal{ID: id, Title: f("title"), PipelineID: f("pipeline"),
			Stage: crm.Stage{ID: f("stage")}, Amount: amount(), Currency: f("currency"), Company: crm.Ref{ID: company},
			Contacts: contacts, Responsible: resp, CloseDate: close, Comment: f("comment"), DealType: crm.Dict{ID: f("deal_type")},
			Origin: crm.Dict{ID: f("origin")}, Observers: observers})
	case "lead":
		id, err = s.CRM.SaveLead(r.Context(), sess.UserID, crm.Lead{ID: id, Title: f("title"), Stage: crm.Stage{ID: f("stage")}, Name: f("name"), CompanyTitle: f("company_title"),
			Phone: f("phone"), Email: f("email"), Source: f("source"), Amount: amount(), Currency: f("currency"), Responsible: resp, Comment: f("comment"),
			Origin: crm.Dict{ID: f("origin")}, Observers: observers})
	case "contact":
		id, err = s.CRM.SaveContact(r.Context(), sess.UserID, crm.Contact{ID: id, Name: f("name"), Position: f("position"), Phone: f("phone"),
			Email: f("email"), Company: crm.Ref{ID: f("company")}, Responsible: resp, Comment: f("comment"),
			ContactType: crm.Dict{ID: f("contact_type")}, Origin: crm.Dict{ID: f("origin")}})
	case "company":
		id, err = s.CRM.SaveCompany(r.Context(), sess.UserID, crm.Company{ID: id, Title: f("title"), Phone: f("phone"), Email: f("email"),
			Website: f("website"), Address: f("address"), Responsible: resp, Comment: f("comment"),
			CompanyType: crm.Dict{ID: f("company_type")}, Industry: crm.Dict{ID: f("industry")}})
	default:
		http.NotFound(w, r)
		return
	}
	back := "/crm/" + kind
	if r.PathValue("id") != "new" {
		back += "/" + r.PathValue("id")
	}
	if err == nil && len(values) > 0 {
		err = s.CRM.SetFields(r.Context(), sess.UserID, crmEntities[kind], id, values)
	}
	if err == nil && f("with_utm") != "" { // «UTM-метки»
		err = s.setUTM(r.Context(), crmEntities[kind], id, marketing.ParseUTM(f))
	}
	if err != nil {
		s.crmErr(w, r, err, back)
		return
	}
	if wantsJSON(r) && r.PathValue("id") == "new" && (kind == "deals" || kind == "leads") { // the kanban's «+»
		card, err := s.crmCardHTML(r, kind, id)
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "card": card})
		return
	}
	http.Redirect(w, r, "/crm/"+kind+"/"+id+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// crmClient finds the client of the «+» form by name: a company with this title, else a contact with
// this name, else a new contact of the user.
func (s *Server) crmClient(r *http.Request, sess auth.Session, name string) (company, contact string, err error) {
	companies, err := s.CRM.Companies(r.Context(), sess.UserID, name)
	if err != nil {
		return "", "", err
	}
	for _, c := range companies {
		if strings.EqualFold(c.Title, name) {
			return c.ID, "", nil
		}
	}
	contacts, err := s.CRM.Contacts(r.Context(), sess.UserID, name, "")
	if err != nil {
		return "", "", err
	}
	for _, c := range contacts {
		if strings.EqualFold(c.Name, name) {
			return "", c.ID, nil
		}
	}
	if err = s.CRM.Check(r.Context(), sess.UserID, "contact", "write", sess.UserID); err != nil {
		return "", "", err
	}
	contact, err = s.CRM.SaveContact(r.Context(), sess.UserID, crm.Contact{Name: name, Responsible: crm.Ref{ID: sess.UserID}})
	return "", contact, err
}

// crmCardHTML renders a new kanban card with the list's template, so the column's «+» puts it on the page.
func (s *Server) crmCardHTML(r *http.Request, kind, id string) (string, error) {
	rates, err := s.CRM.Rates(r.Context())
	if err != nil {
		return "", err
	}
	var item any
	var sum float64
	if kind == "deals" {
		d, err := s.CRM.Deal(r.Context(), id)
		if err != nil {
			return "", err
		}
		item, sum = d, crm.InBase(rates, d.Amount, d.Currency)
	} else {
		l, err := s.CRM.Lead(r.Context(), id)
		if err != nil {
			return "", err
		}
		item, sum = l, crm.InBase(rates, l.Amount, l.Currency)
	}
	next, err := s.CRM.NextActivities(r.Context(), crmEntities[kind], []string{id}) // a robot may have planned one
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = s.pages[s.lang(r)]["crm"].ExecuteTemplate(&buf, "crmkcard", map[string]any{"K": kind, "I": item, "N": next[id], "S": sum})
	return buf.String(), err
}

// crmFormValues are the own fields a form carries: every one with with_fields (the card's form; an unchecked yes/no
// is ""), else those it has (the new card's form, the dialog asking for the required ones). Nil — none.
func (s *Server) crmFormValues(r *http.Request, entity string) (map[string]string, error) {
	set, ok := tasks.CRMFieldSets[entity]
	if !ok {
		return nil, nil
	}
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	all, some := r.FormValue("with_fields") == "1", false
	for k := range r.Form {
		some = some || strings.HasPrefix(k, "f_")
	}
	if !all && !some {
		return nil, nil
	}
	list, err := s.Tasks.FieldsOf(r.Context(), set)
	if err != nil {
		return nil, err
	}
	var values map[string]string
	for _, fd := range list {
		if _, has := r.Form["f_"+fd.ID]; has || all {
			if values == nil {
				values = map[string]string{}
			}
			values[fd.ID] = r.FormValue("f_" + fd.ID)
		}
	}
	return values, nil
}

// crmErr is taskErr knowing the required fields left empty: their names in the message, and the fields themselves
// for a form sent in the background (the kanban and the stage bar ask for them in a dialog).
func (s *Server) crmErr(w http.ResponseWriter, r *http.Request, err error, back string) {
	var re *crm.RequiredError
	switch {
	case errors.As(err, &re) && (fromForm(r) || wantsJSON(r)):
		s.crmJSONErr(w, r, err)
	case wantsJSON(r):
		s.jsonErr(w, r, err)
	case errors.As(err, &re):
		s.taskErr(w, r, re.I18n(), back)
	default:
		s.taskErr(w, r, err, back)
	}
}

func (s *Server) crmJSONErr(w http.ResponseWriter, r *http.Request, err error) {
	var re *crm.RequiredError
	if !errors.As(err, &re) {
		s.jsonErr(w, r, err)
		return
	}
	type field struct {
		ID      string   `json:"id"`
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Options []string `json:"options"`
		Value   string   `json:"value"`
	}
	fields := make([]field, len(re.Fields))
	for i, f := range re.Fields {
		fields[i] = field{f.ID, f.Name, f.Kind, f.Options, f.Value}
	}
	ie := re.I18n()
	writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": s.I18n.Err(s.lang(r), ie), "code": ie.Key, "fields": fields})
}

func (s *Server) crmDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	var err error
	switch crmEntities[kind] {
	case "deal":
		err = s.CRM.DeleteDeal(r.Context(), sess.UserID, id)
	case "lead":
		err = s.CRM.DeleteLead(r.Context(), sess.UserID, id)
	case "contact":
		err = s.CRM.DeleteContact(r.Context(), sess.UserID, id)
	case "company":
		err = s.CRM.DeleteCompany(r.Context(), sess.UserID, id)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/"+kind+"/"+id)
		return
	}
	http.Redirect(w, r, "/crm/"+kind, http.StatusSeeOther)
}

func (s *Server) crmComment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if err := s.crmCheck(r, sess, kind, id, "read"); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	if err := s.CRM.Comment(r.Context(), sess.UserID, crmEntities[kind], id, r.FormValue("body")); err != nil {
		s.taskErr(w, r, err, "/crm/"+kind+"/"+id)
		return
	}
	http.Redirect(w, r, "/crm/"+kind+"/"+id+"#timeline", http.StatusSeeOther)
}

// crmEvent changes a line of a card's timeline: pins or unpins it (whoever may change the card), edits or deletes
// a comment (its author or an administrator).
func (s *Server) crmEvent(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id, act := r.PathValue("kind"), r.PathValue("id"), r.PathValue("act")
	back := "/crm/" + kind + "/" + id + "#timeline"
	ev, err := strconv.ParseInt(r.PathValue("ev"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	need := "read"
	if act == "pin" || act == "unpin" {
		need = "write"
	}
	if err = s.crmCheck(r, sess, kind, id, need); err == nil {
		entity := crmEntities[kind]
		switch act {
		case "pin", "unpin":
			err = s.CRM.Pin(r.Context(), entity, id, ev, act == "pin")
		case "edit":
			err = s.CRM.EditComment(r.Context(), sess.UserID, entity, id, ev, r.FormValue("body"))
		case "delete":
			err = s.CRM.DeleteComment(r.Context(), sess.UserID, entity, id, ev)
		default:
			err = crm.ErrNotFound
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// crmStage moves a deal or a lead to a stage: the kanban (JSON) or the card's stage bar.
func (s *Server) crmStage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	entity, stage := crmEntities[kind], r.FormValue("stage")
	err := s.crmCheck(r, sess, kind, id, "write")
	var values map[string]string
	if err == nil && entity != "deal" && entity != "lead" {
		err = i18n.E("err.bad_form")
	}
	if err == nil { // the fields required at the stage: as the card has them, or filled in by the asking dialog
		if values, err = s.crmFormValues(r, entity); err == nil {
			err = s.CRM.CheckRequired(r.Context(), entity, id, stage, values)
		}
	}
	if err == nil && len(values) > 0 {
		err = s.CRM.SetFields(r.Context(), sess.UserID, entity, id, values)
	}
	switch {
	case err != nil:
	case entity == "deal":
		_, err = s.CRM.MoveDeal(r.Context(), sess.UserID, id, stage)
	default:
		_, err = s.CRM.MoveLead(r.Context(), sess.UserID, id, stage)
	}
	if wantsJSON(r) {
		if err != nil {
			s.crmJSONErr(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		s.crmErr(w, r, err, "/crm/"+kind+"/"+id)
		return
	}
	http.Redirect(w, r, "/crm/"+kind+"/"+id, http.StatusSeeOther)
}

func (s *Server) crmConvert(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, back := r.PathValue("id"), "/crm/leads/"+r.PathValue("id")
	err := s.crmCheck(r, sess, "leads", id, "write")
	var to crm.ConvertTo
	if err == nil && r.FormValue("dialog") == "1" { // the dialog «Конвертация»; without it — what it offers
		to = crm.ConvertTo{Deal: r.FormValue("deal") == "1", Contact: r.FormValue("contact") == "1", Company: r.FormValue("company") == "1",
			PipelineID: r.FormValue("pipeline"), ContactID: r.FormValue("contact_id"), CompanyID: r.FormValue("company_id")}
	} else if err == nil {
		var l crm.Lead
		if l, err = s.CRM.Lead(r.Context(), id); err == nil {
			to, err = s.CRM.ConvertDefaults(r.Context(), l)
		}
	}
	for entity, made := range map[string]bool{"deal": to.Deal, "contact": to.Contact && to.ContactID == "", "company": to.Company && to.CompanyID == ""} {
		if err == nil && made {
			err = s.CRM.Check(r.Context(), sess.UserID, entity, "write", sess.UserID)
		}
	}
	if err == nil && to.ContactID != "" && to.Contact {
		err = s.crmCheck(r, sess, "contacts", to.ContactID, "read")
	}
	if err == nil && to.CompanyID != "" && to.Company {
		err = s.crmCheck(r, sess, "companies", to.CompanyID, "read")
	}
	var got crm.Converted
	if err == nil {
		got, err = s.CRM.ConvertLeadTo(r.Context(), sess.UserID, id, to)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	switch {
	case to.Deal && got.DealID != "":
		back = "/crm/deals/" + got.DealID
	case to.Contact && got.ContactID != "":
		back = "/crm/contacts/" + got.ContactID
	case got.CompanyID != "":
		back = "/crm/companies/" + got.CompanyID
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.converted")), http.StatusSeeOther)
}

// crmBulk: the group actions of the list — the checked deals or leads go to a stage, get a responsible, or are
// deleted; each card as if done on it (rights, timeline, robots). Cards one may not touch are skipped and counted.
func (s *Server) crmBulk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.PathValue("kind")
	entity := crmEntities[kind]
	back := "/crm/" + kind + "?view=list"
	if b := r.FormValue("back"); strings.HasPrefix(b, "/crm/"+kind) {
		back = b
	}
	if entity != "deal" && entity != "lead" {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	op, value := r.FormValue("op"), r.FormValue("value")
	done, skipped := 0, 0
	for _, id := range r.PostForm["id"] {
		err := s.crmCheck(r, sess, kind, id, "write")
		if err == nil && op == "stage" { // a card lacking a field required at the stage stays where it is
			err = s.CRM.CheckRequired(r.Context(), entity, id, value, nil)
		}
		switch {
		case err != nil:
		case op == "stage" && entity == "deal":
			_, err = s.CRM.MoveDeal(r.Context(), sess.UserID, id, value)
		case op == "stage":
			_, err = s.CRM.MoveLead(r.Context(), sess.UserID, id, value)
		case op == "responsible":
			if err = s.CRM.Check(r.Context(), sess.UserID, entity, "write", value); err == nil {
				err = s.CRM.SetResponsible(r.Context(), sess.UserID, entity, id, value)
			}
		case op == "delete" && entity == "deal":
			err = s.CRM.DeleteDeal(r.Context(), sess.UserID, id)
		case op == "delete":
			err = s.CRM.DeleteLead(r.Context(), sess.UserID, id)
		default:
			err = i18n.E("err.bad_form")
		}
		if err != nil {
			skipped++
			continue
		}
		done++
	}
	msg := s.tr(r, "crm.bulk_done", done)
	if skipped > 0 {
		msg += " " + s.tr(r, "crm.bulk_skipped", skipped)
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// crmRecurrence: who may change a deal repeats it on schedule («Регулярная сделка») or stops that.
func (s *Server) crmRecurrence(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/crm/deals/" + id
	err := s.crmCheck(r, sess, "deals", id, "write")
	if err == nil {
		switch r.FormValue("op") {
		case "set":
			rc := crm.Recurrence{DealID: id, Unit: r.FormValue("unit"), Every: atoi(r.FormValue("every")), Limit: atoi(r.FormValue("limit"))}
			if rc.Start, err = time.ParseInLocation(time.DateOnly, r.FormValue("start"), time.Local); err != nil {
				err = i18n.E("err.crm_recur_start")
				break
			}
			if v := r.FormValue("until"); v != "" {
				u, perr := time.ParseInLocation(time.DateOnly, v, time.Local)
				if perr != nil {
					err = i18n.E("err.crm_recur_until")
					break
				}
				rc.Until = &u
			}
			err = s.CRM.SetRecurrence(r.Context(), sess.UserID, rc, time.Now())
		case "stop":
			err = s.CRM.StopRecurrence(r.Context(), id)
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved"))+"#recurrence", http.StatusSeeOther)
}
