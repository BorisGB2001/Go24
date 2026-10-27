package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Бизнес-процессы» ----------

// bpHome: what to start, my approvals, my processes; all the processes (administrators) with ?tab=all&status=&template=.
func (s *Server) bpHome(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	tab := q.Get("tab")
	if tab == "all" && !sess.Admin {
		tab = ""
	}
	data := map[string]any{"Tab": tab}
	tpls, err := s.BP.Templates(r.Context(), "none", true)
	if err == nil {
		tpls, err = s.BP.Startable(bpAdmin(r, sess), sess.UserID, tpls)
	}
	if err == nil {
		data["Templates"] = tpls
		data["Approvals"], err = s.BP.MyApprovals(r.Context(), sess.UserID)
	}
	if err == nil && tab == "all" {
		pg, _ := strconv.Atoi(q.Get("page"))
		pg = max(pg, 0)
		f := bp.Filter{Status: q.Get("status"), TemplateID: q.Get("template"), Limit: 51, Offset: pg * 50}
		var all []bp.Instance
		if all, err = s.BP.AllInstances(r.Context(), f); err == nil {
			if len(all) > 50 { // one more than a page: there is a next page
				all, data["Next"] = all[:50], pg+1
			}
			data["Instances"], data["Status"], data["Template"], data["Page"] = all, f.Status, f.TemplateID, pg
			data["Statuses"] = bp.Statuses
			data["AllTemplates"], err = s.BP.Templates(r.Context(), "", false)
		}
	} else if err == nil {
		data["Instances"], err = s.BP.Instances(r.Context(), sess.UserID, "", "")
	}
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "bp", page{TitleKey: "bp.title", Nav: "bp", Data: data})
}

// bpAdmin: the processes are administered by the portal's administrators, as the constructor is (not only in the admin mode).
func bpAdmin(r *http.Request, sess auth.Session) context.Context {
	if sess.Admin {
		return tasks.WithAdmin(r.Context())
	}
	return r.Context()
}

// bpStop: «Остановить» with a reason; back to the process, or to a CRM card, a file's card or the list (back=…).
func (s *Server) bpStop(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp") && !strings.HasPrefix(back, "/crm/") && !reCardBack.MatchString(back) {
		back = "/bp/i/" + id
	}
	if err := s.BP.Stop(bpAdmin(r, sess), sess.UserID, id, r.FormValue("reason")); err != nil {
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bpDeleteInstance: «Удалить» a process with its history (administrators).
func (s *Server) bpDeleteInstance(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp") && !strings.HasPrefix(back, "/crm/") {
		back = "/bp?tab=all"
	}
	if err := s.BP.DeleteInstance(bpAdmin(r, sess), r.PathValue("id")); err != nil {
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bpStartPage: the form of a template (for a CRM card with ?entity_id=…).
func (s *Server) bpStartPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t, err := s.BP.Template(r.Context(), r.PathValue("id"))
	if err == nil {
		var ok bool
		if ok, err = s.BP.MayStart(bpAdmin(r, sess), sess.UserID, t.ID); err == nil && !ok {
			err = i18n.E("err.bp_no_start")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	s.render(w, r, "bpstart", page{Title: t.Name, Nav: "bp", Data: map[string]any{"T": t, "EntityID": r.URL.Query().Get("entity_id")}})
}

func (s *Server) bpStart(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t, err := s.BP.Template(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	values := map[string]string{}
	for _, f := range t.Fields {
		values[f.Key] = r.FormValue("f_" + f.Key)
	}
	id, err := s.BP.Start(bpAdmin(r, sess), sess.UserID, t.ID, r.FormValue("entity_id"), values)
	if err != nil {
		back := "/bp/start/" + t.ID
		if eid := r.FormValue("entity_id"); eid != "" {
			back += "?entity_id=" + url.QueryEscape(eid)
		}
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/bp/i/"+id, http.StatusSeeOther)
}

// bpInstance: a process — its form, steps, approvals and history; the person's own pending decision.
func (s *Server) bpInstance(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.BP.Instance(bpAdmin(r, sess), sess.UserID, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	approvals, err := s.BP.Approvals(r.Context(), x.ID)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	lines, err := s.BP.Log(r.Context(), x.ID)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	var mine *bp.Approval
	for i := range approvals {
		if approvals[i].User.ID == sess.UserID && approvals[i].Decision == "" && x.Status == "running" && x.Current(approvals[i].Step) {
			mine = &approvals[i]
		}
	}
	t, _ := s.BP.Template(r.Context(), x.TemplateID) // the field labels (the template may be gone)
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	card := ""
	if x.Entity != "none" {
		card = bp.CardPath(x.Entity, x.EntityID)
	}
	s.render(w, r, "bpinstance", page{Title: x.Name, Nav: "bp", Data: map[string]any{"X": x, "Approvals": approvals, "Log": lines,
		"Mine": mine, "Fields": s.bpLabels(r, x.Form(t.Fields)), "Card": card, "Users": users, "CanStop": s.BP.CanStop(bpAdmin(r, sess), sess.UserID, x)}})
}

// bpDecide: a decision on an approval or a review, or (decision=answer) the answer to a request with its fields f_<key>.
func (s *Server) bpDecide(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if r.FormValue("decision") == "answer" {
		values := map[string]string{}
		for k, v := range r.PostForm {
			if key, ok := strings.CutPrefix(k, "f_"); ok && len(v) > 0 {
				values[key] = v[0]
			}
		}
		err = s.BP.Answer(r.Context(), sess.UserID, r.PathValue("id"), values, r.FormValue("comment"))
	} else {
		err = s.BP.Decide(r.Context(), sess.UserID, r.PathValue("id"), r.FormValue("decision"), r.FormValue("comment"))
	}
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp/") && !strings.HasPrefix(back, "/feed?") {
		back = "/bp?tab=approvals"
	}
	if err != nil {
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bpDelegate: «Делегировать» — the approval (a review, a request) handed to the employee "to"; an administrator hands
// anyone's from the process's page.
func (s *Server) bpDelegate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	_, err := s.BP.Delegate(bpAdmin(r, sess), sess.UserID, r.PathValue("id"), r.FormValue("to"))
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp/") && !strings.HasPrefix(back, "/feed?") {
		back = "/bp?tab=approvals"
	}
	if err != nil {
		http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// bpFeedCard is a process as its post in the feed shows it: the state and the viewer's tasks of it.
type bpFeedCard struct {
	X    bp.Instance
	Mine []bp.Approval
}

// fillProcesses puts the processes of the posts into them, for the viewer: one who sees a post sees its process.
func (s *Server) fillProcesses(ctx context.Context, userID string, items ...[]feed.Item) {
	if s.BP == nil {
		return
	}
	var mine []bp.Approval
	loaded := false
	for _, list := range items {
		for _, it := range list {
			p := it.Post
			if p == nil || p.Process == "" {
				continue
			}
			x, err := s.BP.Instance(tasks.WithAdmin(ctx), userID, p.Process)
			if err != nil {
				continue
			}
			if !loaded {
				mine, _ = s.BP.MyApprovals(ctx, userID)
				loaded = true
			}
			c := &bpFeedCard{X: x}
			for _, a := range mine {
				if a.InstanceID == x.ID {
					c.Mine = append(c.Mine, a)
				}
			}
			p.ProcessCard = c
		}
	}
}

// ---------- the constructor (administrators) ----------

func (s *Server) bpTemplates(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.BP.Templates(r.Context(), "", false)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	installed, err := s.BP.Installed(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	data := map[string]any{"Templates": list}
	if len(installed) < len(bp.StdKeys) { // «Стандартные шаблоны»: some are not installed
		var missing []string
		for _, k := range bp.StdKeys {
			if !slices.Contains(installed, k) {
				missing = append(missing, k)
			}
		}
		if data["Users"], err = s.Tasks.ListUsers(r.Context()); err != nil {
			s.taskErr(w, r, err, "/bp")
			return
		}
		data["Std"] = missing
	}
	s.render(w, r, "bptemplates", page{TitleKey: "bp.templates", Nav: "bp", Data: data})
}

// bpTemplatePage is the constructor of one template («new» — an empty one).
func (s *Server) bpTemplatePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	t := bp.Template{Entity: "none", Active: true}
	if id := r.PathValue("id"); id != "new" {
		var err error
		if t, err = s.BP.Template(r.Context(), id); err != nil {
			s.taskErr(w, r, err, "/bp/templates")
			return
		}
	}
	if lid := r.URL.Query().Get("list"); t.ID == "" && lid != "" { // «Новый шаблон» of a list's page
		t.Entity, t.ListID = bp.EntityList, lid
	}
	lists, err := s.BP.Lists(bpAdmin(r, sess), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	var list *bp.List // of a template of a list's elements: its fields «Изменить элемент» sets
	for i := range lists {
		if lists[i].ID == t.ListID {
			list = &lists[i]
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	// «Запуск бизнес-процесса»: a standalone template or one of the same kind of card (started for that card)
	all, err := s.BP.Templates(r.Context(), "", false)
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	var others []bp.Template
	for _, o := range all {
		if o.Entity == "none" || o.Entity == t.Entity {
			others = append(others, o)
		}
	}
	var docs []crm.DocTemplate // the templates of documents «Создание документа» makes, of the card's kind
	if crm.Entities[t.Entity] {
		docs, _ = s.CRM.DocTemplates(r.Context(), t.Entity)
	}
	var stages []crm.Stage
	if t.Entity == "deal" { // the stages of the deals' pipelines, or of the smart process's
		pipes, _ := s.CRM.PipelinesOf(r.Context(), t.TypeID)
		for _, p := range pipes {
			stages = append(stages, p.Stages...)
		}
	} else if t.Entity == "lead" {
		stages, _ = s.CRM.LeadStages(r.Context())
	}
	var userFields []tasks.User // the steps' «field:KEY» people
	fields := s.bpLabels(r, bp.AllFields(t.Fields, t.Steps))
	for _, f := range fields {
		if f.Kind == "user" {
			userFields = append(userFields, tasks.User{ID: bp.FieldPrefix + f.Key, Name: s.tr(r, "bp.to.field", f.Label)})
		}
	}
	types, err := s.CRM.Types(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	ownFields := map[string][]string{} // the names a step may set besides the built-in fields
	for entity, set := range tasks.CRMFieldSets {
		list, _ := s.Tasks.FieldsOf(r.Context(), set)
		for _, f := range list {
			ownFields[entity] = append(ownFields[entity], f.Name)
		}
	}
	var starters []tasks.User // the chips of «Кто может запускать»
	if t.ID != "" {
		refs, err := s.BP.Starters(r.Context(), t.ID)
		if err != nil {
			s.taskErr(w, r, err, "/bp")
			return
		}
		for _, ref := range refs {
			starters = append(starters, tasks.User{ID: ref.ID, Name: ref.Name})
		}
	}
	s.render(w, r, "bptemplate", page{Title: t.Name, Nav: "bp", Data: map[string]any{"T": t, "Starters": starters, "FieldRows": bpFieldRows(t.Fields, users), "NewRows": bpFieldRows(nil, users), "FieldKinds": bp.FieldKinds, "UserFields": userFields, "Types": types, "Lists": lists, "List": list,
		"AllFields": fields, "DateFields": dateFields(bp.AllFields(t.Fields, t.Steps), "date"), "TimeFields": dateFields(bp.AllFields(t.Fields, t.Steps), "date", "datetime"), "AbsenceKinds": company.AbsenceKinds, "Kinds": bp.StepKinds, "CardFields": bp.CardFields, "OwnFields": ownFields, "Modes": bp.Modes, "PickModes": bp.PickModes, "Delegations": bp.Delegations, "Deeds": crm.ActivityKinds, "Docs": docs, "Scopes": bp.FieldScopes, "Others": others, "MaxLoops": bp.MaxLoops, "Ops": bp.Ops, "Users": users, "Projects": projects, "Stages": stages}})
}

// bpFieldRow is a row of the fields' editor (template "bpfields"): the options joined, a user's default as a name.
type bpFieldRow struct {
	Key, Label, Kind, Options, Default, Scope string
	Required                                  bool
}

// bpFieldRows are the rows of the fields and an empty one to add a field.
func bpFieldRows(fields []bp.Field, users []tasks.User) []bpFieldRow {
	var out []bpFieldRow
	for _, f := range fields {
		row := bpFieldRow{Key: f.Key, Label: f.Label, Kind: f.Kind, Options: strings.Join(f.Options, "; "), Default: f.Default, Required: f.Required, Scope: f.Scope}
		if f.Kind == "user" {
			for _, u := range users {
				if u.ID == f.Default {
					row.Default = u.Name
				}
			}
		}
		out = append(out, row)
	}
	return append(out, bpFieldRow{Kind: "string"})
}

// parseFields reads the rows of the fields' editor with the prefix: a row without a key is left out; the options of
// a select are split by «;», a user's default is an employee's name, e-mail or id.
func parseFields(r *http.Request, prefix string, users []tasks.User) []bp.Field {
	var out []bp.Field
	keys := r.Form[prefix+"_key"]
	at := func(name string, i int) string {
		if list := r.Form[prefix+"_"+name]; i < len(list) {
			return strings.TrimSpace(list[i])
		}
		return ""
	}
	for i, key := range keys {
		if key = strings.TrimSpace(key); key == "" {
			continue
		}
		f := bp.Field{Key: key, Label: at("label", i), Kind: at("kind", i), Required: at("req", i) == "1", Default: at("def", i), Scope: at("scope", i)}
		if f.Scope == "param" {
			f.Scope = ""
		}
		if f.Kind == "" {
			f.Kind = "string"
		}
		if f.Kind == "select" {
			for _, o := range strings.Split(at("opts", i), ";") {
				if o = strings.TrimSpace(o); o != "" {
					f.Options = append(f.Options, o)
				}
			}
		}
		if f.Kind == "user" && f.Default != "" {
			name := f.Default
			for _, u := range users {
				if u.ID == name || strings.EqualFold(u.Name, name) || u.Email != "" && strings.EqualFold(u.Email, name) {
					f.Default = u.ID
				}
			}
		}
		out = append(out, f)
	}
	return out
}

// renumber changes the step numbers a step refers to: its jump and where its parallel branches start.
func renumber(st *bp.Step, f func(int) int) {
	if st.Goto != 0 {
		st.Goto = f(st.Goto)
	}
	for k := range st.Branches {
		st.Branches[k] = f(st.Branches[k])
	}
}

// parseSets reads «field = value» lines.
func parseSets(text string) []bp.Assign {
	var out []bp.Assign
	for _, line := range strings.Split(text, "\n") {
		k, v, _ := strings.Cut(line, "=")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, bp.Assign{Field: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
	return out
}

// bpTemplateSave: op=meta (name, description, kind of card, active, fields), op=add (a step, before step at), op=delete|up
// (step n), op=starters («Кто может запускать»: to — people and «dept:ID»).
func (s *Server) bpTemplateSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if r.FormValue("op") == "starters" {
		id, msg := r.PathValue("id"), s.tr(r, "common.saved")
		if err := s.BP.SetStarters(r.Context(), id, r.Form["to"]); err != nil {
			msg = s.errText(r, err)
		}
		http.Redirect(w, r, "/bp/templates/"+id+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
		return
	}
	t := bp.Template{Entity: "none", Active: true}
	id := r.PathValue("id")
	if id != "new" {
		var err error
		if t, err = s.BP.Template(r.Context(), id); err != nil {
			s.taskErr(w, r, err, "/bp/templates")
			return
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/templates")
		return
	}
	n, _ := strconv.Atoi(r.FormValue("n"))
	switch r.FormValue("op") {
	case "meta":
		t.Name, t.Description, t.Active = r.FormValue("name"), r.FormValue("description"), r.FormValue("active") == "on"
		t.AutoCreate, t.AutoChange = r.FormValue("auto_create") == "on", r.FormValue("auto_change") == "on"
		// a smart process is «deal:<its type>»: its cards are deals of its pipelines
		// a list's elements are «list:<the list>»
		var of string
		t.Entity, of, _ = strings.Cut(r.FormValue("entity"), ":")
		t.TypeID, t.ListID = of, ""
		if t.Entity == bp.EntityList {
			t.TypeID, t.ListID = "", of
		}
		t.Fields = parseFields(r, "f", users)
		if len(t.Steps) == 0 { // a new template starts with a notification to the starter, to be changed
			t.Steps = []bp.Step{{Kind: "notify", To: "starter", Text: s.tr(r, "bp.first_step")}}
		}
	case "add":
		days, _ := strconv.Atoi(r.FormValue("days"))
		jump, _ := strconv.Atoi(r.FormValue("goto"))
		st := bp.Step{Kind: r.FormValue("kind"), Text: strings.TrimSpace(r.FormValue("text")), To: r.FormValue("to"),
			Project: r.FormValue("project"), Days: days, Stage: r.FormValue("stage"), URL: strings.TrimSpace(r.FormValue("url"))}
		// the form has the inputs of every kind: a step keeps only its own
		switch st.Kind {
		case "approve", "review", "request":
			st.Users = r.Form["users"]
			if d := r.FormValue("delegate"); d != "sub" {
				st.Delegate = d
			}
			if st.Kind == "approve" {
				st.Mode, st.Goto = r.FormValue("mode"), jump
			}
			if st.Kind == "request" {
				st.Fields, st.Reject = parseFields(r, "rf", users), r.FormValue("reject") == "on"
				if st.Reject {
					st.Goto = jump
				}
			}
		case "wait":
			st.Field = r.FormValue("until")
			if st.Field != "" {
				st.Days = 0
			}
		case "if", "loop":
			st.Field, st.Op, st.Value, st.Goto = r.FormValue("field"), r.FormValue("cmp"), r.FormValue("value"), jump
		case "start": // the name is the step's text: the list of steps shows it
			st.Template, st.Sets = r.FormValue("template"), parseSets(r.FormValue("sets"))
			if o, err := s.BP.Template(r.Context(), st.Template); err == nil {
				st.Text = o.Name
			}
		case "set":
			st.Field, st.Value = r.FormValue("field"), r.FormValue("value")
		case "goto":
			st.Goto = jump
		case "parallel":
			st.Goto = jump
			for _, f := range strings.FieldsFunc(r.FormValue("branches"), func(c rune) bool { return c < '0' || c > '9' }) {
				n, _ := strconv.Atoi(f)
				st.Branches = append(st.Branches, n)
			}
		case "pick":
			st.Users, st.Mode, st.Field, st.Skip = r.Form["users"], r.FormValue("pick_mode"), r.FormValue("pick_field"), r.FormValue("skip") == "on"
			if st.To = ""; st.Skip {
				st.To = r.FormValue("reserve")
			}
		case "chat":
			st.From, st.Users = r.FormValue("from"), r.Form["users"]
		case "post":
			st.From, st.Users, st.Subject = r.FormValue("from"), r.Form["users"], strings.TrimSpace(r.FormValue("subject"))
		case "mail":
			st.From, st.To, st.Email, st.Subject = r.FormValue("from"), r.FormValue("mail_to"), strings.TrimSpace(r.FormValue("email")),
				strings.TrimSpace(r.FormValue("subject"))
		case "comment":
			st.To, st.From = "", r.FormValue("from")
		case "log":
			st.To = ""
		case "document":
			st.To, st.From, st.Doc, st.Sets = "", r.FormValue("from"), r.FormValue("doc"), parseSets(r.FormValue("sets"))
			if d, err := s.CRM.DocTemplate(r.Context(), st.Doc); err == nil {
				st.Text = d.Name
			}
		case "activity":
			st.Deed, st.Note = r.FormValue("deed"), strings.TrimSpace(r.FormValue("note"))
		case "update":
			st.To, st.Sets = "", parseSets(r.FormValue("sets"))
		case "create":
			st.Entity, st.Sets = r.FormValue("card_entity"), parseSets(r.FormValue("sets"))
		case "absence":
			st.Absence, st.Start, st.End = r.FormValue("absence"), r.FormValue("start"), r.FormValue("end")
		case "task":
			st.Users, st.Watchers, st.Note = r.Form["users"], r.Form["watchers"], strings.TrimSpace(r.FormValue("note"))
			st.High, st.Control, st.Wait = r.FormValue("high") == "on", r.FormValue("control") == "on", r.FormValue("wait") == "on"
			for _, line := range strings.Split(r.FormValue("items"), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					st.Items = append(st.Items, line)
				}
			}
		case "event":
			st.Users, st.Start, st.End, st.Note = r.Form["users"], r.FormValue("start"), r.FormValue("end"), strings.TrimSpace(r.FormValue("note"))
		}
		// «перед шагом №»: the steps from it on move a number down, and so do the jumps and the branches to them
		if at, _ := strconv.Atoi(r.FormValue("at")); at >= 1 && at <= len(t.Steps) {
			for i := range t.Steps {
				renumber(&t.Steps[i], func(m int) int {
					if m >= at {
						return m + 1
					}
					return m
				})
			}
			t.Steps = append(t.Steps[:at-1], append([]bp.Step{st}, t.Steps[at-1:]...)...)
		} else {
			t.Steps = append(t.Steps, st)
		}
	case "delete":
		if n >= 0 && n < len(t.Steps) {
			t.Steps = append(t.Steps[:n], t.Steps[n+1:]...)
			for i := range t.Steps { // jumps past the removed step keep pointing at the same steps
				renumber(&t.Steps[i], func(m int) int {
					if m > n+1 {
						return m - 1
					}
					return m
				})
			}
		}
	case "up":
		if n > 0 && n < len(t.Steps) {
			t.Steps[n-1], t.Steps[n] = t.Steps[n], t.Steps[n-1]
			for i := range t.Steps {
				renumber(&t.Steps[i], func(m int) int {
					switch m {
					case n:
						return n + 1
					case n + 1:
						return n
					}
					return m
				})
			}
		}
	default:
		s.taskErr(w, r, i18n.E("err.bad_form"), "/bp/templates")
		return
	}
	back := "/bp/templates/" + id
	nid, err := s.BP.SaveTemplate(r.Context(), sess.UserID, t)
	if err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/bp/templates/"+nid, http.StatusSeeOther)
}

// bpLabels: the fields of «Информация о сотруднике» get their labels in the user's language.
func (s *Server) bpLabels(r *http.Request, list []bp.Field) []bp.Field {
	for i, f := range list {
		if k := bp.UserInfoLabel(f.Key); k != "" && f.Label == "" {
			list[i].Label = s.tr(r, k)
		}
	}
	return list
}

// dateFields are the fields of a process of the kinds: an absence's dates, the end of a pause.
func dateFields(list []bp.Field, kinds ...string) []bp.Field {
	var out []bp.Field
	for _, f := range list {
		if slices.Contains(kinds, f.Kind) {
			out = append(out, f)
		}
	}
	return out
}

// bpStandard: «Стандартные шаблоны» — Отпуск, Командировка, Счёт на оплату, Заявление на выплату with the accountant
// chosen, those not installed yet.
func (s *Server) bpStandard(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	tr := func(key string) string { return s.tr(r, key) }
	n, err := s.BP.InstallStandard(r.Context(), sess.UserID, r.FormValue("accountant"), tr)
	if err != nil {
		http.Redirect(w, r, "/bp/templates?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/bp/templates?msg="+url.QueryEscape(s.tr(r, "bp.std.installed", n)), http.StatusSeeOther)
}

func (s *Server) bpTemplateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := s.BP.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/bp/templates")
		return
	}
	http.Redirect(w, r, "/bp/templates", http.StatusSeeOther)
}

// bpTemplateExport: «Экспорт» — the template as a file to «Импорт» here or on another portal.
func (s *Server) bpTemplateExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	f, err := s.BP.Export(r.Context(), r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/bp/templates")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(f.Name+".bp.json"))
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(f)
}

// bpTemplateImport: «Импорт» of a template's file (file) — a new template (id «new») or over this one.
func (s *Server) bpTemplateImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id, back := r.PathValue("id"), "/bp/templates/"+r.PathValue("id")
	if id == "new" {
		id, back = "", "/bp/templates"
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "err.bp_import_file")), http.StatusSeeOther)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	nid, missing, err := s.BP.Import(r.Context(), sess.UserID, id, data)
	if err != nil {
		http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.errText(r, err)), http.StatusSeeOther)
		return
	}
	msg := s.tr(r, "bp.imported")
	if missing > 0 {
		msg = s.tr(r, "bp.imported_missing", missing)
	}
	http.Redirect(w, r, "/bp/templates/"+nid+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
