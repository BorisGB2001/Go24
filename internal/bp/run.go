package bp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// run executes the process's steps from the current one until a step waits (an approval, a review, a request, a pause) or the
// steps end. A step that fails ends the process as failed, with the reason in its history. In a parallel step it runs its
// branches in turn, a step of a branch at a time, until each waits or is over; when all are over, the branches join.
// Runs of one process go one at a time: two branches decided at once must not do a step twice.
func (s *Store) run(ctx context.Context, id string) error {
	conn, err := s.db.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended($1, 57))`, id); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 57))`, id)
	for i := 0; i < 100; i++ {
		x, err := s.instance(ctx, id)
		if err != nil {
			return err
		}
		if x.Status != "running" {
			return nil
		}
		if x.Step >= len(x.Steps) {
			s.log(ctx, id, x.Step, "", "done", "")
			s.notify(ctx, x.StartedBy.ID, "", id, "✅ "+x.Name)
			return s.finish(ctx, id, "done")
		}
		if len(x.Branches) > 0 {
			k, joined, err := s.nextBranch(ctx, x)
			if joined {
				continue
			}
			if err != nil || k < 0 {
				return err
			}
			x.branch, x.end = k+1, Ranges(x.Steps, x.Step)[k][1]
			x.Step = x.Branches[k]
		}
		st := x.Steps[x.Step]
		next, wait, err := s.step(ctx, x, st)
		if err != nil {
			s.log(ctx, id, x.Step, "", "failed", st.Kind+": "+err.Error())
			s.notify(ctx, x.StartedBy.ID, "", id, "⚠ "+x.Name)
			return s.finish(ctx, id, "failed")
		}
		switch {
		case x.branch > 0:
			if wait {
				next = x.Step
			}
			_, err = s.db.Exec(ctx, `UPDATE bp_branches SET step = $3, waiting = $4 WHERE instance_id = $1 AND n = $2`, id, x.branch-1,
				min(next, x.end), wait)
		case wait:
			return nil
		default:
			_, err = s.db.Exec(ctx, `UPDATE bp_instances SET step = $2 WHERE id = $1`, id, next)
		}
		if err != nil {
			return err
		}
	}
	return fmt.Errorf("bp: too many steps")
}

// nextBranch is the branch of the parallel step x waits in to run a step of (-1 — all wait); when every branch is over,
// they join (joined): the process goes on to the step's Goto.
func (s *Store) nextBranch(ctx context.Context, x Instance) (k int, joined bool, err error) {
	rows, err := s.db.Query(ctx, `SELECT step, waiting FROM bp_branches WHERE instance_id = $1 ORDER BY n`, x.ID)
	if err != nil {
		return -1, false, err
	}
	type branch struct {
		Step    int
		Waiting bool
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[branch])
	if err != nil {
		return -1, false, err
	}
	over := true
	for k, r := range Ranges(x.Steps, x.Step) {
		if k < len(list) && list[k].Step < r[1] {
			over = false
			if !list[k].Waiting {
				return k, false, nil
			}
		}
	}
	if !over {
		return -1, false, nil
	}
	join := jump(x, x.Steps[x.Step].Goto)
	if _, err := s.db.Exec(ctx, `WITH b AS (DELETE FROM bp_branches WHERE instance_id = $1) UPDATE bp_instances SET step = $2 WHERE id = $1`,
		x.ID, join); err != nil {
		return -1, false, err
	}
	s.log(ctx, x.ID, x.Step, "", "joined", "")
	return -1, true, nil
}

// fill puts the form's values and the process's names into a text: {field}, {starter}, {title}, {link}, {created_card}.
func (s *Store) fill(ctx context.Context, x Instance, text string) string {
	pairs := []string{"{starter}", x.StartedBy.Name, "{process}", x.Name, "{link}", "/bp/i/" + x.ID}
	for k, v := range x.Values {
		pairs = append(pairs, "{"+k+"}", v)
	}
	if x.Entity != "none" {
		pairs = append(pairs, "{title}", s.cardTitle(ctx, x.Entity, x.EntityID), "{card}", cardPath(x.Entity, x.EntityID))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

func cardPath(entity, id string) string {
	return "/crm/" + map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[entity] + "/" + id
}

func (s *Store) cardTitle(ctx context.Context, entity, id string) string {
	switch entity {
	case "deal":
		if d, err := s.CRM.Deal(ctx, id); err == nil {
			return d.Title
		}
	case "lead":
		if l, err := s.CRM.Lead(ctx, id); err == nil {
			return l.Title
		}
	case "contact":
		if c, err := s.CRM.Contact(ctx, id); err == nil {
			return c.Name
		}
	case "company":
		if c, err := s.CRM.Company(ctx, id); err == nil {
			return c.Title
		}
	}
	return ""
}

// who is the person a step means: the starter, their manager, the card's responsible or a given user.
func (s *Store) who(ctx context.Context, x Instance, to string) (string, error) {
	switch to {
	case "", "starter":
		return x.StartedBy.ID, nil
	case "manager":
		list, err := s.Company.Managers(ctx, x.StartedBy.ID)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "", fmt.Errorf("%s has no manager", x.StartedBy.Name)
		}
		return list[0].ID, nil
	case "responsible":
		switch x.Entity {
		case "deal":
			d, err := s.CRM.Deal(ctx, x.EntityID)
			return d.Responsible.ID, err
		case "lead":
			l, err := s.CRM.Lead(ctx, x.EntityID)
			return l.Responsible.ID, err
		case "contact":
			c, err := s.CRM.Contact(ctx, x.EntityID)
			return c.Responsible.ID, err
		case "company":
			c, err := s.CRM.Company(ctx, x.EntityID)
			return c.Responsible.ID, err
		}
		return x.StartedBy.ID, nil
	}
	return to, nil
}

// step does one step and says the next one (a jump of a condition or a «go to»; len(steps) — the end); wait says
// the process stops here until a decision or a pause's end.
func (s *Store) step(ctx context.Context, x Instance, st Step) (next int, wait bool, err error) {
	next = x.Step + 1
	text := s.fill(ctx, x, st.Text)
	switch st.Kind {
	case "approve", "review", "request":
		users, err := s.people(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		for _, user := range users {
			if _, err := s.db.Exec(ctx, `INSERT INTO bp_approvals (instance_id, step, user_id, text, kind) VALUES ($1, $2, $3, $4, $5)`,
				x.ID, x.Step, user, text, st.Kind); err != nil {
				return next, false, err
			}
			s.notify(ctx, user, x.StartedBy.ID, x.ID, map[string]string{"approve": "📝 ", "review": "👁 ", "request": "❓ "}[st.Kind]+text)
			s.feedReader(ctx, x, user)
		}
		s.log(ctx, x.ID, x.Step, "", "step", st.Kind+":"+strings.Join(users, ","))
		return next, true, nil
	case "notify":
		user, err := s.who(ctx, x, st.To)
		if err != nil {
			return next, false, err
		}
		s.notify(ctx, user, x.StartedBy.ID, x.ID, text)
		s.feedReader(ctx, x, user)
		s.log(ctx, x.ID, x.Step, "", "step", "notify:"+user)
		return next, false, nil
	case "chat":
		users, err := s.chat(ctx, x, st, text)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "chat:"+strings.Join(users, ","))
		}
		return next, false, err
	case "mail":
		to, err := s.mail(ctx, x, st, text)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "mail:"+to)
		}
		return next, false, err
	case "update":
		err := s.updateCard(ctx, x, st.Sets)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "update")
		}
		return next, false, err
	case "create":
		id, err := s.createCard(ctx, x, st, text)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "create:"+cardPath(st.Entity, id))
		}
		return next, false, err
	case "if":
		ok := check(x.Values[st.Field], st.Op, s.fill(ctx, x, st.Value))
		if !ok {
			next = jump(x, st.Goto)
		}
		s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("if:%t", ok))
		return next, false, nil
	case "goto":
		s.log(ctx, x.ID, x.Step, "", "step", "goto")
		return jump(x, st.Goto), false, nil
	case "set":
		if x.Values == nil {
			x.Values = map[string]string{}
		}
		x.Values[st.Field] = s.fill(ctx, x, st.Value)
		vals, _ := json.Marshal(x.Values)
		if _, err := s.db.Exec(ctx, `UPDATE bp_instances SET values = $2 WHERE id = $1`, x.ID, vals); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "set:"+st.Field)
		return next, false, nil
	case "task":
		user, err := s.who(ctx, x, st.To)
		if err != nil {
			return next, false, err
		}
		actx := tasks.WithAdmin(ctx) // the process acts for the company
		p, err := s.Tasks.ProjectByKey(actx, x.StartedBy.ID, st.Project)
		if err != nil {
			return next, false, err
		}
		var deadline *time.Time
		if st.Days > 0 {
			sched, err := s.Tasks.Schedule(ctx)
			if err != nil {
				return next, false, err
			}
			d := sched.Deadline(time.Now().AddDate(0, 0, st.Days))
			deadline = &d
		}
		desc := s.fill(ctx, x, "{process}: {link}")
		key, err := s.Tasks.CreateTask(actx, x.StartedBy.ID, p, tasks.NewTask{Title: text, Description: desc, ResponsibleID: user, Priority: 1, Deadline: deadline})
		if err != nil {
			return next, false, err
		}
		if x.Entity != "none" {
			if id, err := s.Tasks.TaskIDByKey(ctx, key); err == nil {
				_ = s.Tasks.AddCRMLink(actx, x.StartedBy.ID, id, cardPath(x.Entity, x.EntityID))
			}
		}
		s.feedReader(ctx, x, user)
		s.log(ctx, x.ID, x.Step, "", "step", "task:"+key)
		return next, false, nil
	case "stage":
		switch x.Entity {
		case "deal":
			_, err = s.CRM.MoveDeal(ctx, x.StartedBy.ID, x.EntityID, st.Stage)
		case "lead":
			_, err = s.CRM.MoveLead(ctx, x.StartedBy.ID, x.EntityID, st.Stage)
		default:
			err = fmt.Errorf("no CRM card")
		}
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "stage")
		}
		return next, false, err
	case "parallel": // the branches start; the process stays on the step till they join
		for k, r := range Ranges(x.Steps, x.Step) {
			if _, err := s.db.Exec(ctx, `INSERT INTO bp_branches (instance_id, n, step) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				x.ID, k, r[0]); err != nil {
				return next, false, err
			}
		}
		s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("parallel:%d", len(st.Branches)+1))
		return x.Step, false, nil
	case "absence": // «Внести в график отсутствий»: the starter's own absence, as they would add it
		from, err1 := time.ParseInLocation("2006-01-02", x.Values[st.Start], time.Local)
		to, err2 := time.ParseInLocation("2006-01-02", x.Values[st.End], time.Local)
		if err1 != nil || err2 != nil {
			return next, false, fmt.Errorf("no dates")
		}
		if err := s.Company.AddAbsence(ctx, x.StartedBy.ID, x.StartedBy.ID, st.Absence, from, to, text, false, "", nil); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "absence:"+st.Absence)
		return next, false, nil
	case "wait":
		wake := time.Now().AddDate(0, 0, st.Days)
		if x.branch > 0 {
			_, err = s.db.Exec(ctx, `UPDATE bp_branches SET wake_at = $2 WHERE instance_id = $1 AND n = $3`, x.ID, wake, x.branch-1)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE bp_instances SET wake_at = $2 WHERE id = $1`, x.ID, wake)
		}
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("wait:%d", st.Days))
		return next, true, nil
	case "webhook":
		return next, false, s.webhook(ctx, x, st.URL)
	}
	return next, false, fmt.Errorf("unknown step %q", st.Kind)
}

// jump is the index of the step numbered n from 1; 0 — past the last one, the end of the process (in a parallel
// branch — of the branch).
func jump(x Instance, n int) int {
	end := len(x.Steps)
	if x.branch > 0 {
		end = x.end
	}
	if n <= 0 || n > end {
		return end
	}
	return n - 1
}

// feedReader: a person given a task of the process sees its post in the feed.
func (s *Store) feedReader(ctx context.Context, x Instance, user string) {
	if s.Feed != nil && x.Entity == "none" {
		_ = s.Feed.ProcessReader(ctx, x.ID, user)
	}
}

// people an approval, a review or a request goes to: To and Users, each once.
func (s *Store) people(ctx context.Context, x Instance, st Step) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, to := range st.People() {
		user, err := s.who(ctx, x, to)
		if err != nil {
			return nil, err
		}
		if !seen[user] {
			seen[user] = true
			out = append(out, user)
		}
	}
	return out, nil
}

// check compares a field's value: as numbers when both are numbers, as text otherwise (a date YYYY-MM-DD compares
// right as text); text comparisons ignore case.
func check(v, op, want string) bool {
	v, want = strings.TrimSpace(v), strings.TrimSpace(want)
	switch op {
	case "empty":
		return v == ""
	case "filled":
		return v != ""
	case "contains":
		return strings.Contains(strings.ToLower(v), strings.ToLower(want))
	}
	cmp := strings.Compare(strings.ToLower(v), strings.ToLower(want))
	a, errA := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	b, errB := strconv.ParseFloat(strings.ReplaceAll(want, ",", "."), 64)
	if errA == nil && errB == nil {
		cmp = 0
		if a < b {
			cmp = -1
		} else if a > b {
			cmp = 1
		}
	}
	switch op {
	case "eq":
		return cmp == 0
	case "ne":
		return cmp != 0
	case "gt":
		return cmp > 0
	case "lt":
		return cmp < 0
	}
	return false
}

// webhookClient waits for n8n a while: a workflow may do some work before it answers.
var webhookClient = &http.Client{Timeout: 30 * time.Second}

// webhook sends the process's data to the URL (n8n's Webhook node). When the answer is a JSON object, its
// values join the process's form, so the next steps can use them as {key}.
func (s *Store) webhook(ctx context.Context, x Instance, target string) error {
	payload := map[string]any{"event": "bp.step", "process": x.Name, "process_id": x.ID, "step": x.Step,
		"link": s.BaseURL + "/bp/i/" + x.ID, "values": x.Values, "starter": map[string]string{"id": x.StartedBy.ID, "name": x.StartedBy.Name}}
	if x.Entity != "none" {
		payload["entity"], payload["entity_id"], payload["card"] = x.Entity, x.EntityID, s.BaseURL+cardPath(x.Entity, x.EntityID)
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go24-process")
	res, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	var answer map[string]any
	if json.Unmarshal(raw, &answer) != nil || len(answer) == 0 {
		s.log(ctx, x.ID, x.Step, "", "step", "webhook")
		return nil
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	for k, v := range answer {
		if strings.ContainsAny(k, " {}") || len(k) > 100 {
			continue
		}
		switch t := v.(type) {
		case string:
			x.Values[k] = t
		case float64, bool:
			x.Values[k] = fmt.Sprint(t)
		}
	}
	vals, _ := json.Marshal(x.Values)
	if _, err := s.db.Exec(ctx, `UPDATE bp_instances SET values = $2 WHERE id = $1`, x.ID, vals); err != nil {
		return err
	}
	s.log(ctx, x.ID, x.Step, "", "step", "webhook")
	return nil
}
