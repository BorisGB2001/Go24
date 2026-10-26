// Package bp is «Бизнес-процессы» of go24: a template is a form and a list of steps; a process started from it runs
// the steps one by one — it waits on an approval, a review or a request for more information («Запрос дополнительной
// информации») until the people decide or answer, and on a pause until its time
// (the worker wakes it). A condition, a «go to» and a rejected approval may jump forward to another step (or the end),
// so a list of steps has the branches of the designer. A rejected approval with no jump ends the process.
package bp

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/feed"
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

// StepKinds in the order the constructor offers them.
var StepKinds = []string{"approve", "review", "request", "task", "notify", "chat", "mail", "if", "goto", "parallel", "set", "stage", "update",
	"create", "absence", "wait", "webhook"}

// Modes of an approval by several people: the first decision decides, everyone must approve, or the majority.
var Modes = []string{"any", "all", "majority"}

// Ops of a condition.
var Ops = []string{"eq", "ne", "gt", "lt", "contains", "empty", "filled"}

// FieldKinds of a template's form.
var FieldKinds = []string{"string", "text", "number", "date"}

// Field of the form a process is started with.
type Field struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required,omitempty"`
}

// Step of a template. To: "starter", "manager" (the starter's manager), "responsible" (of the CRM card) or a user id.
type Step struct {
	Kind     string   `json:"kind"`
	Text     string   `json:"text,omitempty"`     // approval question, task title, notification
	To       string   `json:"to,omitempty"`       // approve, task, notify
	Project  string   `json:"project,omitempty"`  // task
	Days     int      `json:"days,omitempty"`     // task deadline, pause length
	Stage    string   `json:"stage,omitempty"`    // stage: the CRM stage id
	URL      string   `json:"url,omitempty"`      // webhook: where the process's data go (n8n's Webhook node)
	Users    []string `json:"users,omitempty"`    // approve, review: more people besides To
	Mode     string   `json:"mode,omitempty"`     // approve: one of Modes ("" — any)
	Field    string   `json:"field,omitempty"`    // if, set: the form's field
	Op       string   `json:"op,omitempty"`       // if: one of Ops
	Value    string   `json:"value,omitempty"`    // if: compared with; set: the new value ({…} filled)
	Goto     int      `json:"goto,omitempty"`     // if (when false), goto, approve and request (when rejected): the step's number from 1; 0 — the end; parallel: the join
	Branches []int    `json:"branches,omitempty"` // parallel: the numbers of the steps the 2nd, 3rd… branches start with (the 1st — the next step)
	Fields   []Field  `json:"fields,omitempty"`   // request: what the person fills in; the values join the process's form
	Reject   bool     `json:"reject,omitempty"`   // request: the person may reject instead of answering, as an approval
	From     string   `json:"from,omitempty"`     // chat, mail: who writes (as To; "" — the starter), mail — from their mailbox
	Email    string   `json:"email,omitempty"`    // mail: more addresses ({…} filled)
	Subject  string   `json:"subject,omitempty"`  // mail
	Entity   string   `json:"entity,omitempty"`   // create: the kind of card
	Sets     []Assign `json:"sets,omitempty"`     // update, create: the card's fields
	Absence  string   `json:"absence,omitempty"`  // absence: the reason, one of company.AbsenceKinds
	Start    string   `json:"start,omitempty"`    // absence: the form's date field it begins on
	End      string   `json:"end,omitempty"`      // absence: … and ends on
}

// Assign is a field of a CRM card a step sets: a key of CardFields or the name of an own field, and the value ({…} filled).
type Assign struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// People a step goes to: To and then Users (approve, review, request).
func (st Step) People() []string {
	out := []string{st.To}
	for _, u := range st.Users {
		if u != "" {
			out = append(out, u)
		}
	}
	return out
}

// Ranges are the branches of the parallel step p as [first, end) indexes of the steps: the 1st begins after p, each
// ends where the next begins, the last — where they join (the step's Goto; the end of the process when 0).
func Ranges(steps []Step, p int) [][2]int {
	join := jump(Instance{Steps: steps}, steps[p].Goto)
	starts := []int{p + 1}
	for _, n := range steps[p].Branches {
		starts = append(starts, n-1)
	}
	out := make([][2]int, len(starts))
	for k, a := range starts {
		end := join
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		out[k] = [2]int{a, end}
	}
	return out
}

// AllFields are the form's fields and then those of the requests among the steps: everything a process may hold.
func AllFields(form []Field, steps []Step) []Field {
	out := append([]Field(nil), form...)
	for _, st := range steps {
		if st.Kind == "request" {
			out = append(out, st.Fields...)
		}
	}
	return out
}

type Template struct {
	ID, Name, Description, Entity string
	TypeID                        string // a template of deals: of this smart process's cards ("" — of deals)
	Fields                        []Field
	Steps                         []Step
	Active                        bool
	AutoCreate, AutoChange        bool   // «Автозапуск»: when a card is added, when it is changed
	Std                           string // the standard template it is installed as (one of StdKeys; not loaded)
	CreatedAt                     time.Time
}

type Store struct {
	BaseURL string // go24's address, for the links a webhook step sends
	db      *pgxpool.Pool
	Tasks   *tasks.Store
	CRM     *crm.Store
	Company *company.Store
	// Chat writes a message from one employee to another in the messenger («Сообщение в чат»; nil — the step fails)
	Chat func(ctx context.Context, fromID, toID, text string) error
	// Feed: a standalone process started by hand is a post of its starter (nil — no posts)
	Feed *feed.Store
}

func NewStore(db *pgxpool.Pool, t *tasks.Store, c *crm.Store, co *company.Store) *Store {
	return &Store{db: db, Tasks: t, CRM: c, Company: co}
}

func scanTemplate(r pgx.CollectableRow) (Template, error) {
	var t Template
	var f, s []byte
	if err := r.Scan(&t.ID, &t.Name, &t.Description, &t.Entity, &t.TypeID, &f, &s, &t.Active, &t.AutoCreate, &t.AutoChange,
		&t.CreatedAt); err != nil {
		return t, err
	}
	if err := json.Unmarshal(f, &t.Fields); err != nil {
		return t, err
	}
	return t, json.Unmarshal(s, &t.Steps)
}

const templateSQL = `SELECT id, name, description, entity, coalesce(type_id::text, ''), fields, steps, active, auto_create, auto_change,
	created_at FROM bp_templates`

// Templates are the templates (of an entity; "" — all), active ones only when active is set.
func (s *Store) Templates(ctx context.Context, entity string, active bool) ([]Template, error) {
	rows, err := s.db.Query(ctx, templateSQL+` WHERE ($1 = '' OR entity = $1) AND (active OR NOT $2) ORDER BY lower(name)`, entity, active)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTemplate)
}

func (s *Store) Template(ctx context.Context, id string) (Template, error) {
	rows, err := s.db.Query(ctx, templateSQL+` WHERE id::text = $1`, id)
	if err != nil {
		return Template{}, err
	}
	list, err := pgx.CollectRows(rows, scanTemplate)
	if err != nil {
		return Template{}, err
	}
	if len(list) == 0 {
		return Template{}, ErrNotFound
	}
	return list[0], nil
}

func checkTemplate(t *Template) error {
	t.Name, t.Description = strings.TrimSpace(t.Name), strings.TrimSpace(t.Description)
	if t.Name == "" || len([]rune(t.Name)) > 200 || len([]rune(t.Description)) > 5000 {
		return i18n.E("err.bp_name")
	}
	if t.Entity == "" {
		t.Entity = "none"
	}
	if t.Entity != "none" && !crm.Entities[t.Entity] || t.TypeID != "" && t.Entity != "deal" {
		return i18n.E("err.bad_form")
	}
	if t.Entity == "none" { // a standalone process has no card to start by
		t.AutoCreate, t.AutoChange = false, false
	}
	seen := map[string]bool{}
	kinds := map[string]string{} // the fields' kinds
	addFields := func(list []Field) error {
		for _, f := range list {
			if f.Key == "" || seen[f.Key] || strings.ContainsAny(f.Key, " {}") || f.Label == "" || !contains(FieldKinds, f.Kind) {
				return i18n.E("err.bp_field")
			}
			seen[f.Key], kinds[f.Key] = true, f.Kind
		}
		return nil
	}
	if err := addFields(t.Fields); err != nil {
		return err
	}
	for i, st := range t.Steps {
		// a request's fields are of the process from this step on: a condition after it may check them
		if st.Kind == "request" && len(st.Fields) == 0 || st.Kind != "request" && (len(st.Fields) > 0 || st.Reject) ||
			st.Kind == "request" && st.Goto != 0 && !st.Reject {
			return i18n.E("err.bp_step")
		}
		if err := addFields(st.Fields); err != nil {
			return err
		}
		if !contains(StepKinds, st.Kind) || len([]rune(st.Text)) > 1000 || st.Days < 0 || st.Days > 365 {
			return i18n.E("err.bp_step")
		}
		if (st.Kind == "task" && st.Project == "") || (st.Kind == "stage" && (st.Stage == "" || t.Entity != "deal" && t.Entity != "lead")) ||
			(st.Kind == "wait" && st.Days == 0) || (st.Text == "" && !contains([]string{"wait", "stage", "webhook", "if", "goto", "parallel", "set", "update", "mail", "absence"}, st.Kind)) ||
			(st.Kind == "webhook" && hooks.CheckURL(st.URL) != nil) {
			return i18n.E("err.bp_step")
		}
		if st.Kind == "absence" && (!contains(company.AbsenceKinds, st.Absence) || kinds[st.Start] != "date" || kinds[st.End] != "date") {
			return i18n.E("err.bp_step")
		}
		if err := checkCardStep(t.Entity, st); err != nil {
			return err
		}
		if (st.Kind == "if" || st.Kind == "set") && !seen[st.Field] || st.Kind == "if" && !contains(Ops, st.Op) ||
			st.Mode != "" && !contains(Modes, st.Mode) || len(st.Users) > 50 || len([]rune(st.Value)) > 1000 {
			return i18n.E("err.bp_step")
		}
		// jumps go forward only, so a process cannot loop
		if st.Goto != 0 && (st.Goto <= i+1 || st.Goto > len(t.Steps)) || st.Kind == "goto" && st.Goto == 0 && i == len(t.Steps)-1 {
			return i18n.E("err.bp_goto", i+1)
		}
	}
	if len(t.Steps) == 0 {
		return i18n.E("err.bp_no_steps")
	}
	return checkParallel(t.Steps)
}

// checkParallel: a parallel step's branches are of a step at least, follow one another before the join and hold no
// parallel step; a jump inside a branch stays in it (0 — to its end), a jump from before the step does not go into it.
func checkParallel(steps []Step) error {
	for p, st := range steps {
		if st.Kind != "parallel" {
			if len(st.Branches) > 0 {
				return i18n.E("err.bp_step")
			}
			continue
		}
		join := len(steps) + 1
		if st.Goto != 0 {
			join = st.Goto
		}
		prev := p + 2
		if len(st.Branches) == 0 || len(st.Branches) > 9 || prev >= join {
			return i18n.E("err.bp_parallel", p+1)
		}
		for _, n := range st.Branches {
			if n <= prev || n >= join {
				return i18n.E("err.bp_parallel", p+1)
			}
			prev = n
		}
		for _, r := range Ranges(steps, p) {
			for j := r[0]; j < r[1]; j++ {
				if steps[j].Kind == "parallel" || steps[j].Goto > r[1]+1 { // a jump to 0 or past it ends the branch
					return i18n.E("err.bp_parallel", j+1)
				}
			}
		}
		for j := 0; j < p; j++ {
			if steps[j].Goto > p+1 && steps[j].Goto < join {
				return i18n.E("err.bp_parallel", j+1)
			}
		}
	}
	return nil
}

// optional is NULL for "".
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// SaveTemplate creates (ID "") or changes a template (administrators; checked by the web layer).
func (s *Store) SaveTemplate(ctx context.Context, userID string, t Template) (string, error) {
	if err := checkTemplate(&t); err != nil {
		return "", err
	}
	for i, st := range t.Steps { // own fields are the CRM's: a name not among them is a mistake now, not when the process runs
		entity := t.Entity
		if st.Kind == "create" {
			entity = st.Entity
		}
		for _, a := range st.Sets {
			if _, err := s.cardField(ctx, entity, a.Field); err != nil {
				return "", i18n.E("err.bp_card_field", i+1, a.Field)
			}
		}
	}
	f, _ := json.Marshal(t.Fields)
	st, _ := json.Marshal(t.Steps)
	if t.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO bp_templates (name, description, entity, fields, steps, active, created_by, type_id,
			auto_create, auto_change) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			t.Name, t.Description, t.Entity, f, st, t.Active, userID, optional(t.TypeID), t.AutoCreate, t.AutoChange).Scan(&t.ID)
		return t.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE bp_templates SET name = $2, description = $3, entity = $4, fields = $5, steps = $6, active = $7,
		type_id = $8, auto_create = $9, auto_change = $10 WHERE id::text = $1`, t.ID, t.Name, t.Description, t.Entity, f, st, t.Active,
		optional(t.TypeID), t.AutoCreate, t.AutoChange)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return t.ID, err
}

func (s *Store) DeleteTemplate(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM bp_templates WHERE id::text = $1`, id)
	return err
}

// ---------- processes ----------

type Instance struct {
	ID, TemplateID, Name, Entity, EntityID, Status string
	Steps                                          []Step
	Values                                         map[string]string
	Step                                           int
	WakeAt                                         *time.Time
	StartedBy                                      company.Person
	CreatedAt                                      time.Time
	FinishedAt                                     *time.Time
	Branches                                       []int // in a parallel step: the step of each branch, its end when it is over

	branch, end int // the step's branch running (from 1; 0 — none) and where it ends (len(Steps) — the process's end)
}

// Current: the process runs or waits on the step (of a branch when parallel).
func (x Instance) Current(i int) bool {
	if x.Status != "running" {
		return false
	}
	if i == x.Step {
		return true
	}
	if len(x.Branches) == 0 || x.Step >= len(x.Steps) {
		return false
	}
	for k, r := range Ranges(x.Steps, x.Step) {
		if k < len(x.Branches) && x.Branches[k] == i && i < r[1] {
			return true
		}
	}
	return false
}

// State of a step for the list: "done", "now" or "".
func (x Instance) State(i int) string {
	if x.Current(i) {
		return "now"
	}
	if i < x.Step {
		return "done"
	}
	if len(x.Branches) > 0 && x.Step < len(x.Steps) {
		for k, r := range Ranges(x.Steps, x.Step) {
			if k < len(x.Branches) && i >= r[0] && i < r[1] && i < x.Branches[k] {
				return "done"
			}
		}
	}
	return ""
}

// Line of a process's history.
type Line struct {
	Step  int
	Actor string
	Kind  string
	Text  string
	At    time.Time
}

// Approval is a person's decision (or their «Ознакомлен» for a review, Kind "review", or their answer to a request,
// Kind "request") a process waits for.
type Approval struct {
	ID, InstanceID, Name, Kind, Text, Decision, Comment string
	Step                                                int
	User                                                company.Person
	StartedBy                                           string
	CreatedAt                                           time.Time
	Fields                                              []Field // request: what to fill in
	CanReject                                           bool    // request: «Отклонить» too
}

const instanceSQL = `SELECT i.id, i.template_id::text, i.name, i.entity, coalesce(i.entity_id::text, ''), i.status, i.steps, i.values,
		i.step, i.wake_at, u.id, u.name, i.created_at, i.finished_at, ARRAY(SELECT b.step FROM bp_branches b WHERE b.instance_id = i.id ORDER BY b.n)
	FROM bp_instances i JOIN users u ON u.id = i.started_by`

func scanInstance(r pgx.CollectableRow) (Instance, error) {
	var x Instance
	var st, vals []byte
	if err := r.Scan(&x.ID, &x.TemplateID, &x.Name, &x.Entity, &x.EntityID, &x.Status, &st, &vals, &x.Step, &x.WakeAt,
		&x.StartedBy.ID, &x.StartedBy.Name, &x.CreatedAt, &x.FinishedAt, &x.Branches); err != nil {
		return x, err
	}
	if err := json.Unmarshal(st, &x.Steps); err != nil {
		return x, err
	}
	x.end = len(x.Steps)
	return x, json.Unmarshal(vals, &x.Values)
}

func (s *Store) instance(ctx context.Context, id string) (Instance, error) {
	rows, err := s.db.Query(ctx, instanceSQL+` WHERE i.id::text = $1`, id)
	if err != nil {
		return Instance{}, err
	}
	list, err := pgx.CollectRows(rows, scanInstance)
	if err != nil {
		return Instance{}, err
	}
	if len(list) == 0 {
		return Instance{}, ErrNotFound
	}
	return list[0], nil
}

// Instance is a process the user may see: they started it, decide in it, or administer.
func (s *Store) Instance(ctx context.Context, userID, id string) (Instance, error) {
	x, err := s.instance(ctx, id)
	if err != nil {
		return x, err
	}
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT $3 OR $2 = (SELECT started_by::text FROM bp_instances WHERE id::text = $1)
		OR EXISTS (SELECT 1 FROM bp_approvals WHERE instance_id::text = $1 AND user_id::text = $2)`, id, userID, tasks.IsAdmin(ctx)).Scan(&ok); err != nil {
		return x, err
	}
	if !ok {
		return x, ErrForbidden
	}
	return x, nil
}

// Instances started by the user, or (entity, entityID) of a CRM card, newest first.
func (s *Store) Instances(ctx context.Context, userID, entity, entityID string) ([]Instance, error) {
	rows, err := s.db.Query(ctx, instanceSQL+` WHERE ($2 = '' AND i.started_by::text = $1) OR ($2 <> '' AND i.entity = $2 AND i.entity_id::text = $3)
		ORDER BY i.created_at DESC LIMIT 100`, userID, entity, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanInstance)
}

// CardInstances are the processes of a CRM card, for one who may read it.
func (s *Store) CardInstances(ctx context.Context, userID, entity, entityID string) ([]Instance, error) {
	if !crm.Entities[entity] {
		return nil, i18n.E("err.bad_form")
	}
	if err := s.cardCheck(ctx, userID, entity, entityID); err != nil {
		return nil, err
	}
	return s.Instances(ctx, userID, entity, entityID)
}

// Statuses of a process, for the filter.
var Statuses = []string{"running", "done", "rejected", "failed", "stopped"}

// Filter of all the processes (administrators): the status and the template, "" — any.
type Filter struct {
	Status, TemplateID string
	Limit, Offset      int
}

// AllInstances are the processes of everybody («Бизнес-процессы → Запущенные»), newest first.
func (s *Store) AllInstances(ctx context.Context, f Filter) ([]Instance, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	rows, err := s.db.Query(ctx, instanceSQL+` WHERE ($1 = '' OR i.status = $1) AND ($2 = '' OR i.template_id::text = $2)
		ORDER BY i.created_at DESC LIMIT $3 OFFSET $4`, f.Status, f.TemplateID, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanInstance)
}

// CanStop: the person may stop the process — an administrator, who started it, or who may change its CRM card.
func (s *Store) CanStop(ctx context.Context, userID string, x Instance) bool {
	if x.Status != "running" {
		return false
	}
	if tasks.IsAdmin(ctx) || x.StartedBy.ID == userID {
		return true
	}
	return x.Entity != "none" && s.cardRight(ctx, userID, x.Entity, "write", x.EntityID) == nil
}

// Stop ends a running process as stopped, with the reason in its history; its waiting approvals are not asked any more.
func (s *Store) Stop(ctx context.Context, userID, id, reason string) error {
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) > 5000 {
		return i18n.E("err.bp_field")
	}
	x, err := s.instance(ctx, id)
	if err != nil {
		return err
	}
	if !s.CanStop(ctx, userID, x) {
		if x.Status != "running" {
			return i18n.E("err.bp_not_running")
		}
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `UPDATE bp_instances SET status = 'stopped', finished_at = now(), wake_at = NULL
		WHERE id::text = $1 AND status = 'running'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 { // finished meanwhile
		return i18n.E("err.bp_not_running")
	}
	s.log(ctx, id, x.Step, userID, "stopped", reason)
	if x.StartedBy.ID != userID {
		s.notify(ctx, x.StartedBy.ID, userID, id, "⏹ "+x.Name)
	}
	return nil
}

// DeleteInstance removes a process with its history (administrators); a running one stops with it.
func (s *Store) DeleteInstance(ctx context.Context, id string) error {
	if !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM bp_instances WHERE id::text = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Log is a process's history.
func (s *Store) Log(ctx context.Context, id string) ([]Line, error) {
	rows, err := s.db.Query(ctx, `SELECT l.step, coalesce(u.name, ''), l.kind, l.text, l.at FROM bp_log l LEFT JOIN users u ON u.id = l.actor_id
		WHERE l.instance_id::text = $1 ORDER BY l.id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Line])
}

// currentSQL: the approval a is of the step its process i waits on — the process's or a parallel branch's.
const currentSQL = `(a.step = i.step OR a.step IN (SELECT b.step FROM bp_branches b WHERE b.instance_id = i.id AND b.waiting))`

const approvalSQL = `SELECT a.id, a.instance_id::text, i.name, a.kind, a.text, coalesce(a.decision, ''), a.comment, a.step, u.id, u.name, s.name, a.created_at,
		CASE WHEN a.kind = 'request' THEN i.steps -> a.step END
	FROM bp_approvals a JOIN bp_instances i ON i.id = a.instance_id JOIN users u ON u.id = a.user_id JOIN users s ON s.id = i.started_by`

func scanApproval(r pgx.CollectableRow) (Approval, error) {
	var a Approval
	var step []byte
	if err := r.Scan(&a.ID, &a.InstanceID, &a.Name, &a.Kind, &a.Text, &a.Decision, &a.Comment, &a.Step, &a.User.ID, &a.User.Name, &a.StartedBy,
		&a.CreatedAt, &step); err != nil {
		return a, err
	}
	if step != nil {
		var st Step
		if err := json.Unmarshal(step, &st); err != nil {
			return a, err
		}
		a.Fields, a.CanReject = st.Fields, st.Reject
	}
	return a, nil
}

// MyApprovals wait for the user's decision.
func (s *Store) MyApprovals(ctx context.Context, userID string) ([]Approval, error) {
	rows, err := s.db.Query(ctx, approvalSQL+` WHERE a.user_id::text = $1 AND a.decision IS NULL AND i.status = 'running' AND `+currentSQL+` ORDER BY a.created_at`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanApproval)
}

// Approvals of a process.
func (s *Store) Approvals(ctx context.Context, id string) ([]Approval, error) {
	rows, err := s.db.Query(ctx, approvalSQL+` WHERE a.instance_id::text = $1 ORDER BY a.created_at`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanApproval)
}

// Start starts a process of the template (of a CRM card when the template is of one) with the form's values.
func (s *Store) Start(ctx context.Context, userID, templateID, entityID string, values map[string]string) (string, error) {
	t, err := s.Template(ctx, templateID)
	if err != nil {
		return "", err
	}
	return s.start(ctx, userID, t, entityID, values, false)
}

// start: auto — the template starts by itself on a card's change, as its author, with the form empty (so a required
// field does not stop it) and without checking the author's access to the card.
func (s *Store) start(ctx context.Context, userID string, t Template, entityID string, values map[string]string, auto bool) (string, error) {
	if !t.Active {
		return "", i18n.E("err.bp_inactive")
	}
	clean, err := cleanValues(t.Fields, values, !auto)
	if err != nil {
		return "", err
	}
	var eid *string
	if t.Entity != "none" {
		if entityID == "" {
			return "", i18n.E("err.bad_form")
		}
		if !auto {
			if err := s.cardCheck(ctx, userID, t.Entity, entityID); err != nil {
				return "", err
			}
		}
		eid = &entityID
	}
	steps, _ := json.Marshal(t.Steps)
	vals, _ := json.Marshal(clean)
	var id string
	if err := s.db.QueryRow(ctx, `INSERT INTO bp_instances (template_id, name, steps, entity, entity_id, values, started_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, t.ID, t.Name, steps, t.Entity, eid, vals, userID).Scan(&id); err != nil {
		return "", err
	}
	kind := "started"
	if auto {
		kind = "autostarted"
	}
	s.log(ctx, id, 0, userID, kind, "")
	if s.Feed != nil && t.Entity == "none" && !auto {
		var lines []string
		for _, f := range t.Fields {
			if v := clean[f.Key]; v != "" {
				lines = append(lines, f.Label+": "+v)
			}
		}
		if len(lines) == 0 {
			lines = append(lines, t.Name)
		}
		_, _ = s.Feed.ProcessPost(ctx, userID, id, t.Name, strings.Join(lines, "\n")) // the process goes on without its post
	}
	return id, s.run(ctx, id)
}

// cleanValues are the fields' values checked: required ones filled (when strict), numbers and dates right.
func cleanValues(fields []Field, values map[string]string, strict bool) (map[string]string, error) {
	clean := map[string]string{}
	for _, f := range fields {
		v := strings.TrimSpace(values[f.Key])
		if f.Required && v == "" && strict {
			return nil, i18n.E("err.bp_required", f.Label)
		}
		if len([]rune(v)) > 10000 {
			return nil, i18n.E("err.bp_field")
		}
		if v != "" && f.Kind == "number" {
			if _, err := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64); err != nil {
				return nil, i18n.E("err.bp_number", f.Label)
			}
		}
		if v != "" && f.Kind == "date" {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return nil, i18n.E("err.bp_number", f.Label)
			}
		}
		clean[f.Key] = v
	}
	return clean, nil
}

// cardCheck: the person may read the CRM card the process is about.
func (s *Store) cardCheck(ctx context.Context, userID, entity, id string) error {
	return s.cardRight(ctx, userID, entity, "read", id)
}

// cardRight: the person may do op ("read", "write") on the CRM card.
func (s *Store) cardRight(ctx context.Context, userID, entity, op, id string) error {
	var resp string
	var err error
	switch entity {
	case "deal":
		var d crm.Deal
		d, err = s.CRM.Deal(ctx, id)
		resp = d.Responsible.ID
	case "lead":
		var l crm.Lead
		l, err = s.CRM.Lead(ctx, id)
		resp = l.Responsible.ID
	case "contact":
		var c crm.Contact
		c, err = s.CRM.Contact(ctx, id)
		resp = c.Responsible.ID
	case "company":
		var c crm.Company
		c, err = s.CRM.Company(ctx, id)
		resp = c.Responsible.ID
	}
	if err != nil {
		return err
	}
	return s.CRM.CheckCard(ctx, userID, entity, op, id, resp)
}

func (s *Store) log(ctx context.Context, id string, step int, actor, kind, text string) {
	var a *string
	if actor != "" {
		a = &actor
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO bp_log (instance_id, step, actor_id, kind, text) VALUES ($1, $2, $3, $4, $5)`, id, step, a, kind, text)
}

// Decide is the person's decision on their approval: approve, reject or (for a review) seen. The step is over when
// its mode says so — then the process goes on, jumps to the step's Goto (rejected) or ends as rejected.
func (s *Store) Decide(ctx context.Context, userID, approvalID, decision, comment string) error {
	comment = strings.TrimSpace(comment)
	if len([]rune(comment)) > 5000 {
		return i18n.E("err.bp_field")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var instance, kind string
	var step int
	// the process's row is locked (not its key: the history refers to it) — two people deciding at once see each
	// other's decisions
	err = tx.QueryRow(ctx, `SELECT a.instance_id::text, a.step, a.kind FROM bp_approvals a JOIN bp_instances i ON i.id = a.instance_id
		WHERE a.id::text = $1 AND a.user_id::text = $2 AND a.decision IS NULL AND i.status = 'running' AND `+currentSQL+`
		FOR NO KEY UPDATE OF i`, approvalID, userID).Scan(&instance, &step, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if kind == "review" {
		decision = "seen"
	} else if decision != "approve" && decision != "reject" || kind == "request" && decision != "reject" { // a request is answered by Answer
		return i18n.E("err.bad_form")
	}
	if _, err := tx.Exec(ctx, `UPDATE bp_approvals SET decision = $2, comment = $3, decided_at = now() WHERE id::text = $1`,
		approvalID, decision, comment); err != nil {
		return err
	}
	var total, yes, no, seen int
	if err := tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE decision = 'approve'), count(*) FILTER (WHERE decision = 'reject'),
		count(*) FILTER (WHERE decision = 'seen') FROM bp_approvals WHERE instance_id::text = $1 AND step = $2`, instance, step).Scan(&total, &yes, &no, &seen); err != nil {
		return err
	}
	x, err := s.instance(ctx, instance)
	if err != nil {
		return err
	}
	mode := ""
	if step < len(x.Steps) {
		mode = x.Steps[step].Mode
	}
	if kind == "request" && (step >= len(x.Steps) || !x.Steps[step].Reject) {
		return i18n.E("err.bad_form")
	}
	outcome := "" // the step goes on waiting
	switch {
	case kind == "review":
		if seen == total {
			outcome = "approved"
		}
	case mode == "all":
		if no > 0 {
			outcome = "rejected"
		} else if yes == total {
			outcome = "approved"
		}
	case mode == "majority":
		if yes*2 > total {
			outcome = "approved"
		} else if no*2 >= total {
			outcome = "rejected"
		}
	default:
		outcome = map[string]string{"approve": "approved", "reject": "rejected"}[decision]
	}
	s.log(ctx, instance, step, userID, map[string]string{"approve": "approved", "reject": "rejected", "seen": "seen"}[decision], comment)
	if outcome == "" {
		return tx.Commit(ctx)
	}
	// the step is over: the others need not decide any more
	if _, err := tx.Exec(ctx, `DELETE FROM bp_approvals WHERE instance_id::text = $1 AND step = $2 AND decision IS NULL`, instance, step); err != nil {
		return err
	}
	next := step + 1
	if outcome == "rejected" {
		if x.Steps[step].Goto == 0 {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			s.notify(ctx, x.StartedBy.ID, userID, instance, "❌ "+x.Name)
			return s.finish(ctx, instance, "rejected")
		}
		next = x.Steps[step].Goto - 1
	}
	if err := advance(ctx, tx, instance, step, next); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.run(ctx, instance)
}

// advance moves the process on from the step it waited on to next — or the pointer of the parallel branch the step is of.
func advance(ctx context.Context, tx pgx.Tx, instance string, step, next int) error {
	tag, err := tx.Exec(ctx, `UPDATE bp_branches SET step = $3, waiting = false, wake_at = NULL
		WHERE instance_id::text = $1 AND step = $2 AND waiting`, instance, step, next)
	if err != nil || tag.RowsAffected() > 0 {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE bp_instances SET step = $2 WHERE id::text = $1`, instance, next)
	return err
}

// Answer is the person's answer to their request: the step's fields filled in join the process's form, the others
// asked need not answer any more, and the process goes on.
func (s *Store) Answer(ctx context.Context, userID, approvalID string, values map[string]string, comment string) error {
	comment = strings.TrimSpace(comment)
	if len([]rune(comment)) > 5000 {
		return i18n.E("err.bp_field")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var instance string
	var step int
	err = tx.QueryRow(ctx, `SELECT a.instance_id::text, a.step FROM bp_approvals a JOIN bp_instances i ON i.id = a.instance_id
		WHERE a.id::text = $1 AND a.user_id::text = $2 AND a.decision IS NULL AND a.kind = 'request' AND i.status = 'running' AND `+currentSQL+`
		FOR NO KEY UPDATE OF i`, approvalID, userID).Scan(&instance, &step)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	x, err := s.instance(ctx, instance)
	if err != nil {
		return err
	}
	if step >= len(x.Steps) {
		return ErrNotFound
	}
	clean, err := cleanValues(x.Steps[step].Fields, values, true)
	if err != nil {
		return err
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	for k, v := range clean {
		x.Values[k] = v
	}
	vals, _ := json.Marshal(x.Values)
	if _, err := tx.Exec(ctx, `UPDATE bp_approvals SET decision = 'done', comment = $2, decided_at = now() WHERE id::text = $1`,
		approvalID, comment); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bp_approvals WHERE instance_id::text = $1 AND step = $2 AND decision IS NULL`, instance, step); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE bp_instances SET values = $2 WHERE id::text = $1`, instance, vals); err != nil {
		return err
	}
	if err := advance(ctx, tx, instance, step, step+1); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bp_log (instance_id, step, actor_id, kind, text) VALUES ($1, $2, $3, 'answered', $4)`,
		instance, step, userID, comment); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.run(ctx, instance)
}

func (s *Store) finish(ctx context.Context, id, status string) error {
	_, err := s.db.Exec(ctx, `UPDATE bp_instances SET status = $2, finished_at = now(), wake_at = NULL WHERE id::text = $1`, id, status)
	return err
}

func (s *Store) notify(ctx context.Context, to, actor, instance, text string) {
	var a *string
	if actor != "" {
		a = &actor
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'bp', $3, $4)`,
		to, a, text, "/bp/i/"+instance)
}

// WakeDue goes on with the processes whose pause (or a parallel branch's) is over (the worker calls it).
func (s *Store) WakeDue(ctx context.Context, now time.Time) []error {
	rows, err := s.db.Query(ctx, `UPDATE bp_instances SET wake_at = NULL, step = step + 1
		WHERE status = 'running' AND wake_at IS NOT NULL AND wake_at <= $1 RETURNING id::text`, now)
	if err != nil {
		return []error{err}
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return []error{err}
	}
	rows, err = s.db.Query(ctx, `UPDATE bp_branches b SET wake_at = NULL, waiting = false, step = b.step + 1 FROM bp_instances i
		WHERE i.id = b.instance_id AND i.status = 'running' AND b.wake_at IS NOT NULL AND b.wake_at <= $1 RETURNING i.id::text`, now)
	if err != nil {
		return []error{err}
	}
	more, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return []error{err}
	}
	for _, id := range more { // a process once though two of its branches wake
		if !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	var errs []error
	for _, id := range ids {
		if err := s.run(ctx, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
