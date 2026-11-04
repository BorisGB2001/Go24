package web

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/crm"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/sms"
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
	if err == nil && tab == "approvals" {
		err = s.bpTasksTab(r, sess, data)
	} else if err == nil && tab == "all" {
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

// bpTasksTab is «Задания», as the list: ?state= (waiting, done, all), ?q= — in the process's name or the
// task's text, ?page=; an administrator opens anyone's with ?user=.
func (s *Server) bpTasksTab(r *http.Request, sess auth.Session, data map[string]any) error {
	q := r.URL.Query()
	pg, _ := strconv.Atoi(q.Get("page"))
	pg = max(pg, 0)
	f := bp.TaskFilter{UserID: sess.UserID, State: q.Get("state"), Query: q.Get("q"), Limit: 51, Offset: pg * 50}
	if !slices.Contains(bp.TaskStates, f.State) {
		f.State = ""
	}
	if u := q.Get("user"); u != "" && sess.Admin {
		f.UserID = u
	}
	list, err := s.BP.UserApprovals(r.Context(), f)
	if err != nil {
		return err
	}
	if len(list) > 50 { // one more than a page: there is a next page
		list, data["Next"] = list[:50], pg+1
	}
	back := "/bp?" + url.Values{"tab": {"approvals"}, "state": {f.State}, "q": {f.Query}, "user": {q.Get("user")}, "page": {strconv.Itoa(pg)}}.Encode()
	data["Tasks"], data["State"], data["Q"], data["Page"], data["Back"] = list, f.State, f.Query, pg, back
	data["States"], data["Own"] = bp.TaskStates, f.UserID == sess.UserID
	if !data["Own"].(bool) {
		data["User"] = f.UserID
	}
	return nil
}

// bpTasksBulk: «Групповые действия» of «Задания» — the ticked tasks (task=ID…) approved, rejected, seen (op=approve,
// reject, seen, with the comment) or delegated (op=delegate, to=ID; an administrator — anyone's). A task the op is not
// for (an approval for «Ознакомлен», a request) or the person may not decide is skipped; the page says how many were done.
func (s *Server) bpTasksBulk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp?") {
		back = "/bp?tab=approvals"
	}
	say := func(msg string) { http.Redirect(w, r, back+sepFor(back)+"msg="+url.QueryEscape(msg), http.StatusSeeOther) }
	op, ids, comment := r.FormValue("op"), r.Form["task"], r.FormValue("comment")
	if !slices.Contains([]string{"approve", "reject", "seen", "delegate"}, op) || op == "delegate" && r.FormValue("to") == "" {
		say(s.errText(r, i18n.E("err.bad_form")))
		return
	}
	if len(ids) == 0 {
		say(s.errText(r, i18n.E("err.bp_bulk_none")))
		return
	}
	kinds := map[string]string{}
	if op != "delegate" {
		mine, err := s.BP.MyApprovals(r.Context(), sess.UserID)
		if err != nil {
			say(s.errText(r, err))
			return
		}
		for _, a := range mine {
			kinds[a.ID] = a.Kind
		}
	}
	done, failed := 0, ""
	for _, id := range ids {
		var err error
		switch {
		case op == "delegate":
			_, err = s.BP.Delegate(bpAdmin(r, sess), sess.UserID, id, r.FormValue("to"))
		case kinds[id] == "" || kinds[id] == "request" || (op == "seen") != (kinds[id] == "review"):
			err = i18n.E("err.bp_bulk_kind")
		default:
			err = s.BP.Decide(r.Context(), sess.UserID, id, op, comment)
		}
		if err != nil {
			if failed == "" {
				failed = s.errText(r, err)
			}
			continue
		}
		done++
	}
	s.Log.Info("bp bulk", "user_id", sess.UserID, "op", op, "tasks", len(ids), "done", done)
	msg := s.tr(r, "bp.bulk_done", done, len(ids))
	if failed != "" {
		msg += " · " + failed
	}
	say(msg)
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

// bpCommand: a button of the «Команды» step the process waits on (step, n) pressed, from its page or its card.
func (s *Server) bpCommand(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/bp") && !strings.HasPrefix(back, "/crm/") && !reCardBack.MatchString(back) {
		back = "/bp/i/" + id
	}
	step, _ := strconv.Atoi(r.FormValue("step"))
	n, _ := strconv.Atoi(r.FormValue("n"))
	if err := s.BP.Command(bpAdmin(r, sess), sess.UserID, id, step, n); err != nil {
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
		values[f.Key] = bpFormValue(r, f)
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

// bpFormValue is the value of a field of the form «bpfield»: f_<key>, or each of m_<key> of a multiple field.
func bpFormValue(r *http.Request, f bp.Field) string {
	if f.Multiple {
		return strings.Join(r.Form["m_"+f.Key], bp.Multi)
	}
	return r.FormValue("f_" + f.Key)
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
		"Mine": mine, "Fields": s.bpLabels(r, x.Form(t.Fields)), "Card": card, "Users": users, "CanStop": s.BP.CanStop(bpAdmin(r, sess), sess.UserID, x),
		"Commands": s.BP.Commands(bpAdmin(r, sess), sess.UserID, x)}})
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
			if key, ok := strings.CutPrefix(k, "m_"); ok { // a multiple field
				values[key] = strings.Join(v, bp.Multi)
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

// bpGlobals is «Глобальные константы и переменные» of the designer: the administrator keeps the company's values
// every template reads as {global.KEY}.
func (s *Server) bpGlobals(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/templates")
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		list := parseFields(r, "g", users)
		for i := range list {
			if list[i].Scope != "var" {
				list[i].Scope = "const"
			}
			list[i].Required = false
		}
		if err := s.BP.SaveGlobals(r.Context(), sess.UserID, list); err != nil {
			s.taskErr(w, r, err, "/bp/globals")
			return
		}
		http.Redirect(w, r, "/bp/globals", http.StatusSeeOther)
		return
	}
	list, err := s.BP.Globals(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/bp/templates")
		return
	}
	s.render(w, r, "bpglobals", page{TitleKey: "bp.globals", Nav: "bp", Data: map[string]any{"Globals": list,
		"Rows": bpFieldRows(list, users), "FieldKinds": bp.FieldKinds, "Scopes": []string{"const", "var"}}})
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
		docs, _ = s.CRM.ActiveDocTemplates(r.Context(), crm.DocKind(t.Entity, t.TypeID))
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
	// «Смена воронки», «Копирование сделки»: the pipelines of the template's deals (a smart process's), or else of deals
	dealPipes, _ := s.CRM.PipelinesOf(r.Context(), map[bool]string{true: t.TypeID}[t.Entity == "deal"])
	// «Добавление товара»: the products of the catalog on sale
	products, _ := s.CRM.ProductsOf(r.Context(), "", "", false)
	var userFields []tasks.User                    // the steps' «field:KEY» people
	globals, err := s.BP.GlobalFields(r.Context()) // «Глобальные константы и переменные»: as fields of any template
	if err != nil {
		s.taskErr(w, r, err, "/bp")
		return
	}
	for i := range globals {
		globals[i].Label = s.tr(r, "bp.global_field", globals[i].Label)
	}
	fields := append(s.bpLabels(r, bp.AllFields(t.Fields, t.Steps)), globals...)
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
	// «Создать»: a deal or a smart process's element in a stage of their pipelines (the smart process's name before its own)
	createPipes, _ := s.CRM.Pipelines(r.Context())
	for i, p := range createPipes {
		createPipes[i].Name = cmp.Or(p.Name, s.tr(r, "crm.pipeline."+p.Code))
	}
	for _, typ := range types {
		pipes, _ := s.CRM.PipelinesOf(r.Context(), typ.ID)
		for _, p := range pipes {
			p.Name = typ.Name + ": " + cmp.Or(p.Name, s.tr(r, "crm.pipeline."+p.Code))
			createPipes = append(createPipes, p)
		}
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
	var senders []sms.Sender // «Отправка SMS»: the providers to choose from
	if s.SMS != nil {
		senders, _ = s.SMS.Senders(r.Context())
	}
	var actions []apps.Activity // the actions of applications (bizproc.activity.add)
	if s.Apps != nil {
		actions, _ = s.Apps.Activities(r.Context())
	}
	// «Изменить» (?step=N from 1): the form of the step's kind with its values instead of a new step's
	edit, values, rows := 0, bpNewStepValues(t), bpFieldRows(nil, users)
	if n, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && n >= 1 && n <= len(t.Steps) {
		edit, values, rows = n, bpStepValues(t.Steps[n-1]), bpFieldRows(t.Steps[n-1].Fields, users)
	}
	s.render(w, r, "bptemplate", page{Title: t.Name, Nav: "bp", Data: map[string]any{"T": t, "Edit": edit, "V": values, "Conds": bpConds(values), "MaxConds": bp.MaxConds - 1, "Senders": senders, "Actions": actions, "WorkdayActs": bp.WorkdayActs, "RandomChars": bp.RandomChars, "MaxRandom": bp.MaxRandom, "Starters": starters, "FieldRows": bpFieldRows(t.Fields, users), "NewRows": rows, "FieldKinds": bp.FieldKinds, "UserFields": userFields, "Types": types, "Lists": lists, "List": list,
		"AllFields": fields, "Globals": globals, "DateFields": dateFields(fields, "date"), "TimeFields": dateFields(fields, "date", "datetime"), "AbsenceKinds": s.absenceKeys(r.Context()), "Kinds": bp.StepKinds, "CRMCard": crm.Entities[t.Entity], "DealPipes": dealPipes, "CreatePipes": createPipes, "Products": products, "ObserverModes": crm.ObserverModes, "ConvertInto": bp.ConvertInto, "CardFields": bp.CardFields, "OwnFields": ownFields, "Modes": bp.Modes, "PickModes": bp.PickModes, "Delegations": bp.Delegations, "Deeds": crm.ActivityKinds, "Docs": docs, "Scopes": bp.FieldScopes, "Others": others, "MaxLoops": bp.MaxLoops, "MaxCommands": bp.MaxCommands, "Ops": bp.Ops, "Users": users, "Projects": projects, "Stages": stages,
		"Scheme": bp.Scheme(t.Steps), "Gap": bpGap(r)}})
}

// bpFieldRow is a row of the fields' editor (template "bpfields"): the options joined, a user's default as a name.
type bpFieldRow struct {
	Key, Label, Kind, Options, Default, Scope string
	Required, Multiple                        bool
}

// bpFieldRows are the rows of the fields and an empty one to add a field.
func bpFieldRows(fields []bp.Field, users []tasks.User) []bpFieldRow {
	var out []bpFieldRow
	for _, f := range fields {
		row := bpFieldRow{Key: f.Key, Label: f.Label, Kind: f.Kind, Options: strings.Join(f.Options, "; "), Default: f.Default, Required: f.Required, Scope: f.Scope,
			Multiple: f.Multiple}
		defs := f.List(f.Default)
		if f.Kind == "user" {
			for i, d := range defs {
				for _, u := range users {
					if u.ID == d {
						defs[i] = u.Name
					}
				}
			}
		}
		row.Default = strings.Join(defs, "; ")
		out = append(out, row)
	}
	return append(out, bpFieldRow{Kind: "string"})
}

// parseFields reads the rows of the fields' editor with the prefix: a row without a key is left out; the options of
// a select are split by «;», a user's default is an employee's name, e-mail or id.
// bpConds reads the rows of «Добавить условие» of an «if» or a loop step, the conditions after its own; a row without
// a field is none.
func bpConds(v url.Values) []bp.Cond {
	var out []bp.Cond
	ors, cmps, vals := v["more_join"], v["more_cmp"], v["more_value"]
	for i, f := range v["more_field"] {
		if f != "" && i < len(ors) && i < len(cmps) && i < len(vals) {
			out = append(out, bp.Cond{Or: ors[i] == "or", Field: f, Op: cmps[i], Value: vals[i]})
		}
	}
	return out
}

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
		f := bp.Field{Key: key, Label: at("label", i), Kind: at("kind", i), Required: at("req", i) == "1", Default: at("def", i), Scope: at("scope", i),
			Multiple: at("multi", i) == "1"}
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
		defs := []string{f.Default}
		if f.Multiple { // the defaults parted by «;»
			defs = strings.Split(f.Default, ";")
		}
		for j, name := range defs {
			defs[j] = strings.TrimSpace(name)
			for _, u := range users {
				if f.Kind == "user" && (u.ID == defs[j] || strings.EqualFold(u.Name, defs[j]) || u.Email != "" && strings.EqualFold(u.Email, defs[j])) {
					defs[j] = u.ID
				}
			}
		}
		f.Default = strings.Join(defs, bp.Multi)
		out = append(out, f)
	}
	return out
}

// bpGap is the place of the scheme of the form: at, from, br.
func bpGap(r *http.Request) bp.Gap {
	at, _ := strconv.Atoi(r.FormValue("at"))
	from, _ := strconv.Atoi(r.FormValue("from"))
	br, _ := strconv.Atoi(r.FormValue("br"))
	return bp.Gap{At: at, From: from, Br: br}
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

// bpStepFromForm reads a step of the designer's form (op=add, op=edit): the form has the inputs of every kind, a step
// keeps only its own.
func (s *Server) bpStepFromForm(r *http.Request, sess auth.Session, users []tasks.User) bp.Step {
	days, _ := strconv.Atoi(r.FormValue("days"))
	jump, _ := strconv.Atoi(r.FormValue("goto"))
	st := bp.Step{Kind: r.FormValue("kind"), Text: strings.TrimSpace(r.FormValue("text")), To: r.FormValue("to"),
		Project: r.FormValue("project"), Days: days, Stage: r.FormValue("stage"), URL: strings.TrimSpace(r.FormValue("url"))}
	if id, ok := strings.CutPrefix(st.Kind, "app:"); ok { // an application's action: «app:<its id>»
		st.Kind, st.App = "app", id
	}
	// the form has the inputs of every kind: a step keeps only its own
	switch st.Kind {
	case "approve", "review", "request":
		st.Users = r.Form["users"]
		if d := r.FormValue("delegate"); d != "sub" {
			st.Delegate = d
		}
		if st.Kind == "approve" {
			st.Mode, st.Goto = r.FormValue("mode"), jump
			if st.Mode == "vote" {
				st.Percent, _ = strconv.Atoi(r.FormValue("percent"))
				st.WaitAll = r.FormValue("wait_all") == "on"
			}
		}
		// «Время на выполнение»: a number of minutes, hours or days
		t, _ := strconv.Atoi(r.FormValue("timeout"))
		st.Timeout = t * map[string]int{"min": 1, "hour": 60, "day": 24 * 60}[r.FormValue("timeout_unit")]
		st.Remark, st.Yes = r.FormValue("remark"), strings.TrimSpace(r.FormValue("yes"))
		if st.Kind != "review" {
			st.No = strings.TrimSpace(r.FormValue("no"))
		}
		if st.Kind == "request" {
			st.Fields, st.Reject = parseFields(r, "rf", users), r.FormValue("reject") == "on"
			if st.Reject {
				st.Goto = jump
			}
		}
	case "wait": // a number of minutes, hours or days, or till the date of a field
		st.Days, st.Field, st.WorkTime = 0, r.FormValue("until"), r.FormValue("worktime") == "on"
		if st.Field == "" {
			t, _ := strconv.Atoi(r.FormValue("timeout"))
			st.Timeout = t * map[string]int{"min": 1, "hour": 60, "day": 24 * 60}[r.FormValue("timeout_unit")]
		}
	case "if", "loop":
		st.Field, st.Op, st.Value, st.Goto = r.FormValue("field"), r.FormValue("cmp"), r.FormValue("value"), jump
		st.More = bpConds(r.Form)
	case "start": // the name is the step's text: the list of steps shows it
		st.Template, st.Sets = r.FormValue("template"), parseSets(r.FormValue("sets"))
		if o, err := s.BP.Template(r.Context(), st.Template); err == nil {
			st.Text = o.Name
		}
	case "set":
		st.Field, st.Value = r.FormValue("field"), r.FormValue("value")
	case "goto":
		st.Goto = jump
	case "commands": // «Название = шаг №» a line, no step — the next one
		st.Users = r.Form["users"]
		for _, a := range parseSets(r.FormValue("commands")) {
			n, _ := strconv.Atoi(a.Value)
			st.Commands = append(st.Commands, bp.Command{Title: a.Field, Goto: n})
		}
		// «Ожидать не дольше» and where to when no one pressed, as «Обработчик событий»
		t, _ := strconv.Atoi(r.FormValue("timeout"))
		st.Timeout = t * map[string]int{"min": 1, "hour": 60, "day": 24 * 60}[r.FormValue("timeout_unit")]
		if st.Timeout > 0 {
			st.Goto, _ = strconv.Atoi(r.FormValue("commands_goto"))
		}
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
	case "create": // a kind of card, or «type:ID» — an element of the smart process; a deal's stage ("" — the first one)
		st.Entity, st.Sets = r.FormValue("card_entity"), parseSets(r.FormValue("sets"))
		if typ, ok := strings.CutPrefix(st.Entity, "type:"); ok {
			st.Entity, st.Type = "deal", typ
		}
		if st.Entity == "deal" {
			st.Stage = r.FormValue("card_stage")
		}
	case "crmupdate":
		st.To, st.Value, st.Entity, st.Sets = "", strings.TrimSpace(r.FormValue("value")), r.FormValue("crm_entity"), parseSets(r.FormValue("sets"))
	case "crmread", "crmdelete", "crmconvert", "crmmove", "crmcopy", "crmobservers", "crmclose", "crmresponsible", "crmproduct": // the card: of a kind ("" — the process's), a link or an id ("" — the process's card)
		st.To, st.Value = "", strings.TrimSpace(r.FormValue("value"))
		switch st.Kind {
		case "crmconvert":
			st.Into = r.Form["into"]
		case "crmmove", "crmcopy": // the stage's name is the step's text: the list of steps shows it
			st.Stage, st.Text = r.FormValue("crm_stage"), s.bpStageName(r, r.FormValue("crm_stage"))
			if st.Kind == "crmcopy" {
				st.Sets = parseSets(r.FormValue("sets"))
			}
		default:
			st.Entity = r.FormValue("crm_entity")
		}
		if st.Kind == "crmobservers" {
			st.Users, st.Mode = r.Form["users"], r.FormValue("obs_mode")
		}
		if st.Kind == "crmclose" {
			st.Deed = r.FormValue("close_deed")
		}
		if st.Kind == "crmresponsible" { // as «Выбор сотрудника»: of whom, how, the absent skipped and the reserve
			st.Users, st.Mode, st.Skip = r.Form["users"], r.FormValue("pick_mode"), r.FormValue("skip") == "on"
			if st.Skip {
				st.To = r.FormValue("reserve")
			}
		}
		if st.Kind == "crmproduct" {
			st.Product, st.Price = r.FormValue("product"), strings.TrimSpace(r.FormValue("price"))
			if p, err := s.CRM.Product(r.Context(), st.Product); err == nil { // the product's name is the step's text: the list of steps shows it
				st.Text = p.Name
			}
			st.Quantity, st.Discount = strings.TrimSpace(r.FormValue("quantity")), strings.TrimSpace(r.FormValue("discount"))
		}
	case "diskfolder", "diskcopy", "diskread", "diskdelete": // «Диск»: of From; a drive and a folder's path, a file or a folder
		st.To, st.From, st.Value = "", r.FormValue("from"), strings.TrimSpace(r.FormValue("value"))
		if st.Kind == "diskfolder" || st.Kind == "diskcopy" {
			st.Drive, st.Folder = r.FormValue("drive"), strings.TrimSpace(r.FormValue("folder"))
		}
		if st.Kind == "diskfolder" {
			st.Value = ""
		}
		st.Move = st.Kind == "diskcopy" && r.FormValue("move") == "1"
	case "listadd", "listupdate", "listread", "listdelete": // the list's name is the step's text: the list of steps shows it
		st.To, st.List, st.Value = "", r.FormValue("list"), strings.TrimSpace(r.FormValue("value"))
		if st.Kind == "listadd" || st.Kind == "listupdate" {
			st.Sets = parseSets(r.FormValue("sets"))
		}
		if l, err := s.BP.List(bpAdmin(r, sess), sess.UserID, st.List); err == nil {
			st.Text = l.Name
		}
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
	case "sms": // the phones ("" — the client's of the card), a provider ("" — the default one)
		st.To, st.Value, st.Sender = "", strings.TrimSpace(r.FormValue("value")), r.FormValue("sender")
	case "workday":
		st.Act = r.FormValue("act")
		if st.Act != "finish" {
			st.Text = ""
		}
	case "random":
		st.To, st.Field, st.Chars = "", r.FormValue("random_field"), r.Form["chars"]
		st.Size, _ = strconv.Atoi(r.FormValue("size"))
	case "foreach":
		st.To, st.Field, st.Item = "", r.FormValue("each_field"), r.FormValue("each_item")
	case "app": // its properties (ap_<key>) and the time to wait for the answer
		st.To, st.Text = "", ""
		if s.Apps != nil {
			if act, err := s.Apps.Activity(r.Context(), st.App); err == nil {
				for _, p := range act.Props {
					if v := strings.TrimSpace(r.FormValue("ap_" + p.Key)); v != "" {
						st.Sets = append(st.Sets, bp.Assign{Field: p.Key, Value: v})
					}
				}
			}
		}
		t, _ := strconv.Atoi(r.FormValue("timeout"))
		st.Timeout = t * map[string]int{"min": 1, "hour": 60, "day": 24 * 60}[r.FormValue("timeout_unit")]
	}
	return st
}

// bpStepValues are the inputs of the designer's form a step fills in, as bpStepFromForm reads them: «Изменить» shows
// the step this way. The names the kinds share hold the step's one value — only the inputs of its kind are sent.
func bpStepValues(st bp.Step) url.Values {
	v := url.Values{}
	if st.Kind == "wait" { // a pause of an old template in days — in the units of the form
		st.Timeout, st.Days = st.Timeout+st.Days*24*60, 0
	}
	set := func(name, value string) {
		if value != "" {
			v.Set(name, value)
		}
	}
	on := func(name string, b bool) {
		if b {
			v.Set(name, "on")
		}
	}
	sets := func(list []bp.Assign) string {
		var b strings.Builder
		for _, a := range list {
			fmt.Fprintf(&b, "%s = %s\n", a.Field, a.Value)
		}
		return b.String()
	}
	v.Set("kind", st.Kind)
	if st.Kind == "app" {
		v.Set("kind", "app:"+st.App)
		for _, a := range st.Sets {
			set("ap_"+a.Field, a.Value)
		}
	}
	set("text", st.Text)
	set("to", st.To)
	set("project", st.Project)
	v.Set("days", strconv.Itoa(st.Days))
	set("stage", st.Stage)
	set("url", st.URL)
	v["users"], v["watchers"], v["into"], v["chars"] = st.Users, st.Watchers, st.Into, st.Chars
	v.Set("delegate", cmp.Or(st.Delegate, "sub"))
	set("mode", st.Mode)
	set("pick_mode", st.Mode)
	v.Set("percent", strconv.Itoa(cmp.Or(st.Percent, 50)))
	on("wait_all", st.WaitAll)
	// «Время на выполнение» in the largest unit it is a whole number of
	v.Set("timeout_unit", "hour")
	if st.Timeout > 0 {
		n, unit := st.Timeout, "min"
		if n%(24*60) == 0 {
			n, unit = n/(24*60), "day"
		} else if n%60 == 0 {
			n, unit = n/60, "hour"
		}
		v.Set("timeout", strconv.Itoa(n))
		v.Set("timeout_unit", unit)
	}
	set("remark", st.Remark)
	set("yes", st.Yes)
	set("no", st.No)
	on("reject", st.Reject)
	set("field", st.Field)
	set("cmp", st.Op)
	set("value", st.Value)
	for _, c := range st.More {
		v.Add("more_join", map[bool]string{false: "and", true: "or"}[c.Or])
		v.Add("more_field", c.Field)
		v.Add("more_cmp", c.Op)
		v.Add("more_value", c.Value)
	}
	v.Set("goto", strconv.Itoa(st.Goto))
	v.Set("commands_goto", strconv.Itoa(st.Goto))
	var branches []string
	for _, b := range st.Branches {
		branches = append(branches, strconv.Itoa(b))
	}
	set("branches", strings.Join(branches, ", "))
	var commands strings.Builder
	for _, c := range st.Commands {
		commands.WriteString(c.Title)
		if c.Goto != 0 {
			fmt.Fprintf(&commands, " = %d", c.Goto)
		}
		commands.WriteString("\n")
	}
	set("commands", commands.String())
	set("template", st.Template)
	set("sets", sets(st.Sets))
	set("from", st.From)
	set("mail_to", st.To)
	set("email", st.Email)
	set("subject", st.Subject)
	set("doc", st.Doc)
	set("deed", st.Deed)
	set("note", st.Note)
	set("card_entity", st.Entity)
	if st.Type != "" {
		set("card_entity", "type:"+st.Type)
	}
	set("card_stage", st.Stage)
	set("crm_entity", st.Entity)
	set("crm_stage", st.Stage)
	set("obs_mode", st.Mode)
	set("close_deed", st.Deed)
	set("product", st.Product)
	set("price", st.Price)
	set("quantity", st.Quantity)
	set("discount", st.Discount)
	set("drive", st.Drive)
	set("folder", st.Folder)
	if st.Move {
		v.Set("move", "1")
	}
	set("list", st.List)
	set("absence", st.Absence)
	set("start", st.Start)
	set("end", st.End)
	on("high", st.High)
	on("control", st.Control)
	on("wait", st.Wait)
	on("worktime", st.WorkTime)
	set("items", strings.Join(st.Items, "\n"))
	set("sender", st.Sender)
	set("act", st.Act)
	set("random_field", st.Field)
	v.Set("size", strconv.Itoa(cmp.Or(st.Size, 8)))
	set("each_field", st.Field)
	set("each_item", st.Item)
	set("until", st.Field)
	set("pick_field", st.Field)
	on("skip", st.Skip)
	set("reserve", st.To)
	return v
}

// bpNewStepValues are the inputs of the form to add a step: the starter does it, a card's process mails its client.
func bpNewStepValues(t bp.Template) url.Values {
	v := url.Values{"to": {"starter"}, "from": {"starter"}, "days": {"0"}, "goto": {"0"}, "commands_goto": {"0"}, "delegate": {"sub"},
		"percent": {"50"}, "timeout_unit": {"hour"}, "size": {"8"}, "skip": {"on"}}
	for _, c := range bp.RandomChars {
		if c != "signs" {
			v.Add("chars", c)
		}
	}
	if crm.Entities[t.Entity] {
		v.Set("mail_to", "client")
	}
	return v
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
		st := s.bpStepFromForm(r, sess, users)
		// «перед шагом №» or a «+» of the scheme: the steps from it on move a number down, and so do the jumps and the
		// branches to them (but the jump of the scheme's branch to its top)
		g := bpGap(r)
		if g.At < 1 || g.At > len(t.Steps) {
			g = bp.Gap{At: len(t.Steps) + 1}
		}
		t.Steps = bp.Insert(t.Steps, st, g)
		at := g.At
		if st.Kind == "foreach" { // its steps are put before the «Перейти к шагу» back to it, added with it
			for i := range t.Steps {
				bp.Renumber(&t.Steps[i], func(m int) int {
					if m > at {
						return m + 1
					}
					return m
				})
			}
			t.Steps = slices.Insert(t.Steps, at, bp.Step{Kind: "goto", Goto: at})
		}
	case "edit": // «Изменить»: the step's properties anew, of its own kind; its number and the jumps to it stay
		if n >= 0 && n < len(t.Steps) {
			if st := s.bpStepFromForm(r, sess, users); st.Kind == t.Steps[n].Kind && st.App == t.Steps[n].App {
				t.Steps[n] = st
			}
		}
	case "delete":
		if n >= 0 && n < len(t.Steps) {
			del := []int{n}
			if t.Steps[n].Kind == "foreach" { // an iterator goes with its «Перейти к шагу» back to it
				if e := bp.EachEnd(t.Steps, n); e > 0 {
					del = []int{e - 1, n}
				}
			}
			for _, n := range del {
				t.Steps = append(t.Steps[:n], t.Steps[n+1:]...)
				for i := range t.Steps { // jumps past the removed step keep pointing at the same steps
					bp.Renumber(&t.Steps[i], func(m int) int {
						if m > n+1 {
							return m - 1
						}
						return m
					})
				}
			}
		}
	case "move": // a block of the scheme (or a step of the list) dragged to a gap
		a, _ := strconv.Atoi(r.FormValue("a"))
		b, _ := strconv.Atoi(r.FormValue("b"))
		t.Steps, _ = bp.Move(t.Steps, a, b, bpGap(r))
	case "copy": // «Копировать»: the block's copy right after it
		a, _ := strconv.Atoi(r.FormValue("a"))
		b, _ := strconv.Atoi(r.FormValue("b"))
		t.Steps = bp.Copy(t.Steps, a, b)
	case "up":
		if n > 0 && n < len(t.Steps) {
			t.Steps[n-1], t.Steps[n] = t.Steps[n], t.Steps[n-1]
			for i := range t.Steps {
				bp.Renumber(&t.Steps[i], func(m int) int {
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

// bpStageName is «pipeline: stage» of a deals' stage (of a smart process's too), "" — not one.
func (s *Server) bpStageName(r *http.Request, id string) string {
	types, _ := s.CRM.Types(r.Context())
	of := []string{""}
	for _, t := range types {
		of = append(of, t.ID)
	}
	for _, typeID := range of {
		pipes, _ := s.CRM.PipelinesOf(r.Context(), typeID)
		for _, p := range pipes {
			for _, st := range p.Stages {
				if st.ID == id {
					return cmp.Or(p.Name, s.tr(r, "crm.pipeline."+p.Code)) + ": " + cmp.Or(st.Name, s.tr(r, "crm.stage."+st.Code))
				}
			}
		}
	}
	return ""
}
