package web

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"go24/internal/apikeys"
	"go24/internal/bp"
	"go24/internal/tasks"
)

// ---------- «Бизнес-процессы» over REST, as bizproc.workflow.*, bizproc.workflow.template.* and bizproc.task.* ----------
//
// The rights are those of the pages: the templates are made and changed by the portal's administrators (they see the
// inactive ones and the steps too), anybody starts an active one; a process is seen by who started it, who has a task
// in it, who may read its CRM card (…/instances?entity=&entity_id=) and the administrators (all=1). Steps are numbered
// from 1, as «goto» of a step is.

func (s *Server) apiBPRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/bp/templates", s.api("bp:read", s.apiBPTemplates))
	mux.HandleFunc("GET /api/v1/bp/templates/{id}", s.api("bp:read", s.apiBPTemplate))
	mux.HandleFunc("POST /api/v1/bp/templates", s.api("bp:write", s.apiBPSaveTemplate))
	mux.HandleFunc("PATCH /api/v1/bp/templates/{id}", s.api("bp:write", s.apiBPSaveTemplate))
	mux.HandleFunc("DELETE /api/v1/bp/templates/{id}", s.api("bp:write", s.apiBPDeleteTemplate))
	mux.HandleFunc("POST /api/v1/bp/templates/{template}/start", s.api("bp:write", s.apiBPStart))
	mux.HandleFunc("POST /api/v1/bp/{template}/start", s.api("bp:write", s.apiBPStart)) // the first address, kept
	mux.HandleFunc("GET /api/v1/bp/instances", s.api("bp:read", s.apiBPInstances))
	mux.HandleFunc("GET /api/v1/bp/instances/{id}", s.api("bp:read", s.apiBPInstance))
	mux.HandleFunc("POST /api/v1/bp/instances/{id}/stop", s.api("bp:write", s.apiBPStop))
	mux.HandleFunc("DELETE /api/v1/bp/instances/{id}", s.api("bp:write", s.apiBPDeleteInstance))
	mux.HandleFunc("GET /api/v1/bp/tasks", s.api("bp:read", s.apiBPTasks))
	mux.HandleFunc("POST /api/v1/bp/tasks/{id}/complete", s.api("bp:write", s.apiBPComplete))
	mux.HandleFunc("POST /api/v1/bp/tasks/{id}/delegate", s.api("bp:write", s.apiBPDelegate))
}

// apiBPCtx: an administrator's key administers the processes, as the pages do (not only in the admin mode).
func (s *Server) apiBPCtx(r *http.Request, p apikeys.Principal) (context.Context, bool) {
	if s.apiAdmin(r.Context(), p.UserID) {
		return tasks.WithAdmin(r.Context()), true
	}
	return r.Context(), false
}

type apiBPTemplate struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Entity      string     `json:"entity"`  // deal, lead, contact, company, file (a document of «Общий диск») or none — a standalone process
	TypeID      string     `json:"type_id"` // of deals: of this smart process's cards
	Active      bool       `json:"active"`
	AutoCreate  bool       `json:"auto_create"` // «Автозапуск»: when a card is added
	AutoChange  bool       `json:"auto_change"` // … when it is changed
	Fields      []bp.Field `json:"fields"`      // the form filled in at the start
	Steps       []bp.Step  `json:"steps,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	URL         string     `json:"url"` // the start form in go24
}

func (s *Server) apiBPTemplateOf(t bp.Template, admin bool) apiBPTemplate {
	out := apiBPTemplate{ID: t.ID, Name: t.Name, Description: t.Description, Entity: t.Entity, TypeID: t.TypeID, Active: t.Active,
		AutoCreate: t.AutoCreate, AutoChange: t.AutoChange, Fields: t.Fields, CreatedAt: t.CreatedAt, URL: s.BaseURL + "/bp/start/" + t.ID}
	if out.Fields == nil {
		out.Fields = []bp.Field{}
	}
	if admin {
		out.Steps = t.Steps
	}
	return out
}

// apiBPTemplates: ?entity=deal|lead|contact|company|file|none, those the key's owner starts; the inactive ones — for
// administrators only.
func (s *Server) apiBPTemplates(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, admin := s.apiBPCtx(r, p)
	list, err := s.BP.Templates(r.Context(), r.URL.Query().Get("entity"), !admin)
	if err == nil {
		list, err = s.BP.Startable(ctx, p.UserID, list) // «Кто может запускать»
	}
	if err != nil {
		return err
	}
	out := []apiBPTemplate{}
	for _, t := range list {
		out = append(out, s.apiBPTemplateOf(t, admin))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) apiBPTemplate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	_, admin := s.apiBPCtx(r, p)
	t, err := s.BP.Template(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	if !t.Active && !admin {
		return bp.ErrNotFound
	}
	writeJSON(w, http.StatusOK, s.apiBPTemplateOf(t, admin))
	return nil
}

// apiBPSaveTemplate creates (POST) or changes (PATCH: what is left out stays) a template, administrators only:
// {"name", "description", "entity", "type_id", "active" (new — true), "auto_create", "auto_change", "fields": [{"key",
// "label", "kind", "required"}], "steps": [{"kind", …} as GET returns them]}.
func (s *Server) apiBPSaveTemplate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Name, Description, Entity, TypeID *string
		Active, AutoCreate, AutoChange    *bool
		Fields                            *[]bp.Field
		Steps                             *[]bp.Step
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	if _, admin := s.apiBPCtx(r, p); !admin {
		return bp.ErrForbidden
	}
	t := bp.Template{Active: true}
	status := http.StatusCreated
	if id := r.PathValue("id"); id != "" {
		var err error
		if t, err = s.BP.Template(r.Context(), id); err != nil {
			return err
		}
		status = http.StatusOK
	}
	for _, v := range []struct {
		to   *string
		from *string
	}{{&t.Name, in.Name}, {&t.Description, in.Description}, {&t.Entity, in.Entity}, {&t.TypeID, in.TypeID}} {
		if v.from != nil {
			*v.to = *v.from
		}
	}
	for _, v := range []struct {
		to   *bool
		from *bool
	}{{&t.Active, in.Active}, {&t.AutoCreate, in.AutoCreate}, {&t.AutoChange, in.AutoChange}} {
		if v.from != nil {
			*v.to = *v.from
		}
	}
	if in.Fields != nil {
		t.Fields = *in.Fields
	}
	if in.Steps != nil {
		t.Steps = *in.Steps
	}
	id, err := s.BP.SaveTemplate(r.Context(), p.UserID, t)
	if err != nil {
		return err
	}
	if t, err = s.BP.Template(r.Context(), id); err != nil {
		return err
	}
	writeJSON(w, status, s.apiBPTemplateOf(t, true))
	return nil
}

// apiBPDeleteTemplate deletes a template with its processes (administrators), as «Удалить» in the constructor does.
func (s *Server) apiBPDeleteTemplate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, admin := s.apiBPCtx(r, p); !admin {
		return bp.ErrForbidden
	}
	if _, err := s.BP.Template(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	if err := s.BP.DeleteTemplate(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type apiBPStep struct {
	N     int    `json:"n"`
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	State string `json:"state"` // done, now or "" — not yet (or skipped)
}

type apiBPLine struct {
	Step  int       `json:"step"`
	Actor string    `json:"actor"`
	Kind  string    `json:"kind"` // started, approved, rejected, seen, answered, stopped, failed, done…
	Text  string    `json:"text"`
	At    time.Time `json:"at"`
}

type apiBPInstance struct {
	ID         string            `json:"id"`
	TemplateID string            `json:"template_id"`
	Name       string            `json:"name"`
	Entity     string            `json:"entity"`
	EntityID   string            `json:"entity_id,omitempty"`
	Status     string            `json:"status"` // running, done, rejected, failed, stopped
	Step       int               `json:"step"`   // the step it is on (running)
	Values     map[string]string `json:"values"`
	StartedBy  apiUser           `json:"started_by"`
	CreatedAt  time.Time         `json:"created_at"`
	FinishedAt *time.Time        `json:"finished_at"`
	WakeAt     *time.Time        `json:"wake_at,omitempty"` // a pause ends
	URL        string            `json:"url"`
	// one process, not the list
	CanStop bool         `json:"can_stop,omitempty"`
	Fields  []bp.Field   `json:"fields,omitempty"` // the form's and the requests' fields, the values' labels
	Steps   []apiBPStep  `json:"steps,omitempty"`
	Tasks   []*apiBPTask `json:"tasks,omitempty"`
	Log     []apiBPLine  `json:"log,omitempty"`
}

func (s *Server) apiBPInstanceOf(x bp.Instance) apiBPInstance {
	out := apiBPInstance{ID: x.ID, TemplateID: x.TemplateID, Name: x.Name, Entity: x.Entity, EntityID: x.EntityID, Status: x.Status,
		Values: x.Values, StartedBy: apiUser{x.StartedBy.ID, x.StartedBy.Name}, CreatedAt: x.CreatedAt, FinishedAt: x.FinishedAt,
		WakeAt: x.WakeAt, URL: s.BaseURL + "/bp/i/" + x.ID}
	if x.Status == "running" {
		out.Step = x.Step + 1
	}
	if out.Values == nil {
		out.Values = map[string]string{}
	}
	return out
}

type apiBPTask struct {
	ID        string     `json:"id"`
	ProcessID string     `json:"process_id"`
	Process   string     `json:"process"`
	Kind      string     `json:"kind"` // approve, review or request
	Text      string     `json:"text"`
	Step      int        `json:"step"`
	User      apiUser    `json:"user"`
	StartedBy string     `json:"started_by"`
	Decision  string     `json:"decision"` // approve, reject, seen, answer; "" — waits
	Comment   string     `json:"comment"`
	Fields    []bp.Field `json:"fields,omitempty"` // request: what to fill in
	CanReject bool       `json:"can_reject,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	URL       string     `json:"url"`
}

func (s *Server) apiBPTaskOf(a bp.Approval) *apiBPTask {
	return &apiBPTask{ID: a.ID, ProcessID: a.InstanceID, Process: a.Name, Kind: a.Kind, Text: a.Text, Step: a.Step + 1,
		User: apiUser{a.User.ID, a.User.Name}, StartedBy: a.StartedBy, Decision: a.Decision, Comment: a.Comment, Fields: a.Fields,
		CanReject: a.CanReject, CreatedAt: a.CreatedAt, URL: s.BaseURL + "/bp/i/" + a.InstanceID}
}

// apiBPInstances are the processes the key's owner started (newest first, 100 at most); ?entity=&entity_id= — of a
// CRM card; all=1 (administrators) — everybody's, with ?status=&template_id=&limit=&offset=.
func (s *Server) apiBPInstances(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	q := r.URL.Query()
	var list []bp.Instance
	var err error
	switch {
	case q.Get("all") == "1":
		if _, admin := s.apiBPCtx(r, p); !admin {
			return bp.ErrForbidden
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		offset, _ := strconv.Atoi(q.Get("offset"))
		list, err = s.BP.AllInstances(r.Context(), bp.Filter{Status: q.Get("status"), TemplateID: q.Get("template_id"), Limit: limit, Offset: max(offset, 0)})
	case q.Get("entity") != "":
		list, err = s.BP.CardInstances(r.Context(), p.UserID, q.Get("entity"), q.Get("entity_id"))
	default:
		list, err = s.BP.Instances(r.Context(), p.UserID, "", "")
	}
	if err != nil {
		return err
	}
	out := []apiBPInstance{}
	for _, x := range list {
		if st := q.Get("status"); st == "" || st == x.Status {
			out = append(out, s.apiBPInstanceOf(x))
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiBPInstance is a process with its steps, tasks and history.
func (s *Server) apiBPInstance(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	return s.apiBPWriteInstance(w, r, p, r.PathValue("id"), http.StatusOK)
}

func (s *Server) apiBPWriteInstance(w http.ResponseWriter, r *http.Request, p apikeys.Principal, id string, status int) error {
	ctx, _ := s.apiBPCtx(r, p)
	x, err := s.BP.Instance(ctx, p.UserID, id)
	if err != nil {
		return err
	}
	approvals, err := s.BP.Approvals(ctx, x.ID)
	if err != nil {
		return err
	}
	lines, err := s.BP.Log(ctx, x.ID)
	if err != nil {
		return err
	}
	t, _ := s.BP.Template(ctx, x.TemplateID) // the field labels (the template may be gone)
	out := s.apiBPInstanceOf(x)
	out.CanStop, out.Fields = s.BP.CanStop(ctx, p.UserID, x), x.Form(t.Fields)
	for i, f := range out.Fields { // «Информация о сотруднике»: its fields are named by their keys
		if f.Label == "" {
			out.Fields[i].Label = f.Key
		}
	}
	out.Steps, out.Tasks, out.Log = []apiBPStep{}, []*apiBPTask{}, []apiBPLine{}
	for i, st := range x.Steps {
		out.Steps = append(out.Steps, apiBPStep{N: i + 1, Kind: st.Kind, Text: st.Text, State: x.State(i)})
	}
	for _, a := range approvals {
		out.Tasks = append(out.Tasks, s.apiBPTaskOf(a))
	}
	for _, l := range lines {
		out.Log = append(out.Log, apiBPLine{Step: l.Step + 1, Actor: l.Actor, Kind: l.Kind, Text: l.Text, At: l.At})
	}
	writeJSON(w, status, out)
	return nil
}

// apiBPStart starts a process as the key's owner: {"entity_id": … (a template of a CRM card), "values": {…}}.
func (s *Server) apiBPStart(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		EntityID string            `json:"entity_id"`
		Values   map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, _ := s.apiBPCtx(r, p)
	id, err := s.BP.Start(ctx, p.UserID, r.PathValue("template"), in.EntityID, in.Values)
	if err != nil {
		return err
	}
	return s.apiBPWriteInstance(w, r, p, id, http.StatusCreated)
}

// apiBPStop: «Остановить» with {"reason"} — who started it, who may change its CRM card, administrators.
func (s *Server) apiBPStop(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ Reason string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, _ := s.apiBPCtx(r, p)
	if err := s.BP.Stop(ctx, p.UserID, r.PathValue("id"), in.Reason); err != nil {
		return err
	}
	return s.apiBPWriteInstance(w, r, p, r.PathValue("id"), http.StatusOK)
}

// apiBPDeleteInstance removes a process with its history (administrators).
func (s *Server) apiBPDeleteInstance(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	ctx, _ := s.apiBPCtx(r, p)
	if err := s.BP.DeleteInstance(ctx, r.PathValue("id")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// apiBPTasks wait for the key owner's decision, the oldest first.
func (s *Server) apiBPTasks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.BP.MyApprovals(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	out := []*apiBPTask{}
	for _, a := range list {
		out = append(out, s.apiBPTaskOf(a))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// apiBPComplete decides on a task: {"decision": "approve" | "reject" | "seen" (a review) | "answer" (a request, with
// "values": {key: …}), "comment"}; the process as it is then.
// apiBPDelegate hands the key owner's task to the employee {"to": id}, as bizproc.task.delegate (an administrator's key —
// anyone's task).
func (s *Server) apiBPDelegate(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct{ To string }
	if err := readJSON(r, &in); err != nil {
		return err
	}
	ctx, _ := s.apiBPCtx(r, p)
	process, err := s.BP.Delegate(ctx, p.UserID, r.PathValue("id"), in.To)
	if err != nil {
		return err
	}
	return s.apiBPWriteInstance(w, r, p, process, http.StatusOK)
}

func (s *Server) apiBPComplete(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	var in struct {
		Decision, Comment string
		Values            map[string]string
	}
	if err := readJSON(r, &in); err != nil {
		return err
	}
	mine, err := s.BP.MyApprovals(r.Context(), p.UserID)
	if err != nil {
		return err
	}
	var process string
	for _, a := range mine {
		if a.ID == r.PathValue("id") {
			process = a.InstanceID
		}
	}
	if process == "" {
		return bp.ErrNotFound
	}
	if in.Decision == "answer" {
		err = s.BP.Answer(r.Context(), p.UserID, r.PathValue("id"), in.Values, in.Comment)
	} else {
		err = s.BP.Decide(r.Context(), p.UserID, r.PathValue("id"), in.Decision, in.Comment)
	}
	if err != nil {
		return err
	}
	return s.apiBPWriteInstance(w, r, p, process, http.StatusOK)
}
