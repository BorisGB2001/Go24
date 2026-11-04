package web

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// The agent of tasks (GO24DEV-484), as CoPilot in the tasks: a pane beside the task lists and the task card
// where the employee talks to the agent «tasks» — «Что у меня горит», «Итоги по задаче», «Разбить на подзадачи» or
// anything else. The agent finds and reads tasks with the employee's rights; a new task or subtask, a checklist, a
// message to a task's chat, a deadline and a status change wait for «Подтвердить».

// taskAIReq carries the request in the context of the agent: the task open beside the pane.
type taskAIReq struct{}

type taskAIOpen struct {
	r   *http.Request
	key string
}

func taskAIOf(ctx context.Context) taskAIOpen {
	o, _ := ctx.Value(taskAIReq{}).(taskAIOpen)
	return o
}

// The agent's limits: the tasks a list gives, the text of a description and a chat it reads.
const (
	taskAIList     = 20
	taskAIListMax  = 50
	taskAIText     = 4000
	taskAIComments = 20  // the last messages of a task's chat
	taskAIComment  = 600 // runes of each
	taskAIPeople   = 20
)

// taskAIActions are the status changes the agent may make (tasks.Store.ApplyAction).
var taskAIActions = []string{string(tasks.ActionStart), string(tasks.ActionPause), string(tasks.ActionComplete), string(tasks.ActionDefer),
	string(tasks.ActionRenew), string(tasks.ActionApprove), string(tasks.ActionDisapprove)}

// taskAIDate reads a deadline as the model writes it: «2026-11-05» (the end of the day) or «2026-11-05 15:00».
func taskAIDate(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02T15:04:05"} {
		if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return &t, nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		t = t.Add(24*time.Hour - time.Minute) // 23:59 of the day, as a deadline «на день»
		return &t, nil
	}
	return nil, i18n.E("err.bad_deadline")
}

func taskAIWhen(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Local().Format("2006-01-02 15:04")
}

// taskAgent is the agent of tasks.
func (s *Server) taskAgent() *ai.Agent {
	obj := func(props string, required ...string) json.RawMessage {
		req, _ := json.Marshal(required)
		if required == nil {
			req = []byte("[]")
		}
		return json.RawMessage(`{"type":"object","properties":{` + props + `},"required":` + string(req) + `}`)
	}
	return &ai.Agent{Name: "tasks", System: s.taskAISystem, Tools: []ai.Tool{
		{ToolSpec: ai.ToolSpec{Name: "list_tasks", Description: "Tasks the employee may see, the nearest deadline first: key, title, status, deadline, responsible, project, subtasks. " +
			"role: responsible (by default — tasks the employee does), accomplice, auditor, creator (given to others), member (in any role), any; " +
			"status: open (by default), overdue, completed, any; query — words of the title or a key; project — a project key; responsible — a person's id (find_people).",
			Schema: obj(`"role":{"type":"string","enum":["responsible","accomplice","auditor","creator","member","any"]},"status":{"type":"string","enum":["open","overdue","completed","any"]},` +
				`"query":{"type":"string"},"project":{"type":"string"},"responsible":{"type":"string"},"limit":{"type":"integer","minimum":1,"maximum":50}`)},
			Run: s.taskAIList},
		{ToolSpec: ai.ToolSpec{Name: "read_task", Description: "A task in full by its key: description, status, deadline, people, checklists, subtasks, the last messages of its chat and the status changes the employee may make.",
			Schema: obj(`"key":{"type":"string"}`, "key")}, Run: s.taskAIRead},
		{ToolSpec: ai.ToolSpec{Name: "list_projects", Description: "Projects and groups the employee may put tasks into: key and name.",
			Schema: obj(``)}, Run: s.taskAIProjects},
		{ToolSpec: ai.ToolSpec{Name: "find_people", Description: "Employees by a part of their name or e-mail: id, name, e-mail — to make someone responsible.",
			Schema: obj(`"query":{"type":"string"}`, "query")}, Run: s.taskAIPeople},
		{ToolSpec: ai.ToolSpec{Name: "create_task", Description: "Creates a task. parent — the key of the task it is a subtask of (it goes into the parent's project); project — a project key " +
			"(by default the parent's, else the one the employee last put a task into); responsible — a person's id (by default the employee); deadline — YYYY-MM-DD or YYYY-MM-DD HH:MM; " +
			"checklist — its items; important — high priority.",
			Schema: obj(`"title":{"type":"string"},"description":{"type":"string"},"parent":{"type":"string"},"project":{"type":"string"},"responsible":{"type":"string"},`+
				`"deadline":{"type":"string"},"checklist":{"type":"array","items":{"type":"string"}},"important":{"type":"boolean"}`, "title")},
			Write: true, Run: s.taskAICreate},
		{ToolSpec: ai.ToolSpec{Name: "add_checklist", Description: "Adds a checklist with the items to a task; title — the checklist's name.",
			Schema: obj(`"key":{"type":"string"},"title":{"type":"string"},"items":{"type":"array","items":{"type":"string"}}`, "key", "items")},
			Write: true, Run: s.taskAIChecklist},
		{ToolSpec: ai.ToolSpec{Name: "add_comment", Description: "Writes a message to a task's chat on behalf of the employee.",
			Schema: obj(`"key":{"type":"string"},"text":{"type":"string"}`, "key", "text")}, Write: true, Run: s.taskAIComment},
		{ToolSpec: ai.ToolSpec{Name: "set_deadline", Description: "Sets a task's deadline: YYYY-MM-DD or YYYY-MM-DD HH:MM; an empty one removes it.",
			Schema: obj(`"key":{"type":"string"},"deadline":{"type":"string"}`, "key", "deadline")}, Write: true, Run: s.taskAIDeadline},
		{ToolSpec: ai.ToolSpec{Name: "change_status", Description: "Changes a task's status: start, pause, complete, defer, renew, approve (accept the work), disapprove (return it) — only those read_task lists.",
			Schema: obj(`"key":{"type":"string"},"action":{"type":"string","enum":["`+strings.Join(taskAIActions, `","`)+`"]}`, "key", "action")},
			Write: true, Run: s.taskAIStatus},
	}}
}

// taskAISystem tells the model whom it works for, when, and which task is open.
func (s *Server) taskAISystem(ctx context.Context, userID string) string {
	o := taskAIOf(ctx)
	lang := s.portalLang()
	if o.r != nil {
		lang = s.lang(o.r)
	}
	var name string
	_ = s.DB.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, userID).Scan(&name)
	open := s.I18n.T(lang, "tasks.ai.no_task")
	if o.key != "" {
		if id, err := s.Tasks.TaskIDByKey(ctx, o.key); err == nil {
			if c, err := s.Tasks.GetCard(ctx, userID, id); err == nil {
				open = s.I18n.T(lang, "tasks.ai.open_task", c.Key(), c.Title)
			}
		}
	}
	return s.I18n.T(lang, "tasks.ai.prompt", name, userID, time.Now().Format("2006-01-02 15:04 (Mon)"), open)
}

// taskAITask is a task in a list the agent gets.
type taskAITask struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Deadline    string `json:"deadline,omitempty"`
	Overdue     bool   `json:"overdue,omitempty"`
	Important   bool   `json:"important,omitempty"`
	Responsible string `json:"responsible"`
	Creator     string `json:"creator,omitempty"`
	Project     string `json:"project"`
	Parent      string `json:"parent,omitempty"`
	Subtasks    string `json:"subtasks,omitempty"` // done/all
}

func taskAITaskOf(t tasks.Task) taskAITask {
	x := taskAITask{Key: t.Key(), Title: t.Title, Status: string(t.Status), Deadline: taskAIWhen(t.Deadline), Important: t.Priority == 2,
		Responsible: t.ResponsibleName, Creator: t.CreatorName, Project: t.ProjectKey, Parent: t.ParentKey}
	x.Overdue = t.Deadline != nil && t.Status != tasks.StatusCompleted && t.Deadline.Before(time.Now())
	if t.SubTotal > 0 {
		x.Subtasks = strconv.Itoa(t.SubDone) + "/" + strconv.Itoa(t.SubTotal)
	}
	return x
}

func (s *Server) taskAIList(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		Role, Status, Query, Project, Responsible string
		Limit                                     int
	}
	_ = json.Unmarshal(raw, &in)
	f := tasks.Filter{UserID: userID, Viewer: userID, Search: in.Query, ResponsibleID: in.Responsible}
	switch in.Role {
	case "", "responsible":
		if in.Responsible == "" {
			f.Role = "responsible"
		}
	case "member":
		f.MemberID = userID
	case "any":
	default:
		f.Role = in.Role
	}
	switch in.Status {
	case "", "open":
		f.OnlyOpen = true
	case "overdue":
		f.Overdue = true
	case "completed":
		f.Status = tasks.StatusCompleted
	}
	if in.Project != "" {
		p, err := s.Tasks.ProjectByKey(ctx, userID, strings.ToUpper(strings.TrimSpace(in.Project)))
		if err != nil {
			return "", err
		}
		f.ProjectID = p.ID
	}
	if in.Limit <= 0 {
		in.Limit = taskAIList
	}
	in.Limit = min(in.Limit, taskAIListMax)
	list, err := s.Tasks.ListTasks(ctx, f)
	if err != nil {
		return "", err
	}
	out := []taskAITask{}
	for i, t := range list {
		if i == in.Limit {
			break
		}
		out = append(out, taskAITaskOf(t))
	}
	return toolJSON(out)
}

// taskAICard is the task the key names, as the employee may see it.
func (s *Server) taskAICard(ctx context.Context, userID, key string) (tasks.TaskCard, error) {
	id, err := s.Tasks.TaskIDByKey(ctx, strings.ToUpper(strings.TrimSpace(key)))
	if err != nil {
		return tasks.TaskCard{}, err
	}
	return s.Tasks.GetCard(ctx, userID, id)
}

func (s *Server) taskAIRead(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct{ Key string }
	_ = json.Unmarshal(raw, &in)
	c, err := s.taskAICard(ctx, userID, in.Key)
	if err != nil {
		return "", err
	}
	type item struct {
		Title string `json:"title"`
		Done  bool   `json:"done,omitempty"`
	}
	type list struct {
		Title string `json:"title,omitempty"`
		Items []item `json:"items"`
	}
	type message struct {
		Author string `json:"author"`
		Date   string `json:"date"`
		Text   string `json:"text"`
	}
	var out struct {
		taskAITask
		Description string       `json:"description,omitempty"`
		Created     string       `json:"created"`
		Accomplices []string     `json:"accomplices,omitempty"`
		Auditors    []string     `json:"auditors,omitempty"`
		Checklists  []list       `json:"checklists,omitempty"`
		Subtasks    []taskAITask `json:"subtask_list,omitempty"`
		Chat        []message    `json:"chat,omitempty"` // the last messages, the oldest first
		Actions     []string     `json:"actions,omitempty"`
		Closed      string       `json:"closed,omitempty"`
	}
	out.taskAITask = taskAITaskOf(c.Task)
	out.Description = aiClip(c.Description, taskAIText)
	out.Created = c.CreatedAt.Local().Format("2006-01-02 15:04")
	out.Closed = taskAIWhen(c.ClosedAt)
	for _, u := range c.Accomplices {
		out.Accomplices = append(out.Accomplices, u.Name)
	}
	for _, u := range c.Auditors {
		out.Auditors = append(out.Auditors, u.Name)
	}
	for _, l := range c.Checklists {
		x := list{Title: l.Title, Items: []item{}}
		for _, it := range l.Items {
			x.Items = append(x.Items, item{it.Title, it.Done})
		}
		out.Checklists = append(out.Checklists, x)
	}
	for _, t := range c.Subtasks {
		out.Subtasks = append(out.Subtasks, taskAITaskOf(t))
	}
	comments := c.Comments
	if len(comments) > taskAIComments {
		comments = comments[len(comments)-taskAIComments:]
	}
	for _, m := range comments {
		if strings.TrimSpace(m.Body) != "" {
			out.Chat = append(out.Chat, message{m.AuthorName, m.CreatedAt.Local().Format("2006-01-02 15:04"), aiClip(m.Body, taskAIComment)})
		}
	}
	if !c.Frozen {
		for _, a := range c.Actions {
			out.Actions = append(out.Actions, string(a))
		}
	}
	return toolJSON(out)
}

func (s *Server) taskAIProjects(ctx context.Context, userID string, _ json.RawMessage) (string, error) {
	list, err := s.Tasks.ListProjects(ctx, userID)
	if err != nil {
		return "", err
	}
	type project struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	out := []project{}
	for _, p := range list {
		if p.CanCreate() && p.MyRole != "admin" {
			out = append(out, project{p.Key, p.Name})
		}
	}
	return toolJSON(out)
}

func (s *Server) taskAIPeople(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct{ Query string }
	_ = json.Unmarshal(raw, &in)
	q := strings.ToLower(strings.TrimSpace(in.Query))
	list, err := s.Tasks.ListUsers(tasks.WithViewer(ctx, userID, tasks.IsGuestCtx(ctx)))
	if err != nil {
		return "", err
	}
	type person struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	out := []person{}
	for _, u := range list {
		if q == "" || strings.Contains(strings.ToLower(u.Name), q) || strings.Contains(strings.ToLower(u.Email), q) {
			if out = append(out, person{u.ID, u.Name, u.Email}); len(out) == taskAIPeople {
				break
			}
		}
	}
	return toolJSON(out)
}

// taskAIItems are the items of a checklist the model gave: trimmed, without empty ones and marks, at most
// copilotItems.
func taskAIItems(items []string) []string {
	return copilotLines(strings.Join(items, "\n"))
}

type taskAINew struct {
	Title, Description, Parent, Project, Responsible, Deadline string
	Checklist                                                  []string
	Important                                                  bool
}

func (s *Server) taskAICreate(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in taskAINew
	_ = json.Unmarshal(raw, &in)
	deadline, err := taskAIDate(in.Deadline)
	if err != nil {
		return "", err
	}
	parent := strings.ToUpper(strings.TrimSpace(in.Parent))
	project := strings.ToUpper(strings.TrimSpace(in.Project))
	if parent != "" {
		pc, err := s.taskAICard(ctx, userID, parent)
		if err != nil {
			return "", err
		}
		project = cmp.Or(project, pc.ProjectKey)
	}
	if project == "" {
		if project, err = s.taskAIDefaultProject(ctx, userID); err != nil {
			return "", err
		}
	}
	p, err := s.Tasks.ProjectByKey(ctx, userID, project)
	if err != nil {
		return "", err
	}
	nt := tasks.NewTask{Title: in.Title, Description: strings.TrimSpace(in.Description), ResponsibleID: in.Responsible, Priority: 1,
		Deadline: deadline, ParentKey: parent, Checklist: taskAIItems(in.Checklist)}
	if in.Important {
		nt.Priority = 2
	}
	key, err := s.Tasks.CreateTask(ctx, userID, p, nt)
	if err != nil {
		return "", err
	}
	return toolJSON(map[string]string{"key": key, "url": "/t/" + key})
}

// taskAIDefaultProject is the project of a new task named by nothing: the one the employee last put a task into, or
// the first they may create tasks in.
func (s *Server) taskAIDefaultProject(ctx context.Context, userID string) (string, error) {
	var key string
	err := s.DB.QueryRow(ctx, `SELECT p.key FROM tasks t JOIN projects p ON p.id = t.project_id
		JOIN project_members pm ON pm.project_id = p.id AND pm.user_id = $1
		WHERE t.creator_id = $1 AND NOT p.archived ORDER BY t.created_at DESC LIMIT 1`, userID).Scan(&key)
	if err == nil {
		return key, nil
	}
	list, err := s.Tasks.ListProjects(ctx, userID)
	if err != nil {
		return "", err
	}
	for _, p := range list {
		if p.CanCreate() && p.MyRole != "admin" {
			return p.Key, nil
		}
	}
	return "", i18n.E("err.ai_project_required")
}

func (s *Server) taskAIChecklist(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct {
		Key, Title string
		Items      []string
	}
	_ = json.Unmarshal(raw, &in)
	items := taskAIItems(in.Items)
	if len(items) == 0 {
		return "", i18n.E("err.ai_text_required")
	}
	c, err := s.taskAICard(ctx, userID, in.Key)
	if err != nil {
		return "", err
	}
	if _, err := s.Tasks.AddChecklistWith(ctx, userID, c.ID, cmp.Or(strings.TrimSpace(in.Title), "CoPilot"), items); err != nil {
		return "", err
	}
	return toolJSON(map[string]any{"key": c.Key(), "items": len(items), "url": "/t/" + c.Key() + "#checklist"})
}

func (s *Server) taskAIComment(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct{ Key, Text string }
	_ = json.Unmarshal(raw, &in)
	if strings.TrimSpace(in.Text) == "" {
		return "", i18n.E("err.ai_text_required")
	}
	c, err := s.taskAICard(ctx, userID, in.Key)
	if err != nil {
		return "", err
	}
	if s.Matrix == nil {
		_, err = s.Tasks.PostComment(ctx, userID, c.ID, strings.TrimSpace(in.Text))
	} else {
		_, err = s.Matrix.PostTaskReply(ctx, userID, c.ID, "", strings.TrimSpace(in.Text))
		err = s.messengerErr(err)
	}
	if err != nil {
		return "", err
	}
	return toolJSON(map[string]string{"key": c.Key(), "url": "/t/" + c.Key() + "#comments"})
}

func (s *Server) taskAIDeadline(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct{ Key, Deadline string }
	_ = json.Unmarshal(raw, &in)
	d, err := taskAIDate(in.Deadline)
	if err != nil {
		return "", err
	}
	c, err := s.taskAICard(ctx, userID, in.Key)
	if err != nil {
		return "", err
	}
	if err := s.Tasks.SetDeadline(ctx, userID, c.ID, d); err != nil {
		return "", err
	}
	return toolJSON(map[string]string{"key": c.Key(), "deadline": taskAIWhen(d), "url": "/t/" + c.Key()})
}

func (s *Server) taskAIStatus(ctx context.Context, userID string, raw json.RawMessage) (string, error) {
	var in struct{ Key, Action string }
	_ = json.Unmarshal(raw, &in)
	if !slices.Contains(taskAIActions, in.Action) {
		return "", i18n.E("err.bad_form")
	}
	c, err := s.taskAICard(ctx, userID, in.Key)
	if err != nil {
		return "", err
	}
	if err := s.Tasks.ApplyAction(ctx, userID, c.ID, tasks.Action(in.Action)); err != nil {
		return "", err
	}
	t, err := s.Tasks.TaskByID(ctx, c.ID)
	if err != nil {
		return "", err
	}
	return toolJSON(map[string]string{"key": c.Key(), "status": string(t.Status), "url": "/t/" + c.Key()})
}

// aiPane is the pane of CoPilot of a section (templates/parts/aipane.html, static/js/aipane.js).
type aiPane struct {
	Base    string // the pane's address: GET — the pane, POST — a message, …/{run}/delete, …/{run}/{action}
	On      bool   // an AI connection to work through
	Run     ai.Run // the conversation shown (no ID — a new one)
	Runs    []ai.Run
	Hello   string    // the greeting of a new conversation
	Quick   []aiQuick // the quick questions under the conversation
	Holder  string    // the placeholder of the box
	Items   []aiItem
	Error   string
	Pending bool // an action waits for «Подтвердить»
}

// aiQuick is a quick question: Say is sent at once; with Fill it waits in the box for the employee to finish it.
type aiQuick struct {
	Icon, Label, Say string
	Fill             bool
	Open             bool // only with a task open
}

// aiItem is a turn of the conversation: the employee's message or the agent's answer with its actions.
type aiItem struct {
	Me   bool
	Text string
	Acts []aiAct
}

// aiAct is an action of the agent as the pane shows it: the tool's name, what it does (lines) and, done, its link.
type aiAct struct {
	ai.Action
	Icon, Title string
	Lines       []string
	URL         string
}

// aiRuns is how many past conversations «Разговоры» lists.
const aiRuns = 15

// aiPaneOf makes the pane of an agent's conversation; act describes each action of the conversation.
func (s *Server) aiPaneOf(r *http.Request, userID, agent string, run ai.Run, err error, act func(*aiAct)) (aiPane, error) {
	ctx := r.Context()
	p := aiPane{Run: run}
	if _, _, e := s.AI.For(ctx, userID); e == nil {
		p.On = true
	} else if !errors.Is(e, ai.ErrNone) {
		return p, e
	}
	var e error
	if p.Runs, e = s.AI.Runs(ctx, userID, agent, aiRuns); e != nil {
		return p, e
	}
	if err != nil {
		p.Error = s.errText(r, err)
	} else if run.Status == ai.RunError {
		p.Error = s.I18n.Err(s.lang(r), run.Err())
	}
	for _, m := range run.Messages {
		if m.Role == "user" {
			if m.Text != "" {
				p.Items = append(p.Items, aiItem{Me: true, Text: m.Text})
			}
			continue
		}
		it := aiItem{Text: m.Text}
		for _, c := range m.Calls {
			a := aiAct{Action: run.Action(c.ID)}
			if a.ID == "" {
				continue
			}
			var res struct{ URL string }
			if a.Status == ai.ActDone && json.Unmarshal([]byte(a.Result), &res) == nil {
				a.URL = res.URL
			}
			act(&a)
			p.Pending = p.Pending || a.Status == ai.ActPending
			it.Acts = append(it.Acts, a)
		}
		if it.Text != "" || len(it.Acts) > 0 {
			p.Items = append(p.Items, it)
		}
	}
	return p, nil
}

// aiRender writes the pane alone, for the script.
func (s *Server) aiRender(w http.ResponseWriter, r *http.Request, p aiPane) {
	var buf bytes.Buffer
	if e := s.pages[s.lang(r)]["tasks"].ExecuteTemplate(&buf, "aipane", p); e != nil {
		s.jsonErr(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// taskAIIcons are the icons of the agent's tools.
var taskAIIcons = map[string]string{"create_task": "hierarchy-2", "add_checklist": "list-check", "add_comment": "message-circle", "set_deadline": "calendar-event",
	"change_status": "player-play"}

// taskAIAct describes an action of the agent of tasks: what the new task is, which items, the message, the deadline.
func (s *Server) taskAIAct(r *http.Request, userID string) func(*aiAct) {
	lang := s.lang(r)
	names := map[string]string{}
	return func(a *aiAct) {
		a.Title = s.I18n.T(lang, "tasks.ai.tool."+a.Tool)
		a.Icon = cmp.Or(taskAIIcons[a.Tool], "check")
		var in struct {
			taskAINew
			Key, Text, Action string
			Items             []string
		}
		_ = json.Unmarshal(a.Args, &in)
		key := strings.ToUpper(strings.TrimSpace(in.Key))
		switch a.Tool {
		case "create_task":
			a.Lines = append(a.Lines, "«"+strings.TrimSpace(in.Title)+"»")
			if p := strings.ToUpper(strings.TrimSpace(in.Parent)); p != "" {
				a.Lines = append(a.Lines, s.I18n.T(lang, "tasks.ai.subtask_of", p))
			}
			if in.Responsible != "" && in.Responsible != userID {
				if _, ok := names[in.Responsible]; !ok {
					var n string
					_ = s.DB.QueryRow(r.Context(), `SELECT name FROM users WHERE id::text = $1`, in.Responsible).Scan(&n)
					names[in.Responsible] = n
				}
				a.Lines = append(a.Lines, s.I18n.T(lang, "tasks.ai.responsible", cmp.Or(names[in.Responsible], in.Responsible)))
			}
			if in.Deadline != "" {
				a.Lines = append(a.Lines, s.I18n.T(lang, "tasks.ai.deadline", in.Deadline))
			}
			if in.Important {
				a.Lines = append(a.Lines, s.I18n.T(lang, "tasks.ai.important"))
			}
			if d := strings.TrimSpace(in.Description); d != "" {
				a.Lines = append(a.Lines, aiClip(d, 300))
			}
			for _, it := range taskAIItems(in.Checklist) {
				a.Lines = append(a.Lines, "☐ "+it)
			}
		case "add_checklist":
			a.Lines = append(a.Lines, key+" · «"+cmp.Or(strings.TrimSpace(in.Title), "CoPilot")+"»")
			for _, it := range taskAIItems(in.Items) {
				a.Lines = append(a.Lines, "☐ "+it)
			}
		case "add_comment":
			a.Lines = append(a.Lines, key, strings.TrimSpace(in.Text))
		case "set_deadline":
			if in.Deadline == "" {
				a.Lines = append(a.Lines, key+" · "+s.I18n.T(lang, "tasks.ai.no_deadline"))
			} else {
				a.Lines = append(a.Lines, key+" · "+in.Deadline)
			}
		case "change_status":
			a.Lines = append(a.Lines, key+" · "+s.I18n.T(lang, "action."+in.Action))
		case "read_task":
			a.Title = s.I18n.T(lang, "tasks.ai.tool.read_task", key)
		}
	}
}

// taskAIPaneOf is the pane of the agent of tasks.
func (s *Server) taskAIPaneOf(r *http.Request, userID string, run ai.Run, err error) (aiPane, error) {
	p, e := s.aiPaneOf(r, userID, "tasks", run, err, s.taskAIAct(r, userID))
	lang := s.lang(r)
	p.Base = "/tasks/agent"
	p.Hello = s.I18n.T(lang, "tasks.ai.hello")
	p.Holder = s.I18n.T(lang, "tasks.ai.placeholder")
	p.Quick = []aiQuick{
		{Icon: "flame", Label: s.I18n.T(lang, "tasks.ai.hot"), Say: s.I18n.T(lang, "tasks.ai.q.hot")},
		{Icon: "file-text", Label: s.I18n.T(lang, "tasks.ai.summary"), Say: s.I18n.T(lang, "tasks.ai.q.summary"), Open: true},
		{Icon: "hierarchy-2", Label: s.I18n.T(lang, "tasks.ai.split"), Say: s.I18n.T(lang, "tasks.ai.q.split"), Open: true},
		{Icon: "plus", Label: s.I18n.T(lang, "tasks.ai.new"), Say: s.I18n.T(lang, "tasks.ai.q.new"), Fill: true},
	}
	return p, e
}

func (s *Server) taskAIRender(w http.ResponseWriter, r *http.Request, sess auth.Session, run ai.Run, err error) {
	p, e := s.taskAIPaneOf(r, sess.UserID, run, err)
	if e != nil {
		s.jsonErr(w, r, e)
		return
	}
	s.aiRender(w, r, p)
}

// taskAIContext is the request's context for the agent: the task open beside the pane.
func taskAIContext(r *http.Request) context.Context {
	return context.WithValue(r.Context(), taskAIReq{}, taskAIOpen{r: r, key: r.FormValue("task")})
}

// taskAIOn: the pane is shown — the AI is set up, «Настройки CoPilot» let the employee use it in tasks, not a guest.
func (s *Server) taskAIOn(r *http.Request, userID string) bool { return s.aiOn(r.Context(), userID, "tasks") }

// taskAgentPane is the pane alone, for the script: ?run= — a past conversation.
func (s *Server) taskAgentPane(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskAIOn(r, sess.UserID) {
		http.NotFound(w, r)
		return
	}
	var run ai.Run
	if id := r.URL.Query().Get("run"); id != "" {
		var err error
		if run, err = s.AI.GetRun(r.Context(), sess.UserID, id); err != nil || run.Agent != "tasks" {
			run = ai.Run{}
		}
	}
	s.taskAIRender(w, r, sess, run, nil)
}

// taskAgentSay sends the employee's message (text) to the agent: run — the conversation going on ("" — a new one),
// task — the key of the task open.
func (s *Server) taskAgentSay(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskAIOn(r, sess.UserID) {
		http.NotFound(w, r)
		return
	}
	run, err := s.AI.Say(taskAIContext(r), sess.UserID, s.taskAgent(), r.FormValue("run"), r.FormValue("text"))
	if err != nil { // the conversation as it is, with the error under it
		if id := cmp.Or(run.ID, r.FormValue("run")); id != "" {
			run, _ = s.AI.GetRun(r.Context(), sess.UserID, id)
		}
	}
	s.taskAIRender(w, r, sess, run, err)
}

// taskAgentDecide confirms (yes=1) or rejects an action the agent waits with.
func (s *Server) taskAgentDecide(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskAIOn(r, sess.UserID) {
		http.NotFound(w, r)
		return
	}
	id := r.PathValue("run")
	run, err := s.AI.Decide(taskAIContext(r), sess.UserID, s.taskAgent(), id, r.PathValue("action"), r.FormValue("yes") == "1")
	if run.ID == "" {
		run, _ = s.AI.GetRun(r.Context(), sess.UserID, id)
	}
	s.taskAIRender(w, r, sess, run, err)
}

// taskAgentDelete removes a conversation; the pane starts a new one.
func (s *Server) taskAgentDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.taskAIOn(r, sess.UserID) {
		http.NotFound(w, r)
		return
	}
	err := s.AI.DeleteRun(r.Context(), sess.UserID, r.PathValue("run"))
	s.taskAIRender(w, r, sess, ai.Run{}, err)
}

func (s *Server) apiTaskAgentRuns(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	list, err := s.AI.Runs(r.Context(), p.UserID, "tasks", 100)
	if err != nil {
		return err
	}
	out := []apiAIRun{}
	for _, run := range list {
		out = append(out, s.apiAIRunOf(r, run))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiTaskAgentRun(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	run, err := s.AI.GetRun(r.Context(), p.UserID, r.PathValue("run"))
	if err == nil && run.Agent != "tasks" {
		err = ai.ErrNotFound
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}

func (s *Server) apiTaskAgentSay(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	var in struct {
		Run  string `json:"run"`
		Text string `json:"text"`
		Task string `json:"task"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx := context.WithValue(r.Context(), taskAIReq{}, taskAIOpen{r: r, key: in.Task})
	run, err := s.AI.Say(ctx, p.UserID, s.taskAgent(), in.Run, in.Text)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}

func (s *Server) apiTaskAgentDecide(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if s.AI == nil {
		return ai.ErrNone
	}
	var in struct {
		Yes bool `json:"yes"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	run, err := s.AI.Decide(context.WithValue(r.Context(), taskAIReq{}, taskAIOpen{r: r}), p.UserID, s.taskAgent(), r.PathValue("run"), r.PathValue("action"), in.Yes)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.apiAIRunOf(r, run))
	return nil
}
