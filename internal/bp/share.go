package bp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Кто может запускать» ----------

// startFor: the template t is started by the employee $1 — nobody is chosen, or they are, or a department of theirs
// (with its sub-departments).
const startFor = `(NOT EXISTS (SELECT 1 FROM bp_template_starters x WHERE x.template_id = t.id)
	OR EXISTS (SELECT 1 FROM bp_template_starters x WHERE x.template_id = t.id AND (x.user_id::text = $1 OR x.department_id IN (
		WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
			UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
		SELECT id FROM dep))))`

// Ref is one of «Кто может запускать»: an employee, or a department («dept:ID»).
type Ref struct{ ID, Name string }

// Starters are «Кто может запускать» of the template (none — everybody).
func (s *Store) Starters(ctx context.Context, id string) ([]Ref, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(x.user_id::text, 'dept:' || x.department_id::text), coalesce(u.name, d.name, '')
		FROM bp_template_starters x LEFT JOIN users u ON u.id = x.user_id LEFT JOIN departments d ON d.id = x.department_id
		WHERE x.template_id::text = $1 ORDER BY x.department_id IS NULL, coalesce(d.name, u.name)`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Ref])
}

// SetStarters chooses who starts the template by hand: people and departments («dept:ID»); none — everybody.
func (s *Store) SetStarters(ctx context.Context, id string, to []string) error {
	var users, depts []string
	for _, v := range to {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			depts = append(depts, d)
		} else if v != "" {
			users = append(users, v)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tpl string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM bp_templates WHERE id::text = $1 FOR UPDATE`, id).Scan(&tpl); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bp_template_starters WHERE template_id = $1`, tpl); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bp_template_starters (template_id, user_id)
		SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, tpl, users); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bp_template_starters (template_id, department_id)
		SELECT $1::uuid, d.id FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, tpl, depts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Startable are those of the templates the user starts by hand (administrators — all of them).
func (s *Store) Startable(ctx context.Context, userID string, list []Template) ([]Template, error) {
	if tasks.IsAdmin(ctx) || len(list) == 0 {
		return list, nil
	}
	ids := make([]string, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	var ok []string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(t.id::text), '{}') FROM bp_templates t WHERE t.id::text = ANY($2) AND `+startFor,
		userID, ids).Scan(&ok); err != nil {
		return nil, err
	}
	out := []Template{}
	for _, t := range list {
		if contains(ok, t.ID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// MayStart: the user starts the template by hand.
func (s *Store) MayStart(ctx context.Context, userID, id string) (bool, error) {
	if tasks.IsAdmin(ctx) {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM bp_templates t WHERE t.id::text = $2 AND `+startFor+`)`, userID, id).Scan(&ok)
	return ok, err
}

// ---------- export and import ----------

// FileFormat marks a template's file.
const FileFormat = "go24-bp"

// File is a template as «Экспорт» gives it and «Импорт» takes it: the employees, the smart process and the CRM stages are
// of this portal, so the file names them too — another portal finds its own by e-mail and name.
type File struct {
	Format      string            `json:"format"`
	Version     int               `json:"version"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Entity      string            `json:"entity"`
	Type        string            `json:"type,omitempty"` // a smart process's name (Entity "deal")
	List        string            `json:"list,omitempty"` // of a list's elements: the list's name
	AutoCreate  bool              `json:"auto_create,omitempty"`
	AutoChange  bool              `json:"auto_change,omitempty"`
	Fields      []Field           `json:"fields"`
	Steps       []Step            `json:"steps"`
	People      map[string]string `json:"people,omitempty"`    // the employees of the steps: id → e-mail
	Stages      map[string]string `json:"stages,omitempty"`    // the stages of the steps: id → «code:…» or «name:…»
	Templates   map[string]string `json:"templates,omitempty"` // the templates «Запуск бизнес-процесса» starts: id → name
	Docs        map[string]string `json:"docs,omitempty"`      // the templates of documents «Создание документа» makes: id → name
}

// person: who of a step is an employee, not «the starter», «the manager», a user field and the like.
func person(v string) bool {
	return v != "" && !contains([]string{"starter", "manager", "responsible", "client"}, v) && !strings.HasPrefix(v, FieldPrefix)
}

// eachPerson calls f on every employee a step names; f returns the new value ("" drops one of Users, Watchers).
func eachPerson(st *Step, f func(string) string) {
	for _, p := range []*string{&st.To, &st.From} {
		if person(*p) {
			*p = f(*p)
		}
	}
	for _, list := range []*[]string{&st.Users, &st.Watchers} {
		var users []string
		for _, u := range *list {
			if person(u) {
				u = f(u)
			}
			if u != "" {
				users = append(users, u)
			}
		}
		*list = users
	}
}

// eachDefault calls f on the default of every user field (of the form and of the requests) and keeps what it returns.
func eachDefault(t *Template, f func(string) string) {
	do := func(list []Field) {
		for i := range list {
			if list[i].Kind == "user" && list[i].Default != "" {
				list[i].Default = f(list[i].Default)
			}
		}
	}
	do(t.Fields)
	for i := range t.Steps {
		do(t.Steps[i].Fields)
	}
}

// Export is the template as a file.
func (s *Store) Export(ctx context.Context, id string) (File, error) {
	t, err := s.Template(ctx, id)
	if err != nil {
		return File{}, err
	}
	f := File{Format: FileFormat, Version: 1, Name: t.Name, Description: t.Description, Entity: t.Entity, AutoCreate: t.AutoCreate,
		AutoChange: t.AutoChange, Fields: t.Fields, Steps: t.Steps, People: map[string]string{}, Stages: map[string]string{},
		Templates: map[string]string{}, Docs: map[string]string{}}
	if f.Fields == nil {
		f.Fields = []Field{}
	}
	if t.TypeID != "" {
		if err := s.db.QueryRow(ctx, `SELECT name FROM crm_types WHERE id::text = $1`, t.TypeID).Scan(&f.Type); err != nil {
			return File{}, err
		}
	}
	if t.ListID != "" {
		if err := s.db.QueryRow(ctx, `SELECT name FROM lists WHERE id::text = $1`, t.ListID).Scan(&f.List); err != nil {
			return File{}, err
		}
	}
	var people, stages []string
	eachDefault(&t, func(v string) string { people = append(people, v); return v })
	for i := range t.Steps {
		eachPerson(&t.Steps[i], func(v string) string { people = append(people, v); return v })
		if t.Steps[i].Stage != "" {
			stages = append(stages, t.Steps[i].Stage)
		}
		if other := t.Steps[i].Template; other != "" {
			if o, err := s.Template(ctx, other); err == nil {
				f.Templates[other] = o.Name
			}
		}
		if doc := t.Steps[i].Doc; doc != "" {
			if d, err := s.CRM.DocTemplate(ctx, doc); err == nil {
				f.Docs[doc] = d.Name
			}
		}
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, email FROM users WHERE id::text = ANY($1)`, people)
	if err != nil {
		return File{}, err
	}
	var uid, email string
	if _, err := pgx.ForEachRow(rows, []any{&uid, &email}, func() error { f.People[uid] = email; return nil }); err != nil {
		return File{}, err
	}
	rows, err = s.db.Query(ctx, `SELECT id::text, CASE WHEN coalesce(code, '') <> '' THEN 'code:' || code ELSE 'name:' || name END
		FROM crm_stages WHERE id::text = ANY($1)`, stages)
	if err != nil {
		return File{}, err
	}
	var sid, key string
	if _, err := pgx.ForEachRow(rows, []any{&sid, &key}, func() error { f.Stages[sid] = key; return nil }); err != nil {
		return File{}, err
	}
	return f, nil
}

// Import puts a template's file into a new template (id "") or over the template id (its activity and «Кто может
// запускать» stay). An employee of the file is the one of this portal with the id or else the e-mail; one not found
// becomes «the starter» (or is left out of the several people of a step) — missing counts them. A smart process is
// found by its name, a stage — by its code or name among those of the template's cards, a template started — by its name.
func (s *Store) Import(ctx context.Context, userID, id string, data []byte) (newID string, missing int, err error) {
	var f File
	if json.Unmarshal(data, &f) != nil || f.Format != FileFormat || f.Version != 1 {
		return "", 0, i18n.E("err.bp_import_file")
	}
	t := Template{Active: true}
	if id != "" {
		if t, err = s.Template(ctx, id); err != nil {
			return "", 0, err
		}
	}
	t.Name, t.Description, t.Entity, t.TypeID, t.ListID, t.AutoCreate, t.AutoChange = f.Name, f.Description, f.Entity, "", "", f.AutoCreate, f.AutoChange
	t.Fields, t.Steps = f.Fields, f.Steps
	if f.Type != "" {
		if err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_types WHERE lower(name) = lower($1) ORDER BY created_at LIMIT 1`,
			f.Type).Scan(&t.TypeID); errors.Is(err, pgx.ErrNoRows) {
			return "", 0, i18n.E("err.bp_import_type", f.Type)
		} else if err != nil {
			return "", 0, err
		}
	}
	if f.List != "" {
		if err := s.db.QueryRow(ctx, `SELECT id::text FROM lists WHERE lower(name) = lower($1) ORDER BY created_at LIMIT 1`,
			f.List).Scan(&t.ListID); errors.Is(err, pgx.ErrNoRows) {
			return "", 0, i18n.E("err.bp_import_list", f.List)
		} else if err != nil {
			return "", 0, err
		}
	}
	local := func(v string) (string, bool) {
		var uid string
		err := s.db.QueryRow(ctx, `SELECT id::text FROM users WHERE active AND (id::text = $1 OR lower(email) = lower($2) AND $2 <> '')
			ORDER BY id::text = $1 DESC LIMIT 1`, v, f.People[v]).Scan(&uid)
		return uid, err == nil
	}
	eachDefault(&t, func(v string) string { // one not found — no default
		if uid, ok := local(v); ok {
			return uid
		}
		missing++
		return ""
	})
	for i := range t.Steps {
		st := &t.Steps[i]
		eachPerson(st, func(v string) string {
			if uid, ok := local(v); ok {
				return uid
			}
			missing++
			if v == st.To || v == st.From {
				return "starter"
			}
			return ""
		})
		if st.Stage != "" {
			if st.Stage, err = s.importStage(ctx, t, st.Stage, f.Stages[st.Stage]); err != nil {
				return "", 0, i18n.E("err.bp_import_stage", i+1)
			}
		}
		if st.Template != "" { // the same template, or one of the name
			if err := s.db.QueryRow(ctx, `SELECT id::text FROM bp_templates WHERE id::text = $1 OR lower(name) = lower($2) AND $2 <> ''
				ORDER BY id::text = $1 DESC, created_at LIMIT 1`, st.Template, f.Templates[st.Template]).Scan(&st.Template); err != nil {
				return "", 0, i18n.E("err.bp_import_template", i+1, f.Templates[st.Template])
			}
		}
		if st.Doc != "" { // the same template of documents, or one of the name for the card's kind
			if err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_doc_templates WHERE entity = $3 AND (id::text = $1 OR lower(name) = lower($2) AND $2 <> '')
				ORDER BY id::text = $1 DESC, created_at LIMIT 1`, st.Doc, f.Docs[st.Doc], t.Entity).Scan(&st.Doc); err != nil {
				return "", 0, i18n.E("err.bp_import_doc", i+1, f.Docs[st.Doc])
			}
		}
	}
	newID, err = s.SaveTemplate(ctx, userID, t)
	return newID, missing, err
}

// importStage is the stage of this portal: the same id among the stages of the template's cards, or else one with
// the code or name of key («code:…», «name:…»).
func (s *Store) importStage(ctx context.Context, t Template, id, key string) (string, error) {
	code, _ := strings.CutPrefix(key, "code:")
	name, _ := strings.CutPrefix(key, "name:")
	if code == key {
		code = ""
	}
	if name == key {
		name = ""
	}
	var out string
	err := s.db.QueryRow(ctx, `SELECT st.id::text FROM crm_stages st LEFT JOIN crm_pipelines p ON p.id = st.pipeline_id
		WHERE st.entity = $1 AND ($1 <> 'deal' OR coalesce(p.type_id::text, '') = $2)
			AND (st.id::text = $3 OR $4 <> '' AND st.code = $4 OR $5 <> '' AND lower(st.name) = lower($5))
		ORDER BY st.id::text = $3 DESC, p.sort, st.sort LIMIT 1`, t.Entity, t.TypeID, id, code, name).Scan(&out)
	return out, err
}
