package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// An agent of a section (mail, tasks…) works for an employee with their rights: the model is given the agent's tools,
// the tools that only read run at once, the ones that write or send out wait for the employee's «Confirm». A run is
// the conversation of the employee with the agent; every call of a tool is logged in it (ai_actions).

// Tool is a tool of an agent; Run works with the rights of the employee the agent works for.
type Tool struct {
	ToolSpec
	Write bool // changes data or sends out: runs only after the employee confirms it
	Run   func(ctx context.Context, userID string, args json.RawMessage) (string, error)
}

// Agent is the agent of a section.
type Agent struct {
	Name   string // the section: locale key ai.agent.<name>
	System func(ctx context.Context, userID string) string
	Tools  []Tool
}

func (a *Agent) tool(name string) *Tool {
	for i := range a.Tools {
		if a.Tools[i].Name == name {
			return &a.Tools[i]
		}
	}
	return nil
}

// Run statuses.
const (
	RunRunning = "running"
	RunConfirm = "confirm" // an action waits for the employee
	RunDone    = "done"
	RunError   = "error"
)

// Action statuses.
const (
	ActDone     = "done"
	ActError    = "error"
	ActPending  = "pending"
	ActRejected = "rejected"
)

// rejected is what the model learns of an action the employee rejected.
const rejected = "The employee rejected this action."

// maxSteps bounds the calls of the model for one message of the employee.
const maxSteps = 12

// Run is a conversation of an employee with an agent.
type Run struct {
	ID           string
	UserID       string
	Agent        string
	Title        string
	ConnectionID string
	Model        string
	Status       string
	Error        string
	Messages     []Message
	Actions      []Action
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Action is a call of a tool by the agent.
type Action struct {
	ID        string
	RunID     string
	CallID    string
	Tool      string
	Args      json.RawMessage
	Write     bool
	Status    string
	Result    string
	CreatedAt time.Time
	DecidedAt *time.Time
}

// Err is the error of a failed run, to translate.
func (r Run) Err() *i18n.Error {
	parts := strings.Split(r.Error, "\x1f")
	args := make([]any, len(parts)-1)
	for i, p := range parts[1:] {
		args[i] = p
	}
	return i18n.E(parts[0], args...)
}

// errText keeps an error of a run as its message key and its arguments as text.
func errText(err error) string {
	var e *i18n.Error
	if !errors.As(err, &e) {
		return "err.internal"
	}
	out := e.Key
	for _, a := range e.Args {
		out += "\x1f" + fmt.Sprint(a)
	}
	return out
}

// text is an error of a tool as the model reads it.
func (s *Store) text(err error) string {
	if s.Text != nil {
		return s.Text(err)
	}
	return err.Error()
}

// Pending are the actions waiting for the employee.
func (r Run) Pending() []Action {
	var out []Action
	for _, a := range r.Actions {
		if a.Status == ActPending {
			out = append(out, a)
		}
	}
	return out
}

// Action is the action of the call ("" ID — none).
func (r Run) Action(callID string) Action {
	for _, a := range r.Actions {
		if a.CallID == callID {
			return a
		}
	}
	return Action{}
}

const runCols = `id, user_id, agent, title, COALESCE(connection_id::text, ''), model, status, error, messages, created_at, updated_at`

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var msgs []byte
	err := row.Scan(&r.ID, &r.UserID, &r.Agent, &r.Title, &r.ConnectionID, &r.Model, &r.Status, &r.Error, &msgs, &r.CreatedAt, &r.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(msgs, &r.Messages)
	}
	return r, err
}

// Runs are the employee's last runs (agent "" — of all agents), the newest first, without their actions.
func (s *Store) Runs(ctx context.Context, userID, agent string, limit int) ([]Run, error) {
	rows, err := s.db.Query(ctx, `SELECT `+runCols+` FROM ai_runs WHERE user_id = $1 AND ($2 = '' OR agent = $2)
		ORDER BY updated_at DESC LIMIT $3`, userID, agent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun is a run of the employee with its actions.
func (s *Store) GetRun(ctx context.Context, userID, id string) (Run, error) {
	r, err := scanRun(s.db.QueryRow(ctx, `SELECT `+runCols+` FROM ai_runs WHERE id::text = $1 AND user_id = $2`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	rows, err := s.db.Query(ctx, `SELECT id, run_id, call_id, tool, args, write, status, result, created_at, decided_at
		FROM ai_actions WHERE run_id = $1 ORDER BY created_at, id`, r.ID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Action
		if err := rows.Scan(&a.ID, &a.RunID, &a.CallID, &a.Tool, &a.Args, &a.Write, &a.Status, &a.Result, &a.CreatedAt, &a.DecidedAt); err != nil {
			return r, err
		}
		r.Actions = append(r.Actions, a)
	}
	return r, rows.Err()
}

// DeleteRun removes a run of the employee with its log.
func (s *Store) DeleteRun(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ai_runs WHERE id::text = $1 AND user_id = $2`, id, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// title is the start of the first message, as the list of runs shows it.
func title(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= 80 {
		return text
	}
	r := []rune(text)
	return string(r[:79]) + "…"
}

// Say sends a message of the employee to an agent: runID "" starts a new run. Actions still waiting are rejected
// — the employee went on without them. The run comes back as it stopped: answered, waiting for a confirmation or
// failed (the error is in the run).
func (s *Store) Say(ctx context.Context, userID string, agent *Agent, runID, text string) (Run, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Run{}, i18n.E("err.ai_text_required")
	}
	var r Run
	var err error
	if runID == "" {
		err = s.db.QueryRow(ctx, `INSERT INTO ai_runs (user_id, agent, title, status) VALUES ($1, $2, $3, $4) RETURNING id`,
			userID, agent.Name, title(text), RunRunning).Scan(&runID)
		if err != nil {
			return r, err
		}
	}
	if r, err = s.GetRun(ctx, userID, runID); err != nil {
		return r, err
	}
	if r.Agent != agent.Name {
		return r, ErrNotFound
	}
	if r.Status == RunRunning && len(r.Messages) > 0 && time.Since(r.UpdatedAt) < 5*time.Minute {
		return r, i18n.E("err.ai_busy")
	}
	for _, a := range r.Pending() {
		if _, err := s.decide(ctx, r.ID, a.ID, ActRejected, rejected); err != nil {
			return r, err
		}
	}
	if r, err = s.GetRun(ctx, userID, runID); err != nil {
		return r, err
	}
	msg := Message{Role: "user", Text: text}
	if res := r.results(); res != nil { // the calls of the last answer get their results first
		msg.Results = res
	}
	if n := len(r.Messages); n > 0 && r.Messages[n-1].Role == "user" { // the model failed to answer the last one
		last := &r.Messages[n-1]
		last.Text = strings.TrimSpace(last.Text + "\n\n" + text)
	} else {
		r.Messages = append(r.Messages, msg)
	}
	return s.step(ctx, agent, r)
}

// Decide confirms (yes) or rejects an action waiting for the employee; when none waits any more, the agent goes on.
func (s *Store) Decide(ctx context.Context, userID string, agent *Agent, runID, actionID string, yes bool) (Run, error) {
	r, err := s.GetRun(ctx, userID, runID)
	if err != nil {
		return r, err
	}
	if r.Agent != agent.Name {
		return r, ErrNotFound
	}
	var act *Action
	for i := range r.Actions {
		if r.Actions[i].ID == actionID {
			act = &r.Actions[i]
		}
	}
	if act == nil {
		return r, ErrNotFound
	}
	if act.Status != ActPending {
		return r, i18n.E("err.ai_decided")
	}
	// the action is claimed first: a second click does not run it twice
	status, result := ActRejected, rejected
	if yes {
		status, result = ActDone, ""
	}
	ok, err := s.decide(ctx, r.ID, act.ID, status, result)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, i18n.E("err.ai_decided")
	}
	if yes {
		status, result = ActDone, ""
		if t := agent.tool(act.Tool); t == nil {
			status, result = ActError, "unknown tool"
		} else if out, err := t.Run(ctx, userID, act.Args); err != nil {
			status, result = ActError, s.text(err)
		} else {
			result = out
		}
		if _, err := s.db.Exec(ctx, `UPDATE ai_actions SET status = $2, result = $3 WHERE id = $1`, act.ID, status, result); err != nil {
			return r, err
		}
	}
	if r, err = s.GetRun(ctx, userID, runID); err != nil {
		return r, err
	}
	if len(r.Pending()) > 0 {
		return r, nil
	}
	r.Messages = append(r.Messages, Message{Role: "user", Results: r.results()})
	return s.step(ctx, agent, r)
}

// decide sets the outcome of a pending action; false — it was decided already.
func (s *Store) decide(ctx context.Context, runID, id, status, result string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE ai_actions SET status = $3, result = $4, decided_at = now()
		WHERE id = $2 AND run_id = $1 AND status = 'pending'`, runID, id, status, result)
	return err == nil && tag.RowsAffected() == 1, err
}

// results are the results of the calls of the last answer of the model, when they are not given to it yet (nil —
// there are none).
func (r Run) results() []Result {
	if len(r.Messages) == 0 {
		return nil
	}
	last := r.Messages[len(r.Messages)-1]
	if last.Role != "assistant" || len(last.Calls) == 0 {
		return nil
	}
	var out []Result
	for _, c := range last.Calls {
		a := r.Action(c.ID)
		res := Result{ID: c.ID, Name: c.Name, Text: a.Result, Error: a.Status == ActError || a.Status == ActRejected}
		if a.ID == "" {
			res.Text, res.Error = "not called", true
		}
		if res.Text == "" {
			res.Text = "ok"
		}
		out = append(out, res)
	}
	return out
}

// step calls the model until it answers without tools, an action waits for the employee or it fails.
func (s *Store) step(ctx context.Context, agent *Agent, r Run) (Run, error) {
	r.Status, r.Error = RunRunning, ""
	if err := s.saveRun(ctx, r); err != nil {
		return r, err
	}
	specs := make([]ToolSpec, len(agent.Tools))
	for i, t := range agent.Tools {
		specs[i] = t.ToolSpec
	}
	system := ""
	if agent.System != nil {
		system = agent.System(ctx, r.UserID)
	}
	fail := func(err error) (Run, error) {
		r.Status, r.Error = RunError, errText(err)
		return r, s.saveRun(ctx, r)
	}
	for range maxSteps {
		c, key, err := s.For(ctx, r.UserID)
		if err != nil {
			return fail(err)
		}
		if err := s.allow(ctx, r.UserID, r.Agent, c); err != nil {
			return fail(err)
		}
		model := c.Model
		r.ConnectionID, r.Model = c.ID, model
		reply, err := s.Ask(ctx, c, key, model, system, r.Messages, specs)
		if err != nil {
			return fail(i18n.E("err.ai_call", err.Error()))
		}
		if err := s.note(ctx, r.UserID, r.ID, r.Agent, c, model, reply); err != nil {
			return r, err
		}
		r.Messages = append(r.Messages, Message{Role: "assistant", Text: reply.Text, Calls: reply.Calls})
		if len(reply.Calls) == 0 {
			r.Status = RunDone
			return r, s.saveRun(ctx, r)
		}
		waits := false
		for _, call := range reply.Calls {
			t := agent.tool(call.Name)
			a := Action{RunID: r.ID, CallID: call.ID, Tool: call.Name, Args: call.Args, Status: ActDone}
			switch {
			case t == nil:
				a.Status, a.Result = ActError, "unknown tool"
			case t.Write:
				a.Write, a.Status, waits = true, ActPending, true
			default:
				out, err := t.Run(ctx, r.UserID, call.Args)
				if err != nil {
					a.Status, a.Result = ActError, s.text(err)
				} else {
					a.Result = out
				}
			}
			if !json.Valid(a.Args) {
				a.Args = json.RawMessage("{}")
			}
			if err := s.db.QueryRow(ctx, `INSERT INTO ai_actions (run_id, call_id, tool, args, write, status, result)
				VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at`,
				a.RunID, a.CallID, a.Tool, a.Args, a.Write, a.Status, a.Result).Scan(&a.ID, &a.CreatedAt); err != nil {
				return r, err
			}
			r.Actions = append(r.Actions, a)
		}
		if waits {
			r.Status = RunConfirm
			return r, s.saveRun(ctx, r)
		}
		r.Messages = append(r.Messages, Message{Role: "user", Results: r.results()})
		if err := s.saveRun(ctx, r); err != nil {
			return r, err
		}
	}
	return fail(i18n.E("err.ai_steps"))
}

func (s *Store) saveRun(ctx context.Context, r Run) error {
	msgs, err := json.Marshal(r.Messages)
	if err != nil {
		return err
	}
	var conn *string
	if r.ConnectionID != "" {
		conn = &r.ConnectionID
	}
	_, err = s.db.Exec(ctx, `UPDATE ai_runs SET status = $2, error = $3, messages = $4, connection_id = $5, model = $6,
		updated_at = now() WHERE id = $1`, r.ID, r.Status, r.Error, msgs, conn, r.Model)
	return err
}
