package bp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Списки» ----------

// Lists are the universal lists (lists): an administrator makes a list of fields of their own, as a template's
// form; employees add its elements, change and delete them as the list's rights let them. Every change goes to the
// element's history, and the worker starts the templates of the list by it (AutoStartLists), as by the CRM timeline.

// EntityList is what a template of the elements of a list is of (its ListID — of which).
const EntityList = "list"

// MaxListFields: a list has this many fields at most; ListPage — elements a page of its table shows.
const (
	MaxListFields = 100
	ListPage      = 50
)

type List struct {
	ID, Name, Description string
	Fields                []Field
	Columns               []string // the keys of the fields the table shows
	Count                 int      // its elements
	CreatedAt             time.Time
}

// ColumnFields are the fields the table of the elements shows, in the list's order.
func (l List) ColumnFields() []Field {
	var out []Field
	for _, f := range l.Fields {
		if slices.Contains(l.Columns, f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// Element of a list: its name («Название» NAME) and the values of the list's fields.
type Element struct {
	ID, ListID, Name     string
	Values               map[string]string
	CreatedBy, ChangedBy Ref
	CreatedAt, ChangedAt time.Time
}

// Change of an element: of a field (Key "name" — of the name), the values as kept.
type Change struct {
	Key string `json:"key"`
	Old string `json:"old"`
	New string `json:"new"`
}

// ListLine is a line of an element's history: it was added (with the values), or changed.
type ListLine struct {
	Author  Ref
	Created bool
	Changes []Change
	At      time.Time
}

// inRights: a row x of list_rights is of the employee $1 — them or a department of theirs (with its sub-departments).
const inRights = `(x.user_id::text = $1 OR x.department_id IN (
		WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
			UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
		SELECT id FROM dep))`

// listRead: the list l is seen by the employee $1 (an administrator, $2 — every list): «Кто видит» is nobody, or
// them; one who adds and changes the elements sees them too.
const listRead = `($2 OR NOT EXISTS (SELECT 1 FROM list_rights x WHERE x.list_id = l.id AND x.op = 'read')
	OR EXISTS (SELECT 1 FROM list_rights x WHERE x.list_id = l.id AND ` + inRights + `))`

// listWrite: … and its elements are added and changed by them: «Кто добавляет и меняет» is nobody, or them.
const listWrite = `($2 OR ` + listRead + ` AND (NOT EXISTS (SELECT 1 FROM list_rights x WHERE x.list_id = l.id AND x.op = 'write')
	OR EXISTS (SELECT 1 FROM list_rights x WHERE x.list_id = l.id AND x.op = 'write' AND ` + inRights + `)))`

const listSQL = `SELECT l.id::text, l.name, l.description, l.fields, l.columns,
	(SELECT count(*) FROM list_elements e WHERE e.list_id = l.id), l.created_at FROM lists l`

func scanList(r pgx.CollectableRow) (List, error) {
	var l List
	var f, c []byte
	if err := r.Scan(&l.ID, &l.Name, &l.Description, &f, &c, &l.Count, &l.CreatedAt); err != nil {
		return l, err
	}
	if err := json.Unmarshal(f, &l.Fields); err != nil {
		return l, err
	}
	return l, json.Unmarshal(c, &l.Columns)
}

// Lists are those the user sees (administrators — all); guests see none.
func (s *Store) Lists(ctx context.Context, userID string) ([]List, error) {
	if tasks.IsGuestCtx(ctx) {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, listSQL+` WHERE `+listRead+` ORDER BY lower(l.name)`, userID, tasks.IsAdmin(ctx))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanList)
}

// List is one the user sees.
func (s *Store) List(ctx context.Context, userID, id string) (List, error) {
	if tasks.IsGuestCtx(ctx) {
		return List{}, ErrNotFound
	}
	rows, err := s.db.Query(ctx, listSQL+` WHERE l.id::text = $3 AND `+listRead, userID, tasks.IsAdmin(ctx), id)
	if err != nil {
		return List{}, err
	}
	list, err := pgx.CollectRows(rows, scanList)
	if err != nil {
		return List{}, err
	}
	if len(list) == 0 {
		return List{}, ErrNotFound
	}
	return list[0], nil
}

// list is one whoever sees it (a process's step).
func (s *Store) list(ctx context.Context, id string) (List, error) {
	return s.List(tasks.WithAdmin(ctx), "", id)
}

// CanWriteList: the user adds, changes and deletes the elements of the list.
func (s *Store) CanWriteList(ctx context.Context, userID, id string) bool {
	if tasks.IsGuestCtx(ctx) {
		return false
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT `+listWrite+` FROM lists l WHERE l.id::text = $3`, userID, tasks.IsAdmin(ctx), id).Scan(&ok)
	return err == nil && ok
}

// SaveList adds a list (ID "") or changes one, of an administrator: its name, description, fields and columns. The
// values of a field taken away stay with the elements unseen.
func (s *Store) SaveList(ctx context.Context, userID string, l List) (string, error) {
	if !tasks.IsAdmin(ctx) {
		return "", ErrForbidden
	}
	l.Name, l.Description = strings.TrimSpace(l.Name), strings.TrimSpace(l.Description)
	if l.Name == "" || len([]rune(l.Name)) > 200 || len([]rune(l.Description)) > 5000 {
		return "", i18n.E("err.bp_name")
	}
	if len(l.Fields) > MaxListFields {
		return "", i18n.E("err.bp_field")
	}
	seen := map[string]bool{}
	for i, f := range l.Fields {
		l.Fields[i].Scope = "" // a list's field is its element's value, not a parameter, variable or constant
		if seen[f.Key] || f.Key == "name" {
			return "", i18n.E("err.bp_field")
		}
		if err := checkField(f); err != nil {
			return "", err
		}
		seen[f.Key] = true
	}
	l.Columns = slices.DeleteFunc(slices.Clone(l.Columns), func(k string) bool { return !seen[k] })
	f, _ := json.Marshal(append([]Field{}, l.Fields...))
	c, _ := json.Marshal(append([]string{}, l.Columns...))
	if l.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO lists (name, description, fields, columns, created_by) VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text`, l.Name, l.Description, f, c, userID).Scan(&l.ID)
		return l.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE lists SET name = $2, description = $3, fields = $4, columns = $5 WHERE id::text = $1`,
		l.ID, l.Name, l.Description, f, c)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return l.ID, err
}

// DeleteList deletes a list with its elements and the templates of it (an administrator).
func (s *Store) DeleteList(ctx context.Context, id string) error {
	if !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	_, err := s.db.Exec(ctx, `DELETE FROM lists WHERE id::text = $1`, id)
	return err
}

// ListRights are «Кто видит» (op "read") or «Кто добавляет и меняет» ("write") of the list: none — everybody.
func (s *Store) ListRights(ctx context.Context, id, op string) ([]Ref, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(x.user_id::text, 'dept:' || x.department_id::text), coalesce(u.name, d.name, '')
		FROM list_rights x LEFT JOIN users u ON u.id = x.user_id LEFT JOIN departments d ON d.id = x.department_id
		WHERE x.list_id::text = $1 AND x.op = $2 ORDER BY x.department_id IS NULL, coalesce(d.name, u.name)`, id, op)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Ref])
}

// SetListRights chooses who sees the list (op "read") or adds and changes its elements ("write"): people and
// departments («dept:ID»); none — everybody (an administrator).
func (s *Store) SetListRights(ctx context.Context, id, op string, to []string) error {
	if !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	if op != "read" && op != "write" {
		return i18n.E("err.bad_form")
	}
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
	var list string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM lists WHERE id::text = $1 FOR UPDATE`, id).Scan(&list); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM list_rights WHERE list_id = $1 AND op = $2`, list, op); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO list_rights (list_id, op, user_id)
		SELECT $1::uuid, $2, u.id FROM users u WHERE u.id::text = ANY($3) ON CONFLICT DO NOTHING`, list, op, users); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO list_rights (list_id, op, department_id)
		SELECT $1::uuid, $2, d.id FROM departments d WHERE d.id::text = ANY($3) ON CONFLICT DO NOTHING`, list, op, depts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const elementSQL = `SELECT e.id::text, e.list_id::text, e.name, e.values, coalesce(e.created_by::text, ''), coalesce(cu.name, ''),
	coalesce(e.changed_by::text, ''), coalesce(hu.name, ''), e.created_at, e.changed_at
	FROM list_elements e LEFT JOIN users cu ON cu.id = e.created_by LEFT JOIN users hu ON hu.id = e.changed_by`

func scanElement(r pgx.CollectableRow) (Element, error) {
	var e Element
	var v []byte
	if err := r.Scan(&e.ID, &e.ListID, &e.Name, &v, &e.CreatedBy.ID, &e.CreatedBy.Name, &e.ChangedBy.ID, &e.ChangedBy.Name,
		&e.CreatedAt, &e.ChangedAt); err != nil {
		return e, err
	}
	return e, json.Unmarshal(v, &e.Values)
}

// ElementFilter of a list's table: words in the name or the values, the order — "name", "created", "changed" or a
// field's key ("" — the newest first), a page.
type ElementFilter struct {
	Query, Sort   string
	Desc          bool
	Limit, Offset int
}

// Elements of the list l (the user sees it: l is of List).
func (s *Store) Elements(ctx context.Context, l List, f ElementFilter) ([]Element, error) {
	order := "e.created_at DESC"
	dir := map[bool]string{false: " ASC", true: " DESC"}[f.Desc]
	key := ""
	switch f.Sort {
	case "name":
		order = "lower(e.name)" + dir
	case "created":
		order = "e.created_at" + dir
	case "changed":
		order = "e.changed_at" + dir
	default:
		if fl, ok := fieldOf(l.Fields, f.Sort); ok {
			key = fl.Key
			switch fl.Kind {
			case "number":
				order = `nullif(replace(e.values->>$3, ',', '.'), '')::numeric`
			case "user":
				order = `(SELECT lower(u.name) FROM users u WHERE u.id::text = e.values->>$3)`
			default:
				order = `lower(e.values->>$3)`
			}
			order += dir + " NULLS LAST, e.created_at DESC"
		}
	}
	q := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(f.Query))
	rows, err := s.db.Query(ctx, elementSQL+` WHERE e.list_id::text = $1 AND ($2 = '' OR e.name ILIKE '%' || $2 || '%' OR e.values::text ILIKE '%' || $2 || '%')
		AND $3::text IS NOT NULL ORDER BY `+order+` LIMIT $4 OFFSET $5`, l.ID, q, key, cmp.Or(max(f.Limit, 0), 50), f.Offset)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanElement)
}

func (s *Store) element(ctx context.Context, id string) (Element, error) {
	rows, err := s.db.Query(ctx, elementSQL+` WHERE e.id::text = $1`, id)
	if err != nil {
		return Element{}, err
	}
	list, err := pgx.CollectRows(rows, scanElement)
	if err != nil {
		return Element{}, err
	}
	if len(list) == 0 {
		return Element{}, ErrNotFound
	}
	return list[0], nil
}

// Element is one of a list the user sees, with its list.
func (s *Store) Element(ctx context.Context, userID, id string) (Element, List, error) {
	e, err := s.element(ctx, id)
	if err != nil {
		return e, List{}, err
	}
	l, err := s.List(ctx, userID, e.ListID)
	return e, l, err
}

// SaveElement adds an element to its list (ID "") or changes one, of one who adds and changes them: the name, the
// values checked by the fields' kinds, the required ones filled. A value not sent is the field's default for a new
// element and stays as it was for one changed.
func (s *Store) SaveElement(ctx context.Context, userID string, e Element) (string, error) {
	if e.ID != "" {
		old, err := s.element(ctx, e.ID)
		if err != nil {
			return "", err
		}
		e.ListID = old.ListID
	}
	l, err := s.List(ctx, userID, e.ListID)
	if err != nil {
		return "", err
	}
	if !s.CanWriteList(ctx, userID, l.ID) {
		return "", ErrForbidden
	}
	return s.saveElement(ctx, userID, l, e, true)
}

// saveElement saves e of l as the user (rights are checked before); strict — the required fields must be filled
// (a process's step changes what it is told to).
func (s *Store) saveElement(ctx context.Context, userID string, l List, e Element, strict bool) (string, error) {
	e.Name = strings.TrimSpace(e.Name)
	var old Element
	if e.ID != "" {
		var err error
		if old, err = s.element(ctx, e.ID); err != nil {
			return "", err
		}
		if e.Name == "" {
			e.Name = old.Name
		}
		merged := map[string]string{}
		for k, v := range old.Values {
			merged[k] = v
		}
		for k, v := range e.Values {
			merged[k] = v
		}
		e.Values = merged
	}
	if e.Name == "" || len([]rune(e.Name)) > 500 {
		return "", i18n.E("err.list_element_name")
	}
	values, err := cleanValues(l.Fields, e.Values, strict)
	if err != nil {
		return "", err
	}
	for k, v := range old.Values { // the values of the fields taken away from the list stay
		if _, ok := fieldOf(l.Fields, k); !ok {
			values[k] = v
		}
	}
	var changes []Change
	if e.ID != "" {
		if old.Name != e.Name {
			changes = append(changes, Change{Key: "name", Old: old.Name, New: e.Name})
		}
		for _, f := range l.Fields {
			if old.Values[f.Key] != values[f.Key] {
				changes = append(changes, Change{Key: f.Key, Old: old.Values[f.Key], New: values[f.Key]})
			}
		}
		if len(changes) == 0 {
			return e.ID, nil
		}
	} else {
		for _, f := range l.Fields {
			if values[f.Key] != "" && !(f.Kind == "bool" && values[f.Key] == "N") {
				changes = append(changes, Change{Key: f.Key, New: values[f.Key]})
			}
		}
	}
	v, _ := json.Marshal(values)
	c, _ := json.Marshal(append([]Change{}, changes...))
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if e.ID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO list_elements (list_id, name, values, created_by, changed_by) VALUES ($1, $2, $3, $4, $4)
			RETURNING id::text`, l.ID, e.Name, v, optional(userID)).Scan(&e.ID)
	} else {
		_, err = tx.Exec(ctx, `UPDATE list_elements SET name = $2, values = $3, changed_by = $4, changed_at = now() WHERE id::text = $1`,
			e.ID, e.Name, v, optional(userID))
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO list_log (element_id, author_id, created, changes) VALUES ($1, $2, $3, $4)`,
		e.ID, optional(userID), old.ID == "", c); err != nil {
		return "", err
	}
	return e.ID, tx.Commit(ctx)
}

// DeleteElement deletes an element, of one who adds and changes them; its processes stay in «Бизнес-процессы».
func (s *Store) DeleteElement(ctx context.Context, userID, id string) error {
	e, err := s.element(ctx, id)
	if err != nil {
		return err
	}
	if !s.CanWriteList(ctx, userID, e.ListID) {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `DELETE FROM list_elements WHERE id = $1`, e.ID)
	return err
}

// ElementLog is the history of an element, newest first.
func (s *Store) ElementLog(ctx context.Context, id string) ([]ListLine, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(g.author_id::text, ''), coalesce(u.name, ''), g.created, g.changes, g.created_at
		FROM list_log g LEFT JOIN users u ON u.id = g.author_id WHERE g.element_id::text = $1 ORDER BY g.id DESC LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ListLine, error) {
		var l ListLine
		var c []byte
		if err := r.Scan(&l.Author.ID, &l.Author.Name, &l.Created, &c, &l.At); err != nil {
			return l, err
		}
		return l, json.Unmarshal(c, &l.Changes)
	})
}

// listField is the key of the element's field a step sets: "name", a field's key or its label.
func listField(l List, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "name") {
		return "name", true
	}
	for _, f := range l.Fields {
		if f.Key == name || strings.EqualFold(f.Label, name) {
			return f.Key, true
		}
	}
	return "", false
}

// updateElement is «Изменить элемент» of a process of a list's element: the fields set ({…} filled; an employee by
// their name or e-mail too), as the process's starter.
func (s *Store) updateElement(ctx context.Context, x Instance, sets []Assign) error {
	e, err := s.element(ctx, x.EntityID)
	if err != nil {
		return err
	}
	l, err := s.list(ctx, e.ListID)
	if err != nil {
		return err
	}
	e.Values = map[string]string{}
	for _, a := range sets {
		key, ok := listField(l, a.Field)
		if !ok {
			return errors.New("no field " + strconv.Quote(a.Field))
		}
		v := strings.TrimSpace(s.fillRaw(ctx, x, a.Value))
		if key == "name" {
			if v == "" {
				return i18n.E("err.list_element_name")
			}
			e.Name = v
			continue
		}
		if f, _ := fieldOf(l.Fields, key); f.Kind == "user" && v != "" {
			if v, err = s.person(ctx, x, v); err != nil {
				return err
			}
		}
		e.Values[key] = v
	}
	_, err = s.saveElement(ctx, x.StartedBy.ID, l, e, false)
	return err
}

// listValues are the element's fields for the texts of its process: {list.KEY} — a value as people read it.
func (s *Store) listValues(ctx context.Context, id string, shown bool) []string {
	e, err := s.element(ctx, id)
	if err != nil {
		return nil
	}
	l, err := s.list(ctx, e.ListID)
	if err != nil {
		return nil
	}
	var pairs []string
	for _, f := range l.Fields {
		v := e.Values[f.Key]
		if shown {
			v = s.show(ctx, f, v)
		}
		pairs = append(pairs, "{list."+f.Key+"}", v)
	}
	return pairs
}

// AutoStartLists starts the templates of lists set to start by themselves on the elements added or changed since the
// last call (the worker calls it), as AutoStart does: not on an event made while a process of the template ran on the
// element, and once on one element by one batch.
func (s *Store) AutoStartLists(ctx context.Context) []error {
	type event struct {
		id              int64
		element, author string
		created         bool
	}
	var last int64
	if err := s.db.QueryRow(ctx, `SELECT list_id FROM bp_cursor`).Scan(&last); err != nil {
		return []error{err}
	}
	rows, err := s.db.Query(ctx, `SELECT id, element_id::text, coalesce(author_id::text, ''), created FROM list_log
		WHERE id > $1 AND created_at <= now() - make_interval(secs => $3) ORDER BY id LIMIT $2`, last, autoBatch, autoLag)
	if err != nil {
		return []error{err}
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (event, error) {
		var e event
		return e, r.Scan(&e.id, &e.element, &e.author, &e.created)
	})
	if err != nil || len(events) == 0 {
		return errs(err)
	}
	tag, err := s.db.Exec(ctx, `UPDATE bp_cursor SET list_id = $2 WHERE list_id = $1`, last, events[len(events)-1].id)
	if err != nil || tag.RowsAffected() == 0 {
		return errs(err)
	}
	var out []error
	started := map[[3]string]bool{}
	for _, e := range events {
		if e.author == "" {
			continue
		}
		rows, err := s.db.Query(ctx, templateSQL+` t WHERE active AND entity = $1 AND (auto_create AND $3 OR auto_change AND NOT $3)
			AND list_id = (SELECT list_id FROM list_elements WHERE id::text = $2)
			AND NOT EXISTS (SELECT 1 FROM bp_instances i WHERE i.template_id = t.id AND i.entity_id::text = $2
				AND (i.status = 'running' OR i.finished_at >= (SELECT created_at FROM list_log WHERE id = $4)))
			ORDER BY lower(name)`, EntityList, e.element, e.created, e.id)
		if err != nil {
			out = append(out, err)
			continue
		}
		list, err := pgx.CollectRows(rows, scanTemplate)
		if err != nil {
			out = append(out, err)
			continue
		}
		for _, t := range list {
			key := [3]string{t.ID, e.element, strconv.FormatBool(e.created)}
			if started[key] {
				continue
			}
			started[key] = true
			if _, err := s.start(ctx, e.author, t, e.element, nil, true, ""); err != nil {
				out = append(out, err)
			}
		}
	}
	return out
}

// ListTemplates are the active templates of the list's elements.
func (s *Store) ListTemplates(ctx context.Context, listID string) ([]Template, error) {
	rows, err := s.db.Query(ctx, templateSQL+` WHERE active AND entity = $1 AND list_id::text = $2 ORDER BY lower(name)`, EntityList, listID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanTemplate)
}
