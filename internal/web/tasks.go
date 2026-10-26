package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
	"go24/internal/worktime"
)

// myRoles are the «Мои задачи» tabs.
var myRoles = []string{"responsible", "accomplice", "creator", "auditor"}

type myTab struct {
	Role  string
	Count int
}

func (s *Server) myTasks(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	role := r.URL.Query().Get("role")
	if role == "" {
		role = "responsible"
	}
	showAll := r.URL.Query().Get("all") == "1"
	subs, err := s.Tasks.Subordinates(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	// ?as=<user>: the tasks of someone the user acts for («исполняющий обязанности» with the power to see them),
	// or of a subordinate — then only what the manager may see of them (a collab of theirs stays closed)
	uid, asName, viewer, titleKey := sess.UserID, "", "", "acting.tasks_for"
	if as := r.URL.Query().Get("as"); as != "" {
		people, err := s.Company.ActingFor(r.Context(), sess.UserID, "view")
		if err != nil {
			s.taskErr(w, r, err, "/my")
			return
		}
		for _, p := range people {
			if p.ID == as {
				uid, asName = p.ID, p.Name
			}
		}
		for _, p := range subs {
			if p.ID == as && asName == "" {
				uid, asName, viewer, titleKey = p.ID, p.Name, sess.UserID, "my.tasks_of"
			}
		}
		if asName == "" {
			s.taskErr(w, r, tasks.ErrForbidden, "/my")
			return
		}
	}

	roles := myRoles
	if len(subs) > 0 && asName == "" {
		roles = append(roles[:len(roles):len(roles)], "subordinates")
	}
	var tabs []myTab
	for _, rl := range roles {
		open, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{UserID: uid, Role: rl, OnlyOpen: true, Viewer: viewer})
		if err != nil {
			s.taskErr(w, r, err, "/my")
			return
		}
		tabs = append(tabs, myTab{Role: rl, Count: len(open)})
	}
	// ?view= is where the sidebar counters lead: exactly the tasks they count.
	view := r.URL.Query().Get("view")
	f := tasks.Filter{UserID: uid, Role: role, OnlyOpen: !showAll}
	switch view {
	case "open":
		f = tasks.Filter{UserID: uid, Doing: true, OnlyOpen: true}
	case "overdue":
		f = tasks.Filter{UserID: uid, Doing: true, Overdue: true}
	case "control":
		f = tasks.Filter{CreatorID: uid, Status: tasks.StatusAwaitingControl}
	case "comments": // the green counter: tasks with messages not seen yet
		f = tasks.Filter{UserID: uid, NewComments: true}
	case "favorites":
		f = tasks.Filter{UserID: uid, Favorites: true}
	default:
		view = ""
	}
	f.Viewer = viewer
	var proj *tasks.Project // ?project= narrows a view to one project: the counters of a collab's card lead here
	if key := r.URL.Query().Get("project"); key != "" && view != "" {
		p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, key)
		if err != nil {
			s.taskErr(w, r, err, "/my")
			return
		}
		f.ProjectID, proj = p.ID, &p
	}
	list, err := s.Tasks.ListTasks(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	nav := "my"
	if view == "control" || (view == "" && role == "creator") {
		nav = "control"
	}
	pg := page{TitleKey: "title.my_tasks", Nav: nav, Data: map[string]any{
		"Role": role, "Tabs": tabs, "Tasks": list, "ShowAll": showAll, "View": view, "As": uid, "AsName": asName,
		"Bulk": newBulkBar(r, users), "Subordinates": subs, "TitleKey": titleKey, "Project": proj,
	}}
	if asName != "" {
		pg.TitleKey, pg.Title, pg.Nav = "", s.tr(r, titleKey, asName), "as:"+uid
		if viewer != "" {
			pg.Nav = "my"
		}
	}
	s.render(w, r, "my", pg)
}

// allTasks: every task the user may see (in admin mode — every task in go24), with the list filter and presets.
func (s *Server) allTasks(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	showAll := q.Get("all") == "1"
	f, project, err := s.listFilter(r, sess)
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	list, err := s.Tasks.ListTasks(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	presets, err := s.Tasks.Presets(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	query := filterQuery(q)
	filtered := false
	for k := range query {
		filtered = filtered || (k != "project" && k != "all")
	}
	s.render(w, r, "tasks", page{TitleKey: "title.all_tasks", Nav: "tasks", Data: map[string]any{
		"Tasks": list, "ShowAll": showAll, "Projects": projects, "Project": project, "Limit": tasks.ListLimit,
		"Q": q, "Query": query.Encode(), "Filtered": filtered, "Users": users, "Presets": presets, "BuiltIn": builtInPresets,
		"Statuses": []tasks.Status{tasks.StatusPending, tasks.StatusInProgress, tasks.StatusAwaitingControl, tasks.StatusDeferred, tasks.StatusCompleted},
		"Periods":  tasks.DeadlinePeriods, "Bulk": newBulkBar(r, users),
		"CanExport": s.taskPerm(r, sess, "export"), "CanImport": s.taskPerm(r, sess, "import"),
	}})
}

// listFilter is the filter of the task list from the query (?project=, ?q=, ?status= … as filterKeys) and the project
// it is limited to.
func (s *Server) listFilter(r *http.Request, sess auth.Session) (tasks.Filter, tasks.Project, error) {
	q := r.URL.Query()
	f := tasks.Filter{Viewer: sess.UserID, OnlyOpen: q.Get("all") != "1"}
	var project tasks.Project
	if key := q.Get("project"); key != "" {
		p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, key)
		if err != nil {
			return f, project, err
		}
		project, f.ProjectID = p, p.ID
	}
	me := func(v string) string { // the presets say "me" for whoever opens them
		if v == "me" {
			return sess.UserID
		}
		return v
	}
	f.Search, f.Tag, f.Period, f.HighPriority = q.Get("q"), q.Get("tag"), q.Get("period"), q.Get("prio") == "high"
	f.ResponsibleID, f.CreatorID, f.MemberID = me(q.Get("responsible")), me(q.Get("creator")), me(q.Get("member"))
	for _, st := range q["status"] {
		f.Statuses = append(f.Statuses, tasks.Status(st))
	}
	if len(f.Statuses) > 0 { // a status filter decides about completed tasks itself
		f.OnlyOpen = false
	}
	return f, project, nil
}

// filterKeys are the parameters of the list filter; a preset keeps exactly these.
var filterKeys = []string{"q", "status", "responsible", "creator", "member", "period", "tag", "prio", "project", "all"}

// filterQuery is the filter part of the request query, without empty values.
func filterQuery(q url.Values) url.Values {
	out := url.Values{}
	for _, k := range filterKeys {
		for _, v := range q[k] {
			if v != "" {
				out.Add(k, v)
			}
		}
	}
	return out
}

// builtInPresets are the presets every list has: name key → query.
var builtInPresets = []struct{ Key, Query string }{
	{"in_work", "status=in_progress"},
	{"overdue", "period=overdue"},
	{"mine", "member=me"},
	{"assigned", "creator=me"},
	{"important", "prio=high"},
}

// presetSave keeps the current filter under a name; presetDelete removes a preset.
func (s *Server) presetSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q, _ := url.ParseQuery(r.FormValue("query"))
	query := filterQuery(q).Encode()
	if err := s.Tasks.SavePreset(r.Context(), sess.UserID, r.FormValue("name"), query); err != nil {
		s.taskErr(w, r, err, "/tasks?"+query)
		return
	}
	http.Redirect(w, r, "/tasks?"+query, http.StatusSeeOther)
}

func (s *Server) presetDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Tasks.DeletePreset(r.Context(), sess.UserID, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/tasks")
		return
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

func (s *Server) projectSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil && r.FormValue("kind") != "" {
		err = s.Tasks.SetProjectKind(r.Context(), p, r.FormValue("kind"))
	}
	if err == nil && r.FormValue("visibility") != "" {
		err = s.Tasks.SetVisibility(r.Context(), p, r.FormValue("visibility"))
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("project settings", "user_id", sess.UserID, "project", p.Key, "visibility", r.FormValue("visibility"), "kind", r.FormValue("kind"))
	s.Matrix.Wake()
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}

// search jumps straight to a task by key (GO-12) or lists tasks matching the text.
func (s *Server) search(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if _, _, err := tasks.ParseKey(q); err == nil {
		// only a task one may see: a key of someone else's collab or project does not even say it exists
		if id, err := s.Tasks.TaskIDByKey(r.Context(), q); err == nil && s.Tasks.CheckVisible(r.Context(), sess.UserID, id) == nil {
			http.Redirect(w, r, "/t/"+strings.ToUpper(q), http.StatusFound)
			return
		}
	}
	list, err := s.Tasks.Search(r.Context(), sess.UserID, q)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "search", page{TitleKey: "title.search", Query: q, Data: list})
}

// projects: the user's projects, or (?archived=1) those in the archive.
func (s *Server) projects(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	archived := r.URL.Query().Get("archived") == "1"
	list, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if archived {
		list, err = s.Tasks.ArchivedProjects(r.Context(), sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	list = slices.DeleteFunc(list, tasks.Project.Collab) // collabs have their own section
	invites, err := s.Tasks.MyInvites(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	s.render(w, r, "projects", page{TitleKey: "title.projects", Nav: "projects", Data: map[string]any{
		"List": list, "Archived": archived, "Invites": invites}})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	key, err := s.Tasks.CreateProject(r.Context(), sess.UserID,
		r.FormValue("key"), r.FormValue("name"), r.FormValue("description"))
	if kind := r.FormValue("kind"); err == nil && kind != "" && kind != "group" {
		var p tasks.Project
		if p, err = s.Tasks.ProjectByKey(r.Context(), sess.UserID, key); err == nil {
			err = s.Tasks.SetProjectKind(r.Context(), p, kind)
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	s.Matrix.Wake()
	http.Redirect(w, r, "/p/"+key, http.StatusSeeOther)
}

func (s *Server) project(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	showAll := r.URL.Query().Get("all") == "1"
	list, err := s.Tasks.ListTasks(r.Context(), tasks.Filter{ProjectID: p.ID, OnlyOpen: !showAll, Viewer: sess.UserID})
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	members, err := s.Tasks.Members(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	canInvite, err := s.Tasks.CanInvite(r.Context(), p)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	invites, err := s.Tasks.ProjectInvites(r.Context(), p.ID)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	invitePerm, err := s.Tasks.InvitePerm(r.Context(), p)
	if err != nil {
		s.taskErr(w, r, err, "/projects")
		return
	}
	data := map[string]any{
		"CanInvite": canInvite, "Invites": invites, "InvitePerm": invitePerm,
		"Project": p, "Tasks": list, "Members": members, "Users": users, "ShowAll": showAll, "Me": sess.UserID,
		"DefaultDeadline": sched.DefaultDeadline(time.Now()),
	}
	s.collabData(r, p, data)
	s.render(w, r, "project", page{Title: p.Name, Nav: projectNav(p), Data: data})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	deadline, err := parseDeadline(r.FormValue("deadline"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	prio, _ := strconv.Atoi(r.FormValue("priority"))
	tpl := templateID(r, sess.UserID, s)
	key, err := s.Tasks.CreateTask(r.Context(), sess.UserID, p, tasks.NewTask{
		Title:               r.FormValue("title"),
		Description:         r.FormValue("description"),
		ResponsibleID:       r.FormValue("responsible"),
		Priority:            tasks.Priority(prio),
		Deadline:            deadline,
		RequiresControl:     r.FormValue("requires_control") == "on",
		AllowChangeDeadline: r.FormValue("allow_change_deadline") == "on",
		MatchWorkTime:       r.FormValue("match_work_time") == "on",
		NoEfficiency:        r.FormValue("no_efficiency") == "on",
		Params:              formParams(r),
		ParentKey:           strings.TrimSpace(r.FormValue("parent")),
		Accomplices:         r.Form["accomplices"],
		Auditors:            r.Form["auditors"],
		StageID:             r.FormValue("stage"),
		Checklist:           strings.Split(r.FormValue("checklist"), "\n"),
		TemplateID:          tpl,
	})
	if err == nil && tpl != "" { // made from a template: the subtasks of its subtask templates
		if serr := s.Tasks.CreateTemplateSubtasks(r.Context(), sess.UserID, tpl, key, time.Now()); serr != nil {
			s.Log.Warn("subtasks of a template", "template", tpl, "err", serr)
		}
	}
	if wantsJSON(r) { // «+» on a kanban column
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		if r.FormValue("as") == "row" { // backlog of a scrum team
			s.scrumTaskJSON(w, r, sess, key, func(string) error { return nil })
			return
		}
		s.quickTask(w, r, sess, key)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if link := r.FormValue("crm"); link != "" { // made from a CRM card: linked to it
		if id, err := s.Tasks.TaskIDByKey(r.Context(), key); err == nil {
			if err := s.Tasks.AddCRMLink(r.Context(), sess.UserID, id, link); err != nil {
				s.Log.Warn("crm link of a new task", "err", err)
			}
		}
	}
	if src := r.FormValue("copy"); src != "" { // a copy: the tags and CRM links of the task it copies
		s.taskCopyDone(r, sess, src, key)
	}
	if letter := r.FormValue("mail"); letter != "" { // made of a letter: its files and a link back
		s.taskLetterDone(r, sess, letter, key)
	}
	if room, ev := r.FormValue("im_room"), r.FormValue("im_event"); room != "" && ev != "" && s.Matrix != nil { // of a chat message
		s.taskMessageDone(r, sess, room, ev, key)
	}
	http.Redirect(w, r, "/t/"+key, http.StatusSeeOther)
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	uid, role := r.FormValue("user_id"), r.FormValue("role")
	var ie *i18n.Error
	switch {
	case err != nil:
	case sess.AdminActive(), p.Collab(): // administrators add people at once; so do a collab's owner and moderators
		err = s.Tasks.SetMember(r.Context(), p, uid, role)
	default: // a new person is invited and joins on accepting; a member gets the new role
		if err = s.Tasks.InviteMember(r.Context(), p, sess.UserID, uid, role); errors.As(err, &ie) && ie.Key == "err.already_member" {
			err = s.Tasks.SetMember(r.Context(), p, uid, role)
		}
	}
	if err == nil {
		s.Matrix.Wake()
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/p/" + r.PathValue("key")
	p, err := s.Tasks.ProjectByKey(r.Context(), sess.UserID, r.PathValue("key"))
	if err == nil {
		err = s.Tasks.RemoveMember(r.Context(), p, r.PathValue("uid"))
	}
	if err == nil {
		s.Matrix.Wake()
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#members", http.StatusSeeOther)
}

func (s *Server) task(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	card, err := s.Tasks.GetCard(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	if err := s.Tasks.See(r.Context(), sess.UserID, id); err != nil { // «Просмотры»
		s.Log.Error("task view", "err", err)
	}
	viewers, err := s.Tasks.Viewers(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	if s.Notify != nil { // the card is open: its notifications are read
		if err := s.Notify.MarkTaskRead(r.Context(), sess.UserID, id); err != nil {
			s.Log.Error("notify task read", "err", err)
		}
	}
	if s.Matrix != nil { // and its chat, a thread of the project's chat in the messenger (not to keep the card waiting)
		go func(ctx context.Context) {
			if err := s.Matrix.ReadTaskThread(ctx, sess.UserID, id); err != nil {
				s.Log.Warn("im task thread read", "err", err)
			}
		}(context.WithoutCancel(r.Context()))
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	files, err := s.Storages.TaskFiles(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	stores, err := s.Storages.List(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()))
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	pred, succ, err := s.Tasks.TaskLinks(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	ancestors, err := s.Tasks.Ancestors(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	subDone, subTotal, err := s.Tasks.SubtreeProgress(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	marks, err := s.Tasks.Marks(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	one := []tasks.Task{card.Task}
	if err := s.Tasks.FillMarks(r.Context(), "", one); err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	allTags, _ := s.Tasks.Tags(r.Context())
	reminders, err := s.Tasks.Reminders(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	fields, err := s.Tasks.FieldValues(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	crm, err := s.Tasks.CRMLinks(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	results, err := s.Tasks.Results(r.Context(), id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	resultIDs := map[string]bool{}
	for _, x := range results {
		resultIDs[x.ID] = true
	}
	var letters []mail.TaskLetter // the letters the task was made of
	if s.Mail != nil {
		if letters, err = s.Mail.TaskLetters(r.Context(), id); err != nil {
			s.taskErr(w, r, err, "/my")
			return
		}
	}
	dod, err := s.Tasks.TaskDodOf(r.Context(), sess.UserID, id)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	nav := "my"
	if card.Collab {
		nav = "collabs"
	}
	s.render(w, r, "task", page{Title: card.Key() + " " + card.Title, Nav: nav, Data: map[string]any{
		"Letters": letters, "Me": sess.UserID, "Viewers": viewers, "Dod": dod, "DodComplete": slices.Contains(card.Actions, tasks.ActionComplete),
		"Card": card, "Users": users, "Files": files, "Storages": stores, "Pred": pred, "Succ": succ, "LinkTypes": tasks.LinkTypes,
		"Chat": chatFeed(card, sess.UserID, resultIDs), "People": peopleCount(card), "BaseURL": s.BaseURL,
		"Messenger": s.Matrix != nil, "Form": editTaskForm(card, users),
		"Ancestors": ancestors, "SubDone": subDone, "SubTotal": subTotal, "Marks": marks, "Tags": one[0].Tags, "AllTags": allTags,
		"Results": results, "ResultIDs": resultIDs, "Reminders": reminders, "ReminderBefore": reminderBefore,
		"Fields": fields, "CRM": crm, "Calls": s.Calls != nil, "AppTabs": s.appTabs(r, sess.UserID, "TASK_VIEW_TAB"), "CanTake": s.Tasks.CanTake(r.Context(), sess.UserID, card.Task),
		// a collab in the archive takes nothing; a guest adds files and subtasks only where the collab lets guests
		"CanAttach": s.Tasks.CanAttach(r.Context(), sess.UserID, id) == nil,
		"CanSub":    !card.Frozen && s.Tasks.GuestMay(r.Context(), card.ProjectID, sess.UserID, "tasks") == nil,
	}})
}

// reminderBefore are the offsets «before the deadline» the card offers, in minutes.
var reminderBefore = []int{15, 30, 60, 180, 1440, 2880}

// taskForm is the one task form of go24: creating a task and «Изменить» in its card show the
// same fields — title, description, project (new), responsible, deadline, priority, accomplices, auditors, settings.
type taskForm struct {
	New                                   bool
	Action, Cancel                        string
	Card                                  tasks.TaskCard // edit: the task (its other fields are kept as they are)
	Title, Description, Responsible       string
	Deadline                              *time.Time
	Priority                              tasks.Priority
	Control, AllowDeadline, MatchWorkTime bool
	NoEfficiency                          bool // «Не учитывать в эффективности» (not in templates)
	Params                                tasks.Params
	Accomplices, Auditors                 []tasks.User
	Users                                 []tasks.User
	Projects                              []tasks.Project // new: where the task may go
	Project, Parent                       string          // new: preselected project key, parent task key
	CRM                                   string          // new: a CRM card the task is made from (its go24 path)
	Copy                                  string          // new: «Копировать» — the task the form is a copy of (its tags and CRM links go along)
	Mail                                  *mail.Message   // new: the letter the task is made of (its files go along)
	MailFiles                             int
	IM                                    *imSource // new: the chat message the task is made of
	Checklist                             string // new, template: one item per line
	// template mode: the deadline is an offset; the schedule of a regular template (a new task can be made regular too)
	Template                    bool
	TemplateID                  string
	TemplateParent              string // a subtask template: its base template
	TemplateSubs                int    // the subtask templates (a new task from a template gets their subtasks)
	DeadlineDays, DeadlineHours int
	Repeat                      tasks.Repeat
	ForNewUsers                 bool // «Шаблон для новых сотрудников»
}

// CollabKey limits the people fields to the collab's members: the task's collab, or (a new task, a template) the
// collab chosen in the project field.
func (f taskForm) CollabKey() string {
	if f.New || f.Template {
		return "@project"
	}
	if f.Card.Collab {
		return f.Card.ProjectKey
	}
	return ""
}

func editTaskForm(c tasks.TaskCard, users []tasks.User) taskForm {
	return taskForm{Action: "/t/" + c.Key() + "/edit", Card: c, Title: c.Title, Description: c.Description,
		Responsible: c.ResponsibleID, Deadline: c.Deadline, Priority: c.Priority, Control: c.RequiresControl,
		AllowDeadline: c.AllowChangeDeadline, MatchWorkTime: c.MatchWorkTime, NoEfficiency: c.NoEfficiency, Params: c.Params,
		Accomplices: c.Accomplices, Auditors: c.Auditors, Users: users}
}

// chatItem is one line of the task chat: a message, or a change from the history shown as a system line.
type chatItem struct {
	Comment  *tasks.Comment
	Event    *tasks.HistoryEntry
	Mine     bool
	Key      string // the task (actions under a message)
	IsResult bool   // the message is marked as the task's result
	NewDay   bool   // first item of a calendar day: the chat shows a date separator
	At       time.Time
}

// chatFeed merges messages and history into one timeline, like the portal task chat.
func chatFeed(c tasks.TaskCard, me string, results map[string]bool) []chatItem {
	items := make([]chatItem, 0, len(c.Comments)+len(c.History))
	for i := range c.Comments {
		m := &c.Comments[i]
		items = append(items, chatItem{Comment: m, Mine: m.AuthorID == me, At: m.CreatedAt, Key: c.Key(), IsResult: results[m.ID]})
	}
	for i := range c.History {
		h := &c.History[i]
		if h.Field == "description" { // "description changed" carries no text; the card shows the current one
			continue
		}
		items = append(items, chatItem{Event: h, At: h.CreatedAt})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.Before(items[j].At) })
	var last string
	for i := range items {
		d := items[i].At.Local().Format("2006-01-02")
		items[i].NewDay = d != last
		last = d
	}
	return items
}

// peopleCount: everyone in the task chat — creator, responsible, accomplices and auditors.
func peopleCount(c tasks.TaskCard) int {
	ids := map[string]bool{c.CreatorID: true, c.ResponsibleID: true}
	for _, u := range c.Accomplices {
		ids[u.ID] = true
	}
	for _, u := range c.Auditors {
		ids[u.ID] = true
	}
	return len(ids)
}

// taskPost resolves the task from the URL and runs fn, then redirects back to the task card.
func (s *Server) taskPost(w http.ResponseWriter, r *http.Request, anchor string, fn func(taskID string) error) {
	back := "/t/" + r.PathValue("key")
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = fn(id)
	}
	if wantsJSON(r) { // a page script (the Gantt chart): the page updates itself
		if err != nil {
			s.jsonErr(w, r, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		}
		return
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+anchor, http.StatusSeeOther)
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if wantsJSON(r) {
		s.taskActionJSON(w, r, sess)
		return
	}
	s.taskPost(w, r, "", func(id string) error {
		return s.Tasks.ApplyAction(r.Context(), sess.UserID, id, tasks.Action(r.FormValue("action")))
	})
}

// taskActionJSON serves drag & drop on the kanban: the page updates in place from this answer.
// {status, moves: {target status: action}, counters: {open, overdue, to_control}} or {error} with 4xx.
func (s *Server) taskActionJSON(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.Tasks.ApplyAction(r.Context(), sess.UserID, id, tasks.Action(r.FormValue("action")))
	}
	var t tasks.Task
	if err == nil {
		t, err = s.Tasks.TaskByID(r.Context(), id)
	}
	var moves map[string]map[tasks.Status]tasks.Action
	if err == nil {
		moves, err = s.Tasks.Moves(r.Context(), sess.UserID, []tasks.Task{t})
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	c, _ := s.Tasks.Counters(r.Context(), sess.UserID)
	m := moves[t.ID]
	if m == nil {
		m = map[tasks.Status]tasks.Action{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": t.Status, "status_title": s.tr(r, "status."+string(t.Status)), "moves": m,
		"counters": map[string]int{"open": c.Open, "overdue": c.Overdue, "to_control": c.ToControl, "comments": c.Comments},
	})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// jsonErr answers a page script with a translated error and a fitting status code.
func (s *Server) jsonErr(w http.ResponseWriter, r *http.Request, err error) {
	var ie *i18n.Error
	if !errors.As(err, &ie) {
		s.Log.Error("json", "err", err)
		ie = i18n.E("err.internal")
	}
	status := http.StatusUnprocessableEntity
	switch ie.Key {
	case "err.forbidden":
		status = http.StatusForbidden
	case "err.not_found":
		status = http.StatusNotFound
	case "err.internal":
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]string{"error": s.I18n.Err(s.lang(r), ie), "code": ie.Key})
}

func (s *Server) taskEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		deadline, err := parseDeadline(r.FormValue("deadline"))
		if err != nil {
			return err
		}
		startPlan, err := parseDate(r.FormValue("start_plan"))
		if err != nil {
			return err
		}
		endPlan, err := parseDate(r.FormValue("end_plan"))
		if err != nil {
			return err
		}
		var estimate *int
		if v := strings.TrimSpace(r.FormValue("estimate")); v != "" {
			secs, err := tasks.ParseDuration(v)
			if err != nil {
				return err
			}
			m := secs / 60
			estimate = &m
		}
		version, _ := strconv.Atoi(r.FormValue("version"))
		prio, _ := strconv.Atoi(r.FormValue("priority"))
		err = s.Tasks.UpdateTask(r.Context(), sess.UserID, id, tasks.TaskUpdate{
			Version:             version,
			Title:               r.FormValue("title"),
			Description:         r.FormValue("description"),
			ResponsibleID:       r.FormValue("responsible"),
			Priority:            tasks.Priority(prio),
			Deadline:            deadline,
			RequiresControl:     r.FormValue("requires_control") == "on",
			EstimateMinutes:     estimate,
			StartPlan:           startPlan,
			EndPlan:             endPlan,
			AllowChangeDeadline: r.FormValue("allow_change_deadline") == "on",
			MatchWorkTime:       r.FormValue("match_work_time") == "on",
			Params:              formParams(r),
		})
		if err == nil && r.FormValue("efficiency_form") == "1" { // the forms with the «Не учитывать в эффективности» box
			err = s.Tasks.SetNoEfficiency(r.Context(), sess.UserID, id, r.FormValue("no_efficiency") == "on")
		}
		if err != nil || r.FormValue("people") != "1" {
			return err
		}
		// the full task form also carries the accomplices and auditors
		return s.Tasks.SetParticipants(r.Context(), sess.UserID, id, r.Form["accomplices"], r.Form["auditors"])
	})
}

// formParams reads the boxes of the task parameters (the template "params").
func formParams(r *http.Request) tasks.Params {
	return tasks.Params{DatesFromSubtasks: r.FormValue("dates_from_subtasks") == "on",
		AutoComplete: r.FormValue("auto_complete") == "on", ResultRequired: r.FormValue("result_required") == "on"}
}

// parseDate reads an <input type="date"> value as a calendar date ("" = not set).
func parseDate(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, i18n.E("err.bad_date")
	}
	return &t, nil
}

// ---------- time tracking ----------

func (s *Server) timerStart(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#time", func(id string) error { return s.Tasks.StartTimer(r.Context(), sess.UserID, id) })
}

// timerStop stops the user's timer wherever it runs and returns to the page the user came from.
func (s *Server) timerStop(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Tasks.StopTimer(r.Context(), sess.UserID); err != nil {
		s.fail(w, "stop timer", err)
		return
	}
	back := "/my"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && ref.Path != "" {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) timeAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#time", func(id string) error {
		secs, err := tasks.ParseDuration(r.FormValue("duration"))
		if err != nil {
			return err
		}
		day := time.Now()
		if v := r.FormValue("date"); v != "" {
			if day, err = time.ParseInLocation("2006-01-02", v, time.Local); err != nil {
				return i18n.E("err.bad_date")
			}
		}
		return s.Tasks.AddTime(r.Context(), sess.UserID, id, secs, day, r.FormValue("comment"))
	})
}

// timeUpdate changes a time entry (the pencil next to the bin).
func (s *Server) timeUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#time", func(id string) error {
		secs, err := tasks.ParseDuration(r.FormValue("duration"))
		if err != nil {
			return err
		}
		day, err := time.ParseInLocation("2006-01-02", r.FormValue("date"), time.Local)
		if err != nil {
			return i18n.E("err.bad_date")
		}
		return s.Tasks.UpdateTime(r.Context(), sess.UserID, id, r.PathValue("id"), secs, day, r.FormValue("comment"))
	})
}

func (s *Server) timeDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#time", func(id string) error {
		return s.Tasks.DeleteTime(r.Context(), sess.UserID, id, r.PathValue("id"))
	})
}

func (s *Server) taskParticipant(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#people", func(id string) error {
		err := s.Tasks.SetParticipant(r.Context(), sess.UserID, id,
			r.FormValue("user_id"), r.FormValue("role"), r.FormValue("op") != "remove")
		if err == nil {
			s.Matrix.Wake()
		}
		return err
	})
}

// taskDelegate: «Делегировать» — the responsible hands the task to another person.
func (s *Server) taskDelegate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		err := s.Tasks.Delegate(r.Context(), sess.UserID, id, r.FormValue("to"))
		if err == nil {
			s.Matrix.Wake()
		}
		return err
	})
}

// taskMessenger opens the task's discussion in the messenger (a thread or the task's room, made on first use).
func (s *Server) taskMessenger(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/t/" + r.PathValue("key")
	if s.Matrix == nil {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
	if err == nil {
		err = s.Tasks.CheckVisible(r.Context(), sess.UserID, id)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	room, root, err := s.Matrix.TaskChat(r.Context(), id)
	if err != nil {
		s.Log.Error("messenger link", "err", err)
		s.taskErr(w, r, i18n.E("err.messenger"), back)
		return
	}
	link := "/im?room=" + url.QueryEscape(room)
	if root != "" {
		link += "&thread=" + url.QueryEscape(root)
	}
	http.Redirect(w, r, link, http.StatusSeeOther)
}

// checklistAdd: every line of title is an item of the list (a pasted list gives many items).
func (s *Server) checklistAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#checklist", func(id string) error {
		return s.Tasks.AddChecklistItems(r.Context(), sess.UserID, id, r.FormValue("list"), r.FormValue("title"))
	})
}

// checklistChange: op = toggle (the default), delete, edit (title), important (on), move (list, before) or
// task — «Сделать задачей»: the new task form with the item's text, a subtask of this one.
func (s *Server) checklistChange(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	key, item := r.PathValue("key"), r.PathValue("id")
	if r.FormValue("op") == "task" {
		link := "/tasks/new?" + url.Values{"parent": {key}, "title": {r.FormValue("title")}}.Encode()
		http.Redirect(w, r, link, http.StatusSeeOther)
		return
	}
	s.taskPost(w, r, "#checklist", func(id string) error {
		switch r.FormValue("op") {
		case "delete":
			return s.Tasks.ChangeChecklistItem(r.Context(), sess.UserID, id, item, true)
		case "edit":
			return s.Tasks.EditChecklistItem(r.Context(), sess.UserID, id, item, r.FormValue("title"))
		case "important":
			return s.Tasks.MarkChecklistItem(r.Context(), sess.UserID, id, item, r.FormValue("on") == "1")
		case "move":
			return s.Tasks.MoveChecklistItem(r.Context(), sess.UserID, id, item, r.FormValue("list"), r.FormValue("before"))
		}
		return s.Tasks.ChangeChecklistItem(r.Context(), sess.UserID, id, item, false)
	})
}

// checklistList: a new list (POST /t/{key}/checklists), or op = rename (title) / delete of one.
func (s *Server) checklistList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#checklist", func(id string) error {
		list := r.PathValue("id")
		switch {
		case list == "":
			_, err := s.Tasks.AddChecklist(r.Context(), sess.UserID, id, r.FormValue("title"))
			return err
		case r.FormValue("op") == "delete":
			return s.Tasks.DeleteChecklist(r.Context(), sess.UserID, id, list)
		}
		return s.Tasks.RenameChecklist(r.Context(), sess.UserID, id, list, r.FormValue("title"))
	})
}

func (s *Server) commentAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if wantsJSON(r) { // the chat sends messages without leaving the page
		id, err := s.Tasks.TaskIDByKey(r.Context(), r.PathValue("key"))
		var m tasks.Comment
		if err == nil {
			m, err = s.postComment(r, sess, id, r.FormValue("body"))
		}
		var buf bytes.Buffer
		if err == nil {
			err = s.pages[s.lang(r)]["task"].ExecuteTemplate(&buf, "chatmsg", chatItem{Comment: &m, Mine: true, At: m.CreatedAt})
		}
		if err != nil {
			s.jsonErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"id": m.ID, "html": buf.String()})
		return
	}
	s.taskPost(w, r, "#comments", func(id string) error {
		_, err := s.postComment(r, sess, id, r.FormValue("body"))
		return err
	})
}

// taskErr turns domain errors into user-facing responses in the user's language.
// "Forbidden" and "not found" are recognized by message key, so tasks, storages and files share them.
func (s *Server) taskErr(w http.ResponseWriter, r *http.Request, err error, back string) {
	if fromForm(r) { // a form sent in the background: it stays as typed and shows the message
		s.jsonErr(w, r, err)
		return
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) {
		s.fail(w, "tasks", err)
		return
	}
	switch ie.Key {
	case "err.forbidden":
		http.Error(w, s.tr(r, ie.Key), http.StatusForbidden)
	case "err.not_found":
		http.Error(w, s.tr(r, ie.Key), http.StatusNotFound)
	default: // validation errors and conflicts: back to the form with a message
		sep := "?"
		if strings.Contains(back, "?") {
			sep = "&"
		}
		http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.I18n.Err(s.lang(r), ie)), http.StatusSeeOther)
	}
}

func parseDeadline(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
	if err != nil {
		return nil, i18n.E("err.bad_deadline")
	}
	return &t, nil
}

// ---------- «Мой план» ----------

func (s *Server) plan(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	cols, err := s.Tasks.Plan(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "plan", page{TitleKey: "title.plan", Nav: "plan", Data: cols})
}

// planMove is called by drag & drop (fetch); answers 204 or an error status.
func (s *Server) planMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), r.FormValue("task"))
	if err == nil {
		err = s.Tasks.MovePlan(r.Context(), sess.UserID, id, r.FormValue("stage"))
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) planStage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	switch id := r.FormValue("id"); r.FormValue("op") {
	case "add":
		err = s.Tasks.AddStage(r.Context(), sess.UserID, r.FormValue("title"))
	case "rename":
		err = s.Tasks.RenameStage(r.Context(), sess.UserID, id, r.FormValue("title"))
	case "delete":
		err = s.Tasks.DeleteStage(r.Context(), sess.UserID, id)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/plan")
		return
	}
	http.Redirect(w, r, "/plan", http.StatusSeeOther)
}

// setColumns saves the task list columns chosen with the gear (fetch from list.js, or a plain form).
// widths: the column and pane widths the user dragged in every list (static/js/widths.js asks once per page).
func (s *Server) widths(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	all, err := s.Tasks.Widths(r.Context(), sess.UserID)
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(all)
}

// setWidths keeps one list's widths: name and widths ({column: px}, empty — forget them).
func (s *Server) setWidths(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var widths map[string]int
	if v := r.FormValue("widths"); v != "" && json.Unmarshal([]byte(v), &widths) != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	if err := s.Tasks.SetWidths(r.Context(), sess.UserID, r.FormValue("name"), widths); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setColumns(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.jsonErr(w, r, i18n.E("err.bad_form"))
		return
	}
	if err := s.Tasks.SetColumns(r.Context(), sess.UserID, r.Form["cols"]); err != nil {
		if wantsJSON(r) {
			s.jsonErr(w, r, err)
		} else {
			s.taskErr(w, r, err, "/my")
		}
		return
	}
	if wantsJSON(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	back := "/my"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && ref.Path != "" {
		back = ref.RequestURI()
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// taskDeadline moves the deadline from the card (creator, managers, or the responsible when allowed).
func (s *Server) taskDeadline(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		var d *time.Time
		if r.FormValue("op") != "clear" {
			var err error
			if d, err = parseDeadline(r.FormValue("deadline")); err != nil {
				return err
			}
		}
		return s.Tasks.SetDeadline(r.Context(), sess.UserID, id, d)
	})
}

// ---------- company settings (administrators) ----------

func (s *Server) worktimePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.fail(w, "worktime", err)
		return
	}
	off := map[int]bool{}
	for _, d := range sched.Weekend {
		off[d] = true
	}
	rules, err := s.Company.ReportRules(r.Context())
	if err != nil {
		s.fail(w, "worktime", err)
		return
	}
	_, depts, err := s.tree(r)
	if err != nil {
		s.fail(w, "worktime", err)
		return
	}
	s.render(w, r, "worktime", page{TitleKey: "title.worktime", Nav: "worktime", Data: map[string]any{
		"Schedule": sched, "Off": off, "Days": []int{1, 2, 3, 4, 5, 6, 0},
		"Rules": rules, "Periods": company.ReportPeriods, "Departments": depts,
	}})
}

func (s *Server) saveWorktime(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/worktime")
		return
	}
	sched := worktime.Schedule{Start: r.FormValue("start"), End: r.FormValue("end"), Weekend: []int{},
		Holidays: strings.Fields(strings.NewReplacer(",", " ", ";", " ").Replace(r.FormValue("holidays"))),
		LateAfter: r.FormValue("late_after"), LeaveBefore: r.FormValue("leave_before"), MinDuration: r.FormValue("min_duration"),
		FreeEdit: r.FormValue("free_edit") != ""}
	for _, v := range r.Form["weekend"] {
		d, err := strconv.Atoi(v)
		if err != nil {
			s.taskErr(w, r, i18n.E("err.bad_form"), "/settings/worktime")
			return
		}
		sched.Weekend = append(sched.Weekend, d)
	}
	if err := s.Tasks.SetSchedule(r.Context(), sess.UserID, sched); err != nil {
		s.taskErr(w, r, err, "/settings/worktime")
		return
	}
	s.Log.Info("worktime saved", "user_id", sess.UserID)
	http.Redirect(w, r, "/settings/worktime?msg="+url.QueryEscape(s.tr(r, "worktime.saved")), http.StatusSeeOther)
}

// postComment: with the messenger on, the message is written to Matrix first (it is kept there) and then shown
// in go24; without it, go24 keeps it itself.
func (s *Server) postComment(r *http.Request, sess auth.Session, taskID, body string) (tasks.Comment, error) {
	if s.Matrix == nil {
		return s.Tasks.PostComment(r.Context(), sess.UserID, taskID, body)
	}
	if strings.TrimSpace(body) == "" {
		return tasks.Comment{}, i18n.E("err.comment_empty")
	}
	c, err := s.Matrix.PostTaskComment(r.Context(), sess.UserID, taskID, strings.TrimSpace(body))
	var ie *i18n.Error
	if err != nil && !errors.As(err, &ie) {
		s.Log.Error("task comment to matrix", "err", err)
		return c, i18n.E("err.messenger")
	}
	return c, err
}

// newTaskPage is «Создать задачу» from the task lists: the project is picked in the form.
func (s *Server) newTaskPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	projects, err := s.Tasks.ListProjects(r.Context(), sess.UserID)
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	var mine []tasks.Project
	for _, p := range projects {
		if p.CanCreate() && p.MyRole != "admin" {
			mine = append(mine, p)
		}
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	sched, err := s.Tasks.Schedule(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	q := r.URL.Query()
	deadline := sched.DefaultDeadline(time.Now())
	project, parent := strings.ToUpper(q.Get("project")), strings.ToUpper(q.Get("parent"))
	if project == "" && parent != "" {
		project, _, _ = tasks.ParseKey(parent)
	}
	cancel := "/my"
	if project != "" {
		cancel = "/p/" + project
	}
	f := taskForm{New: true, Action: "/tasks/new", Cancel: cancel, Responsible: sess.UserID, Deadline: &deadline,
		Priority: 1, Users: users, Projects: mine, Project: project, Parent: parent, CRM: q.Get("crm"),
		Repeat: tasks.Repeat{Period: "weekly", Every: 1, WeekDays: []int{int(time.Now().Weekday()+6)%7 + 1}, MonthDay: time.Now().Day(),
			YearMonth: int(time.Now().Month()), At: "09:00", StartsOn: time.Now()}}
	f.Title = q.Get("title") // «Сделать задачей» of a checklist item
	if id := q.Get("template"); id != "" { // «Создать задачу» of a template: its fields, the deadline counted from now
		if t, err := s.Tasks.Template(r.Context(), sess.UserID, id); err == nil {
			tf := templateForm(t, users, mine)
			f.Title, f.Description, f.Responsible, f.Priority = tf.Title, tf.Description, tf.Responsible, tf.Priority
			f.Control, f.AllowDeadline, f.MatchWorkTime = tf.Control, tf.AllowDeadline, tf.MatchWorkTime
			f.Accomplices, f.Auditors, f.Project, f.Checklist, f.TemplateID = tf.Accomplices, tf.Auditors, tf.Project, tf.Checklist, t.ID
			f.TemplateSubs = tf.TemplateSubs
			f.Deadline = t.NewTask(time.Now()).Deadline
		}
	}
	if key := q.Get("copy"); key != "" { // «Копировать» in a task card
		s.taskCopyForm(r, sess, key, &f)
	}
	if title, text, ok := s.feedSource(r, sess); ok { // … of a feed post or the feed's form
		f.Title, f.Description = title, text
	}
	if id := q.Get("mail"); id != "" { // «Создать задачу» of a letter
		s.taskFromLetter(r, sess, id, &f)
	}
	if room, ev := q.Get("im_room"), q.Get("im_event"); room != "" && ev != "" && s.Matrix != nil { // … of a chat message
		s.taskFromMessage(r, sess, room, ev, &f)
	}
	s.render(w, r, "newtask", page{TitleKey: "task.new", Nav: "my", Data: map[string]any{"Projects": mine, "Form": f}})
}

// taskCopyForm fills the new task's form with the fields of a task the person can see: the title, description,
// people, deadline (a past one gives way to the default), priority, options, project, parent task and checklist.
func (s *Server) taskCopyForm(r *http.Request, sess auth.Session, key string, f *taskForm) {
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err != nil {
		return
	}
	c, err := s.Tasks.GetCard(r.Context(), sess.UserID, id)
	if err != nil {
		return
	}
	f.Copy, f.Title, f.Description, f.Responsible, f.Priority = c.Key(), c.Title, c.Description, c.ResponsibleID, c.Priority
	f.Control, f.AllowDeadline, f.MatchWorkTime, f.NoEfficiency = c.RequiresControl, c.AllowChangeDeadline, c.MatchWorkTime, c.NoEfficiency
	f.Params = c.Params
	f.Accomplices, f.Auditors, f.Parent = c.Accomplices, c.Auditors, c.ParentKey
	if c.Deadline != nil && c.Deadline.After(time.Now()) {
		f.Deadline = c.Deadline
	}
	for _, p := range f.Projects {
		if p.Key == c.ProjectKey {
			f.Project, f.Cancel = p.Key, "/t/"+c.Key()
		}
	}
	f.Checklist = strings.Join(c.ChecklistLines(), "\n")
}

// taskCopyDone gives the new copy the tags and CRM links of the task it was copied from.
func (s *Server) taskCopyDone(r *http.Request, sess auth.Session, src, key string) {
	srcID, err := s.Tasks.TaskIDByKey(r.Context(), src)
	if err != nil {
		return
	}
	c, err := s.Tasks.GetCard(r.Context(), sess.UserID, srcID)
	if err != nil {
		return
	}
	id, err := s.Tasks.TaskIDByKey(r.Context(), key)
	if err != nil {
		return
	}
	one := []tasks.Task{c.Task}
	if err := s.Tasks.FillMarks(r.Context(), "", one); err == nil {
		for _, tag := range one[0].Tags {
			if err := s.Tasks.SetTag(r.Context(), sess.UserID, id, tag, true); err != nil {
				s.Log.Warn("tag of a task copy", "err", err)
			}
		}
	}
	links, _ := s.Tasks.CRMLinks(r.Context(), srcID)
	for _, l := range links {
		if err := s.Tasks.AddCRMLink(r.Context(), sess.UserID, id, l.URL); err != nil {
			s.Log.Warn("crm link of a task copy", "err", err)
		}
	}
}

// templateID is the template a new task's form came from (only one of the user's own).
func templateID(r *http.Request, userID string, s *Server) string {
	id := r.FormValue("template")
	if id == "" {
		return ""
	}
	if _, err := s.Tasks.Template(r.Context(), userID, id); err != nil {
		return ""
	}
	return id
}

// newTaskCreate creates the task in the chosen project (same rules as the project's own form).
func (s *Server) newTaskCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if r.FormValue("repeat") == "on" { // a regular task: the portal keeps it as a template that makes the tasks
		t, err := parseTemplate(r)
		if err == nil {
			_, err = s.Tasks.SaveTemplate(r.Context(), sess.UserID, t)
		}
		if err != nil {
			s.taskErr(w, r, err, "/tasks/new")
			return
		}
		http.Redirect(w, r, "/templates?msg="+url.QueryEscape(s.tr(r, "templates.regular_saved")), http.StatusSeeOther)
		return
	}
	key := r.FormValue("project")
	if key == "" {
		s.taskErr(w, r, i18n.E("err.bad_form"), "/tasks/new")
		return
	}
	r.SetPathValue("key", key)
	s.createTask(w, r, sess)
}

// taskMark: favourite, pin, mute (the person's own), on=1 / on=0.
func (s *Server) taskMark(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		return s.Tasks.SetMark(r.Context(), sess.UserID, id, r.FormValue("mark"), r.FormValue("on") == "1")
	})
}

// taskTag adds a tag (op=add) or removes it (op=remove).
func (s *Server) taskTag(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		return s.Tasks.SetTag(r.Context(), sess.UserID, id, r.FormValue("tag"), r.FormValue("op") != "remove")
	})
}

// taskCRM links a CRM item (op=add, url) or unlinks it (op=remove).
func (s *Server) taskCRM(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		if r.FormValue("op") == "remove" {
			return s.Tasks.RemoveCRMLink(r.Context(), sess.UserID, id, r.FormValue("url"))
		}
		return s.Tasks.AddCRMLink(r.Context(), sess.UserID, id, r.FormValue("url"))
	})
}

// taskRate: «Оценка» of the creator (rating = positive | negative | "").
func (s *Server) taskRate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error { return s.Tasks.Rate(r.Context(), sess.UserID, id, r.FormValue("rating")) })
}

// reminderAdd sets a reminder: when=at (the time in «at») or when=deadline (minutes «before» the deadline).
func (s *Server) reminderAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		var at *time.Time
		before := 0
		if r.FormValue("when") == "deadline" {
			before, _ = strconv.Atoi(r.FormValue("before"))
			if !slices.Contains(reminderBefore, before) {
				return i18n.E("err.bad_form")
			}
		} else {
			var err error
			if at, err = parseDeadline(r.FormValue("at")); err != nil || at == nil {
				return i18n.E("err.bad_form")
			}
		}
		return s.Tasks.AddReminder(r.Context(), sess.UserID, id, r.FormValue("recipient"), at, before, time.Now())
	})
}

func (s *Server) reminderDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error { return s.Tasks.DeleteReminder(r.Context(), sess.UserID, id, r.PathValue("id")) })
}

// taskResult marks a chat message as the task's result (on=1) or unmarks it.
func (s *Server) taskResult(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "#results", func(id string) error {
		return s.Tasks.SetResult(r.Context(), sess.UserID, id, r.PathValue("id"), r.FormValue("on") == "1")
	})
}
