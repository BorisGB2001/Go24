package bp

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/feed"
	"go24/internal/i18n"
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
	for i := 0; ; i++ {
		x, err := s.instance(ctx, id)
		if err == nil {
			err = s.withGlobals(ctx, &x)
		}
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
		if err == nil && i == 1000 { // loops without a pause or a person to wait for
			err = i18n.E("err.bp_too_long")
		}
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
}

// holds: the condition of an «if» or a loop step is true — the field's value compared with the step's ({…} filled, a
// formula counted), and the step's more conditions, «и» before «или» joins them.
func (s *Store) holds(ctx context.Context, x Instance, st Step) (bool, error) {
	some, all := false, true
	for i, c := range append([]Cond{{Field: st.Field, Op: st.Op, Value: st.Value}}, st.More...) {
		if i > 0 && c.Or { // a group of «и» ends
			some, all = some || all, true
		}
		if !all { // the group is false already
			continue
		}
		ok, err := s.holdsOne(ctx, x, c)
		if err != nil {
			return false, err
		}
		all = ok
	}
	return some || all, nil
}

// holdsOne: the field's value compared with the condition's.
func (s *Store) holdsOne(ctx context.Context, x Instance, c Cond) (bool, error) {
	want, err := s.value(ctx, x, c.Value)
	if err != nil {
		return false, err
	}
	f, _ := x.Field(c.Field)
	if f.Kind == "user" && c.Op == "in" { // «Условие группы пользователей»
		return s.memberOf(ctx, x, f.List(x.Values[c.Field]), Alternatives(want))
	}
	switch f.Kind {
	case "bool", "datetime": // «Y», «yes», «1»; a time with a space
		if v, err := cleanValue(Field{Kind: f.Kind}, want); err == nil {
			want = v
		}
	case "user": // an employee by name or e-mail too
		if id, err := s.person(ctx, x, want); want != "" && err == nil {
			want = id
		}
	}
	if f.Multiple && c.Op == "in" { // some of the values is one of the wanted
		for _, v := range f.List(x.Values[c.Field]) {
			if check(v, c.Op, want) {
				return true, nil
			}
		}
		return false, nil
	}
	return check(x.Values[c.Field], c.Op, want), nil
}

// memberOf: some of the employees is one of refs — an employee (by id, name, e-mail, «starter»…) or a department (by
// «dept:ID» or name), a member of it or of a sub-one, or its head.
func (s *Store) memberOf(ctx context.Context, x Instance, users, refs []string) (bool, error) {
	if len(users) == 0 {
		return false, nil
	}
	var people, depts []string
	for _, r := range refs {
		if id, ok := strings.CutPrefix(r, "dept:"); ok {
			depts = append(depts, id)
		} else if id, err := s.person(ctx, x, r); err == nil {
			people = append(people, id)
		} else {
			depts = append(depts, r) // a department by its name
		}
	}
	if slices.ContainsFunc(users, func(u string) bool { return slices.Contains(people, u) }) {
		return true, nil
	}
	if len(depts) == 0 {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = ANY($2) OR lower(name) = ANY($3)
			UNION SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
		SELECT EXISTS (SELECT 1 FROM user_departments WHERE user_id::text = ANY($1) AND department_id IN (SELECT id FROM sub))
			OR EXISTS (SELECT 1 FROM departments WHERE head_id::text = ANY($1) AND id IN (SELECT id FROM sub))`,
		users, depts, lower(depts)).Scan(&ok)
	return ok, err
}

func lower(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = strings.ToLower(s)
	}
	return out
}

// startChild starts the process of the step's template for the starter of x: of the same CRM card when the template
// is of that kind of card, else a standalone one. Its fields are the step's values ({…} of x filled); the chain of
// processes started so is MaxDepth long at most.
func (s *Store) startChild(ctx context.Context, x Instance, st Step) (string, error) {
	t, err := s.Template(ctx, st.Template)
	if err != nil {
		return "", err
	}
	entity := ""
	if t.Entity != "none" {
		if t.Entity != x.Entity || x.EntityID == "" {
			return "", i18n.E("err.bp_start_entity")
		}
		entity = x.EntityID
	}
	var depth int
	if err := s.db.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM bp_instances WHERE id = $1
		UNION ALL SELECT i.id, i.parent_id FROM bp_instances i JOIN up ON i.id = up.parent_id) SELECT count(*) FROM up`, x.ID).Scan(&depth); err != nil {
		return "", err
	}
	if depth > MaxDepth {
		return "", i18n.E("err.bp_depth", MaxDepth)
	}
	values := map[string]string{}
	for _, a := range st.Sets {
		v, err := s.value(ctx, x, a.Value)
		if err != nil {
			return "", err
		}
		if f, ok := fieldOf(t.Fields, a.Field); ok && f.Kind == "user" && v != "" { // starter, manager, a name, an e-mail
			if id, err := s.person(ctx, x, v); err == nil {
				v = id
			}
		}
		values[a.Field] = v
	}
	return s.start(ctx, x.StartedBy.ID, t, entity, values, false, x.ID)
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

// fill puts the form's values and the process's names into a text: {field}, {starter}, {title}, {link}, {created_card}, {created_document}, {created_task},
// {created_list_element}, {converted_deal}, {created_folder}.
func (s *Store) fill(ctx context.Context, x Instance, text string) string {
	return s.fillAs(ctx, x, text, true)
}

// fillRaw is fill with the values as kept — the id of a user field, "Y" of a bool — for a field to set or compare.
func (s *Store) fillRaw(ctx context.Context, x Instance, text string) string {
	return s.fillAs(ctx, x, text, false)
}

func (s *Store) fillAs(ctx context.Context, x Instance, text string, shown bool) string {
	pairs := []string{"{starter}", x.StartedBy.Name, "{process}", x.Name, "{link}", "/bp/i/" + x.ID}
	for k, v := range x.Values {
		if f, ok := x.Field(k); ok && shown && f.Kind == "file" { // the names and addresses of the files
			v = s.fileText(ctx, f, v)
		} else if ok && shown {
			v = s.show(ctx, f, v)
		}
		pairs = append(pairs, "{"+k+"}", v)
	}
	if x.Entity != "none" {
		pairs = append(pairs, "{title}", s.cardTitle(ctx, x.Entity, x.EntityID), "{card}", CardPath(x.Entity, x.EntityID))
	}
	if x.Entity == EntityList {
		pairs = append(pairs, s.listValues(ctx, x.EntityID, shown)...)
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// show is a field's value as people read it: a bool «Да»/«Нет», a user — the name, a datetime without «T»; the
// values of a multiple field — parted by commas.
func (s *Store) show(ctx context.Context, f Field, v string) string {
	if f.Multiple {
		one := f
		one.Multiple = false
		var out []string
		for _, p := range f.List(v) {
			out = append(out, s.show(ctx, one, p))
		}
		return strings.Join(out, ", ")
	}
	switch {
	case v == "":
	case f.Kind == "bool" && s.Tr != nil:
		return s.Tr("bp.bool." + v)
	case f.Kind == "user":
		var name string
		if s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, v).Scan(&name) == nil {
			return name
		}
	case f.Kind == "datetime":
		return strings.Replace(v, "T", " ", 1)
	case f.Kind == "file":
		if name := s.FileNames(ctx, []string{v})[v]; name != "" {
			return name
		}
	}
	return v
}

// CardPath is the address of the card a process is about: a CRM card, a document's card on the drive.
func CardPath(entity, id string) string {
	if entity == EntityFile {
		return "/files/" + id + "/about"
	}
	if entity == EntityList {
		return "/bp/el/" + id
	}
	return "/crm/" + map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[entity] + "/" + id
}

func (s *Store) cardTitle(ctx context.Context, entity, id string) string {
	switch entity {
	case EntityFile:
		if s.Files != nil {
			name, _, _ := s.Files.BPFile(ctx, id)
			return name
		}
	case EntityList:
		if e, err := s.element(ctx, id); err == nil {
			return e.Name
		}
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

// who is the person a step means: the starter, their manager, the card's responsible, the employee of a user field
// or a given user.
func (s *Store) who(ctx context.Context, x Instance, to string) (string, error) {
	if key, ok := strings.CutPrefix(to, FieldPrefix); ok {
		f, _ := x.Field(key)
		list := f.List(x.Values[key])
		if len(list) == 0 {
			return "", fmt.Errorf("the field %q is empty", f.Label)
		}
		return list[0], nil // of a multiple field — the first; people() takes them all
	}
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
		case EntityFile: // who uploaded the document
			if s.Files == nil {
				return "", ErrNotFound
			}
			_, by, err := s.Files.BPFile(ctx, x.EntityID)
			if err == nil && by == "" {
				return x.StartedBy.ID, nil
			}
			return by, err
		case EntityList: // who added the element
			e, err := s.element(ctx, x.EntityID)
			if err == nil && e.CreatedBy.ID == "" {
				return x.StartedBy.ID, nil
			}
			return e.CreatedBy.ID, err
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
	if st.Off { // «Выключить»: the step (its block) is not done
		s.log(ctx, x.ID, x.Step, "", "off", "")
		return Skip(x.Steps, x.Step), false, nil
	}
	next = x.Step + 1
	text := s.fill(ctx, x, st.Text)
	switch st.Kind {
	case "approve", "review", "request":
		users, err := s.people(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		// a loop came back: the decisions of the step's last time are in the history, the people decide anew
		if _, err := s.db.Exec(ctx, `DELETE FROM bp_approvals WHERE instance_id = $1 AND step = $2`, x.ID, x.Step); err != nil {
			return next, false, err
		}
		var due *time.Time // «Время на выполнение»
		if st.Timeout > 0 {
			t := time.Now().Add(time.Duration(st.Timeout) * time.Minute)
			due = &t
		}
		for _, user := range users {
			if _, err := s.db.Exec(ctx, `INSERT INTO bp_approvals (instance_id, step, user_id, text, kind, due_at) VALUES ($1, $2, $3, $4, $5, $6)`,
				x.ID, x.Step, user, text, st.Kind, due); err != nil {
				return next, false, err
			}
			s.notify(ctx, user, x.StartedBy.ID, x.ID, map[string]string{"approve": "📝 ", "review": "👁 ", "request": "❓ "}[st.Kind]+text)
			s.feedReader(ctx, x, user)
		}
		s.log(ctx, x.ID, x.Step, "", "step", st.Kind+":"+strings.Join(users, ","))
		return next, true, nil
	case "status": // «Установить статус»: the process's status, as a state of the state-machine processes
		if _, err := s.db.Exec(ctx, `UPDATE bp_instances SET label = $2 WHERE id = $1`, x.ID, text); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "status", text)
		return next, false, nil
	case "commands": // «Команды»: the process waits till one of the people presses a button (Command)
		users, err := s.people(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		for _, user := range users {
			s.notify(ctx, user, x.StartedBy.ID, x.ID, "🔘 "+cmp.Or(text, x.Name))
			s.feedReader(ctx, x, user)
		}
		if st.Timeout > 0 { // «Ожидать не дольше»: «Обработчик событий» — a command or the time, which comes first
			if _, err := s.db.Exec(ctx, `UPDATE bp_instances SET wake_at = $2 WHERE id = $1`, x.ID, time.Now().Add(time.Duration(st.Timeout)*time.Minute)); err != nil {
				return next, false, err
			}
		}
		s.log(ctx, x.ID, x.Step, "", "step", "commands:"+strings.Join(users, ","))
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
	case "post": // «Сообщение в живую ленту»: of From, to the addressees or all employees
		if s.Feed == nil {
			return next, false, i18n.E("err.bp_no_feed")
		}
		author, err := s.who(ctx, x, cmp.Or(st.From, "starter"))
		if err != nil {
			return next, false, err
		}
		var to feed.Audience
		for _, u := range st.Users {
			id, err := s.who(ctx, x, u)
			if err != nil {
				return next, false, err
			}
			if !slices.Contains(to.Users, id) {
				to.Users = append(to.Users, id)
			}
		}
		id, err := s.Feed.CreatePostTo(ctx, author, "", to, s.fill(ctx, x, st.Subject), text, false)
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "post:"+id)
		return next, false, nil
	case "update":
		update := s.updateCard
		if x.Entity == EntityList {
			update = s.updateElement
		}
		err := update(ctx, x, st.Sets)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "update")
		}
		return next, false, err
	case "create":
		id, err := s.createCard(ctx, x, st, text)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", "create:"+CardPath(st.Entity, id))
		}
		return next, false, err
	case "crmupdate", "crmread", "crmconvert", "crmdelete", "crmmove", "crmcopy", "crmobservers", "crmclose", "crmresponsible", "crmproduct":
		did, err := s.crmStep(ctx, x, st)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", did)
		}
		return next, false, err
	case "diskfolder", "diskcopy", "diskread", "diskdelete":
		did, err := s.diskStep(ctx, x, st)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", did)
		}
		return next, false, err
	case "listadd", "listupdate", "listread", "listdelete":
		did, err := s.listStep(ctx, x, st)
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", did)
		}
		return next, false, err
	case "sms", "workday", "workdayinfo", "random":
		var did string
		switch st.Kind {
		case "sms":
			did, err = s.smsStep(ctx, x, text, st)
		case "workday":
			did, err = s.workdayStep(ctx, x, text, st)
		case "workdayinfo":
			did, err = s.workdayInfo(ctx, x, st)
		default:
			f, _ := x.Field(st.Field)
			did, err = s.randomStep(ctx, x, st, f.Kind)
		}
		if err == nil {
			s.log(ctx, x.ID, x.Step, "", "step", did)
		}
		return next, false, err
	case "comment": // «Добавить комментарий»: in the card's timeline, of From
		author, err := s.who(ctx, x, cmp.Or(st.From, "starter"))
		if err != nil {
			return next, false, err
		}
		if err := s.CRM.Comment(ctx, author, x.Entity, x.EntityID, text); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "comment:"+author)
		return next, false, nil
	case "activity":
		id, err := s.activity(ctx, x, st, text)
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "activity:"+id)
		return next, false, nil
	case "document":
		id, err := s.document(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "document:"+id)
		return next, false, nil
	case "if":
		ok, err := s.holds(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		if !ok {
			next = jump(x, st.Goto)
		}
		s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("if:%t", ok))
		return next, false, nil
	case "loop": // «Цикл»: back to st.Goto while the condition holds, MaxLoops times at most
		ok, err := s.holds(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		if ok {
			var n int
			if err := s.db.QueryRow(ctx, `SELECT count(*) FROM bp_log WHERE instance_id = $1 AND step = $2 AND kind = 'step' AND text = 'loop:true'`,
				x.ID, x.Step).Scan(&n); err != nil {
				return next, false, err
			}
			if n >= MaxLoops {
				return next, false, i18n.E("err.bp_loops", MaxLoops)
			}
			next = st.Goto - 1
		}
		s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("loop:%t", ok))
		return next, false, nil
	case "start": // «Запуск бизнес-процесса»: another one, of the starter, with its fields filled; the process goes on
		child, err := s.startChild(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "start:"+child)
		return next, false, nil
	case "goto":
		s.log(ctx, x.ID, x.Step, "", "step", "goto")
		return jump(x, st.Goto), false, nil
	case "foreach": // «Итератор»: the next value of the multiple field into Item, or past the steps when they are over
		return s.each(ctx, x, st)
	case "set":
		if x.Values == nil {
			x.Values = map[string]string{}
		}
		v, err := s.value(ctx, x, st.Value)
		if err != nil {
			return next, false, err
		}
		f, _ := x.Field(st.Field)
		if f.Multiple { // the values one a line or parted by «;»
			v = strings.ReplaceAll(v, ";", Multi)
		}
		x.Values[st.Field] = v
		if f.Kind == "user" && v != "" { // starter, manager, a name, an e-mail — each of a multiple field
			var ids []string
			for _, p := range f.List(v) {
				id, err := s.person(ctx, x, p)
				if err != nil {
					return next, false, err
				}
				if !contains(ids, id) {
					ids = append(ids, id)
				}
			}
			x.Values[st.Field] = strings.Join(ids, Multi)
		} else if f.Kind == "file" { // a file of the process (another field's) only
			v, err := cleanValue(Field{Kind: f.Kind, Multiple: f.Multiple}, x.Values[st.Field])
			if err == nil {
				err = s.claimFiles(ctx, x.ID, "", "", f.List(v))
			}
			if err != nil {
				return next, false, err
			}
			x.Values[st.Field] = v
		} else if f.Kind != "" {
			if v, err := cleanValue(Field{Kind: f.Kind, Options: f.Options, Multiple: f.Multiple}, x.Values[st.Field]); err == nil {
				x.Values[st.Field] = v
			}
		}
		if err := s.saveValues(ctx, x); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "set:"+st.Field)
		return next, false, nil
	case "userinfo": // «Информация о сотруднике»: the person's card into the fields user_<…>
		user, err := s.who(ctx, x, st.To)
		if err != nil {
			return next, false, err
		}
		if err := s.userInfo(ctx, x, user); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "userinfo:"+user)
		return next, false, nil
	case "pick": // «Выбор сотрудника»: one of Users into the user field
		user, err := s.pick(ctx, x, st)
		if err != nil {
			return next, false, err
		}
		if x.Values == nil {
			x.Values = map[string]string{}
		}
		x.Values[st.Field] = user
		if err := s.saveValues(ctx, x); err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, user, "picked", "")
		return next, false, nil
	case "log": // «Запись в отчёт»: the text into the process's history
		s.log(ctx, x.ID, x.Step, "", "report", text)
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
		if note := strings.TrimSpace(s.fill(ctx, x, st.Note)); note != "" {
			desc = note + "\n\n" + desc
		}
		in := tasks.NewTask{Title: text, Description: desc, ResponsibleID: user, Priority: 1, Deadline: deadline,
			RequiresControl: st.Control}
		if st.High {
			in.Priority = 2
		}
		for _, item := range st.Items {
			in.Checklist = append(in.Checklist, s.fill(ctx, x, item))
		}
		for _, l := range []struct {
			from []string
			to   *[]string
		}{{st.Users, &in.Accomplices}, {st.Watchers, &in.Auditors}} {
			for _, to := range l.from {
				u, err := s.who(ctx, x, to)
				if err != nil {
					return next, false, err
				}
				if u != user && !slices.Contains(*l.to, u) {
					*l.to = append(*l.to, u)
				}
			}
		}
		key, err := s.Tasks.CreateTask(actx, x.StartedBy.ID, p, in)
		if err != nil {
			return next, false, err
		}
		id, err := s.Tasks.TaskIDByKey(ctx, key)
		if err != nil {
			return next, false, err
		}
		if crm.Entities[x.Entity] {
			_ = s.Tasks.AddCRMLink(actx, x.StartedBy.ID, id, CardPath(x.Entity, x.EntityID))
		}
		if st.Attach {
			if err := s.attachToTask(ctx, x, id); err != nil {
				return next, false, err
			}
		}
		if x.Values == nil {
			x.Values = map[string]string{}
		}
		x.Values["created_task"] = "/tasks/" + key
		if err := s.saveValues(ctx, x); err != nil {
			return next, false, err
		}
		for _, u := range slices.Concat([]string{user}, in.Accomplices, in.Auditors) {
			s.feedReader(ctx, x, u)
		}
		s.log(ctx, x.ID, x.Step, "", "step", "task:"+key)
		if !st.Wait {
			return next, false, nil
		}
		if x.branch > 0 {
			_, err = s.db.Exec(ctx, `UPDATE bp_branches SET wait_task = $2 WHERE instance_id = $1 AND n = $3`, x.ID, id, x.branch-1)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE bp_instances SET wait_task = $2 WHERE id = $1`, x.ID, id)
		}
		return next, err == nil, err
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
	case "event": // «Событие календаря»: a meeting of each of the people in their own calendar
		ids, err := s.event(ctx, x, st, text)
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "event:"+strings.Join(ids, ","))
		return next, false, nil
	case "end": // «Завершение бизнес-процесса»: the process is over here, its branches and approvals with it
		s.log(ctx, x.ID, x.Step, "", "ended", text)
		s.notify(ctx, x.StartedBy.ID, "", x.ID, "✅ "+x.Name)
		return next, true, s.finish(ctx, x.ID, "done")
	case "wait":
		now := time.Now()
		pause := st.Timeout + st.Days*24*60 // minutes; Days — of the templates before the units
		wake := now.Add(time.Duration(pause) * time.Minute)
		if st.Field != "" { // «до даты» of the field: a date — its beginning; empty or past — no pause
			v := x.Values[st.Field]
			t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local)
			if err != nil {
				t, err = time.ParseInLocation("2006-01-02", v, time.Local)
			}
			if err != nil || !t.After(now) {
				s.log(ctx, x.ID, x.Step, "", "step", "wait:"+st.Field)
				return next, false, nil
			}
			wake = t
		}
		if st.WorkTime { // «Учитывать рабочее время»: days are working days, hours and minutes — working hours
			sched, err := s.Tasks.Schedule(ctx)
			if err != nil {
				return next, false, err
			}
			switch {
			case st.Field != "": // the date out of working time — till the next working hours
				wake = sched.NextWorking(wake)
			case pause%(24*60) == 0:
				wake = sched.AddWorkingDays(now, pause/(24*60))
			default:
				wake = sched.AddWorking(now, time.Duration(pause)*time.Minute)
			}
			if !wake.After(now) {
				s.log(ctx, x.ID, x.Step, "", "step", "wait:"+st.Field)
				return next, false, nil
			}
		}
		if x.branch > 0 {
			_, err = s.db.Exec(ctx, `UPDATE bp_branches SET wake_at = $2 WHERE instance_id = $1 AND n = $3`, x.ID, wake, x.branch-1)
		} else {
			_, err = s.db.Exec(ctx, `UPDATE bp_instances SET wake_at = $2 WHERE id = $1`, x.ID, wake)
		}
		if err != nil {
			return next, false, err
		}
		s.log(ctx, x.ID, x.Step, "", "step", "wait:"+wake.Format(time.RFC3339))
		return next, true, nil
	case "webhook":
		return next, false, s.webhook(ctx, x, st.URL)
	case "app":
		wait, err := s.appStep(ctx, x, st)
		return next, wait, err
	}
	return next, false, fmt.Errorf("unknown step %q", st.Kind)
}

// event puts the meeting of «Событие календаря» into the calendars of the step's people: from the Start field's time (a
// date — the whole day) till End's (none — an hour later, a date — the same day).
func (s *Store) event(ctx context.Context, x Instance, st Step, title string) ([]string, error) {
	if s.Calendar == nil {
		return nil, i18n.E("err.bp_no_calendar")
	}
	at := func(v string) (time.Time, bool, error) {
		if t, err := time.ParseInLocation("2006-01-02T15:04", v, time.Local); err == nil {
			return t, false, nil
		}
		t, err := time.ParseInLocation("2006-01-02", v, time.Local)
		return t, true, err
	}
	start, allDay, err := at(x.Values[st.Start])
	if err != nil {
		return nil, i18n.E("err.bp_event_date")
	}
	end := start.Add(time.Hour)
	if allDay {
		end = start
	}
	if st.End != "" {
		var day bool
		if end, day, err = at(x.Values[st.End]); err != nil {
			return nil, i18n.E("err.bp_event_date")
		}
		allDay = allDay && day
	}
	e := calendar.Event{Title: title, Description: s.fill(ctx, x, st.Note), Start: start, End: end, AllDay: allDay}
	var ids, seen []string
	for _, u := range st.Users {
		user, err := s.who(ctx, x, u)
		if err != nil {
			return nil, err
		}
		if slices.Contains(seen, user) {
			continue
		}
		seen = append(seen, user)
		id, err := s.Calendar.Save(ctx, user, e, nil)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// userInfo puts the employee's card into the process's values (UserInfo).
func (s *Store) userInfo(ctx context.Context, x Instance, user string) error {
	p, err := s.Company.Profile(ctx, user)
	if err != nil {
		return err
	}
	var depts []string
	for _, d := range p.Departments {
		depts = append(depts, d.Name)
	}
	var manager string
	if list, err := s.Company.Managers(ctx, user); err == nil && len(list) > 0 {
		manager = list[0].ID
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	for k, v := range map[string]string{"name": p.Name, "last_name": p.LastName, "first_name": p.FirstName, "email": p.Email,
		"position": p.Position, "work_phone": p.WorkPhone, "mobile": p.MobilePhone, "inner_phone": p.InnerPhone,
		"department": strings.Join(depts, ", "), "manager": manager} {
		x.Values[UserInfoPrefix+k] = v
	}
	return s.saveValues(ctx, x)
}

// pick is whom «Выбор сотрудника» chooses of the step's people (those it cannot name — an empty field, no manager —
// are left out): at random or the one after the last picked, of those at work today when the step skips the absent.
// When all are absent, the reserve (To) or, without one, anyone of them.
func (s *Store) pick(ctx context.Context, x Instance, st Step) (string, error) {
	var all []string
	for _, to := range st.Users {
		if user, err := s.who(ctx, x, to); err == nil && user != "" && !slices.Contains(all, user) {
			all = append(all, user)
		}
	}
	free := all
	if st.Skip {
		free = nil
		for _, user := range all {
			if a, err := s.Company.AbsentNow(ctx, user); err != nil {
				return "", err
			} else if a == nil {
				free = append(free, user)
			}
		}
		if len(free) == 0 && st.To != "" {
			return s.who(ctx, x, st.To)
		}
		if len(free) == 0 {
			free = all
		}
	}
	if len(free) == 0 {
		return "", fmt.Errorf("nobody to pick at step %d", x.Step+1)
	}
	if st.Mode != "queue" || x.TemplateID == "" {
		return free[rand.IntN(len(free))], nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var last string
	err = tx.QueryRow(ctx, `SELECT user_id::text FROM bp_picks WHERE template_id = $1 AND step = $2 FOR UPDATE`, x.TemplateID, x.Step).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	user, after := free[0], slices.Index(all, last)
	for _, u := range free {
		if slices.Index(all, u) > after {
			user = u
			break
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bp_picks (template_id, step, user_id) VALUES ($1, $2, $3)
		ON CONFLICT (template_id, step) DO UPDATE SET user_id = excluded.user_id`, x.TemplateID, x.Step, user); err != nil {
		return "", err
	}
	return user, tx.Commit(ctx)
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
		all := []string{user}
		if key, ok := strings.CutPrefix(to, FieldPrefix); ok { // everyone of a multiple field
			f, _ := x.Field(key)
			all = f.List(x.Values[key])
		}
		for _, user := range all {
			if !seen[user] {
				seen[user] = true
				out = append(out, user)
			}
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
	case "notcontains":
		return !strings.Contains(strings.ToLower(v), strings.ToLower(want))
	case "in":
		for _, w := range Alternatives(want) {
			if compare(v, w) == 0 {
				return true
			}
		}
		return false
	case "between":
		from, to, ok := strings.Cut(want, ";")
		return ok && v != "" && compare(v, strings.TrimSpace(from)) >= 0 && compare(v, strings.TrimSpace(to)) <= 0
	}
	c := compare(v, want)
	switch op {
	case "eq":
		return c == 0
	case "ne":
		return c != 0
	case "gt":
		return c > 0
	case "ge":
		return c >= 0
	case "lt":
		return c < 0
	case "le":
		return c <= 0
	}
	return false
}

// compare: as numbers when both are numbers, as text without case otherwise.
func compare(v, want string) int {
	a, errA := strconv.ParseFloat(strings.ReplaceAll(v, ",", "."), 64)
	b, errB := strconv.ParseFloat(strings.ReplaceAll(want, ",", "."), 64)
	if errA == nil && errB == nil {
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	return strings.Compare(strings.ToLower(v), strings.ToLower(want))
}

// Alternatives are the values of «содержится в»: given by «;», or by «,» when there is no «;» (a number may have a
// comma), or by lines.
func Alternatives(want string) []string {
	sep := ";"
	if strings.Contains(want, Multi) {
		sep = Multi
	} else if !strings.Contains(want, ";") {
		sep = ","
	}
	var out []string
	for _, w := range strings.Split(want, sep) {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// webhookClient waits for n8n a while: a workflow may do some work before it answers.
var webhookClient = &http.Client{Timeout: 30 * time.Second}

// webhook sends the process's data to the URL (n8n's Webhook node). When the answer is a JSON object, its
// values join the process's form, so the next steps can use them as {key}.
func (s *Store) webhook(ctx context.Context, x Instance, target string) error {
	payload := map[string]any{"event": "bp.step", "process": x.Name, "process_id": x.ID, "step": x.Step,
		"link": s.BaseURL + "/bp/i/" + x.ID, "values": x.Values, "starter": map[string]string{"id": x.StartedBy.ID, "name": x.StartedBy.Name}}
	if x.Entity != "none" {
		payload["entity"], payload["entity_id"], payload["card"] = x.Entity, x.EntityID, s.BaseURL+CardPath(x.Entity, x.EntityID)
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
		if strings.ContainsAny(k, " {}") || len(k) > 100 || strings.HasPrefix(k, GlobalPrefix) {
			continue
		}
		switch t := v.(type) {
		case string:
			x.Values[k] = t
		case float64, bool:
			x.Values[k] = fmt.Sprint(t)
		}
	}
	if err := s.saveValues(ctx, x); err != nil {
		return err
	}
	s.log(ctx, x.ID, x.Step, "", "step", "webhook")
	return nil
}

// each is a step of «Итератор»: the values of the multiple field are counted by the step's lines «each:N» of the
// history since its last «each:end» — the next one goes into Item and the steps after the iterator are done; when
// they are over, the process goes past the «Перейти к шагу» back to it.
func (s *Store) each(ctx context.Context, x Instance, st Step) (next int, wait bool, err error) {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM bp_log WHERE instance_id = $1 AND step = $2 AND kind = 'step' AND text LIKE 'each:%'
		AND id > coalesce((SELECT max(id) FROM bp_log WHERE instance_id = $1 AND step = $2 AND kind = 'step' AND text = 'each:end'), 0)`,
		x.ID, x.Step).Scan(&n); err != nil {
		return x.Step + 1, false, err
	}
	f, _ := x.Field(st.Field)
	list := f.List(x.Values[st.Field])
	if n >= len(list) {
		s.log(ctx, x.ID, x.Step, "", "step", "each:end")
		return min(EachEnd(x.Steps, x.Step), jump(x, 0)), false, nil
	}
	if n >= MaxLoops {
		return x.Step + 1, false, i18n.E("err.bp_loops", MaxLoops)
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	x.Values[st.Item] = list[n]
	if err := s.saveValues(ctx, x); err != nil {
		return x.Step + 1, false, err
	}
	s.log(ctx, x.ID, x.Step, "", "step", fmt.Sprintf("each:%d", n+1))
	return x.Step + 1, false, nil
}
