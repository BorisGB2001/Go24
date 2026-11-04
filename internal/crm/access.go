package crm

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Access rights: a level for each kind of card and action «Права доступа» (reading, adding,
// changing, deleting, export, import); deals of a pipeline may have their own levels (key "deal:<pipeline id>", an
// action without a level there — as for all the deals). Administrators in admin mode have every right.

// Levels from the least: none, own, dept, all.
var Levels = []string{"none", "own", "dept", "all"}

// Actions a level is given for.
var Actions = []string{"read", "add", "write", "delete", "export", "import"}

// since says which action's level an action saved before it had (a role saved before adding, export and import).
var since = map[string]string{"add": "write", "export": "read", "import": "write"}

// PipelineKey is the key of the rights on the deals of a pipeline ("deal" — of any).
func PipelineKey(pipelineID string) string {
	if !uuidRe.MatchString(pipelineID) {
		return "deal"
	}
	return "deal:" + pipelineID
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func rank(level string) int {
	for i, l := range Levels {
		if l == level {
			return i
		}
	}
	return 0
}

// Perms are levels by kind of card and action.
type Perms map[string]map[string]string

// Level of an action on a kind of card.
func (p Perms) Level(entity, action string) string {
	if l := p[entity][action]; l != "" {
		return l
	}
	if strings.HasPrefix(entity, "deal:") {
		return p.Level("deal", action)
	}
	return "none"
}

// Pipelines are the pipelines with their own rights.
func (p Perms) Pipelines() []string {
	var out []string
	for k := range p {
		if id, ok := strings.CutPrefix(k, "deal:"); ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Role is a set of rights given to people.
type Role struct {
	ID, Code, Name string
	Default        bool
	Perms          Perms
	Members        []Ref // employees and departments (ID «dept:<id>»)
}

// Roles with their members.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id, coalesce(r.code, ''), r.name, r.is_default, r.perms FROM crm_roles r ORDER BY r.is_default DESC, r.created_at`)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Role, error) {
		var x Role
		var raw []byte
		if err := r.Scan(&x.ID, &x.Code, &x.Name, &x.Default, &raw); err != nil {
			return x, err
		}
		return x, json.Unmarshal(raw, &x.Perms)
	})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, r := range roles {
		idx[r.ID] = i
	}
	rows, err = s.db.Query(ctx, `SELECT m.role_id::text, coalesce(m.user_id::text, 'dept:' || m.department_id::text), coalesce(u.name, d.name, '')
		FROM crm_role_members m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN departments d ON d.id = m.department_id
		ORDER BY m.department_id IS NULL, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var u Ref
		if err := rows.Scan(&role, &u.ID, &u.Name); err != nil {
			return nil, err
		}
		roles[idx[role]].Members = append(roles[idx[role]].Members, u)
	}
	return roles, rows.Err()
}

// cleanPerms keeps the known levels: every action of each kind of card, the ones a pipeline sets for its deals.
func cleanPerms(p Perms) Perms {
	out := Perms{}
	for e := range Entities {
		out[e] = map[string]string{}
		for _, a := range Actions {
			l, ok := p[e][a]
			if !ok && since[a] != "" {
				l = p[e][since[a]]
			}
			if rank(l) == 0 {
				l = "none"
			}
			out[e][a] = l
		}
	}
	for _, id := range p.Pipelines() {
		if PipelineKey(id) == "deal" {
			continue
		}
		own := map[string]string{}
		for _, a := range Actions {
			if l := p["deal:"+id][a]; slices.Contains(Levels, l) {
				own[a] = l
			}
		}
		if len(own) > 0 {
			out["deal:"+id] = own
		}
	}
	return out
}

// SaveRole creates (ID "") or changes a role (administrators; checked by the web layer).
func (s *Store) SaveRole(ctx context.Context, r Role) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if len([]rune(r.Name)) > 100 || (r.ID == "" && r.Name == "") {
		return "", i18n.E("err.crm_role")
	}
	raw, _ := json.Marshal(cleanPerms(r.Perms))
	if r.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO crm_roles (name, perms) VALUES ($1, $2) RETURNING id`, r.Name, raw).Scan(&r.ID)
		return r.ID, err
	}
	// a built-in role keeps its name from the locales unless one is given
	tag, err := s.db.Exec(ctx, `UPDATE crm_roles SET perms = $2, name = CASE WHEN $3 = '' AND code IS NOT NULL THEN name ELSE $3 END
		WHERE id::text = $1`, r.ID, raw, r.Name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return r.ID, err
}

// DeleteRole removes a role (not the default one).
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_roles WHERE id::text = $1 AND NOT is_default`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.crm_role_default")
	}
	return err
}

// SetMember gives a person or a department («dept:<id>», with the ones below) a role or takes it away.
func (s *Store) SetMember(ctx context.Context, roleID, member string, on bool) error {
	col := "user_id"
	if id, ok := strings.CutPrefix(member, "dept:"); ok {
		col, member = "department_id", id
	}
	if !uuidRe.MatchString(member) {
		return ErrNotFound
	}
	var err error
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO crm_role_members (role_id, `+col+`) SELECT id, $2 FROM crm_roles WHERE id::text = $1
			AND NOT is_default ON CONFLICT DO NOTHING`, roleID, member)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM crm_role_members WHERE role_id::text = $1 AND `+col+` = $2`, roleID, member)
	}
	return err
}

// PermsOf are a person's rights: the highest level of their roles (their own and their departments', with the ones
// above), or the default role's when they have none.
func (s *Store) PermsOf(ctx context.Context, userID string) (Perms, error) {
	if tasks.IsAdmin(ctx) {
		all := Perms{}
		for e := range Entities {
			all[e] = map[string]string{}
			for _, a := range Actions {
				all[e][a] = "all"
			}
		}
		return all, nil
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
			UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL),
		mine AS (SELECT role_id FROM crm_role_members WHERE user_id::text = $1 OR department_id IN (SELECT id FROM up))
		SELECT r.perms FROM crm_roles r WHERE r.id IN (SELECT role_id FROM mine)
		UNION ALL SELECT r.perms FROM crm_roles r WHERE r.is_default AND NOT EXISTS (SELECT 1 FROM mine)`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Perms, error) {
		var raw []byte
		var p Perms
		if err := r.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return cleanPerms(p), nil
	})
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for e := range Entities {
		keys = append(keys, e)
	}
	for _, p := range list {
		for _, id := range p.Pipelines() {
			if !slices.Contains(keys, "deal:"+id) {
				keys = append(keys, "deal:"+id)
			}
		}
	}
	out := cleanPerms(nil)
	for _, k := range keys {
		if out[k] == nil {
			out[k] = map[string]string{}
		}
		for _, a := range Actions {
			for _, p := range list {
				if l := p.Level(k, a); rank(l) > rank(out.Level(k, a)) || out[k][a] == "" {
					out[k][a] = l
				}
			}
		}
	}
	return out, nil
}

// scope is the SQL condition of a level on the column of the responsible, with its argument numbered n.
func scope(level, column, n string) string {
	switch level { // every condition names the argument, so the query's arguments always match
	case "all":
		return "(" + n + "::text IS NULL OR true)"
	case "own":
		return column + "::text = " + n
	case "dept":
		return "(" + column + "::text = " + n + ` OR ` + column + ` IN (SELECT o.user_id FROM user_departments o
			WHERE o.department_id IN (SELECT m.department_id FROM user_departments m WHERE m.user_id::text = ` + n + `)))`
	}
	return "(" + n + "::text IS NULL AND false)"
}

// Can: may the person do the action on a card whose responsible is given?
func (s *Store) Can(ctx context.Context, userID, entity, action, responsibleID string) (bool, error) {
	p, err := s.PermsOf(ctx, userID)
	if err != nil {
		return false, err
	}
	switch l := p.Level(entity, action); l {
	case "all":
		return true, nil
	case "own":
		return responsibleID == userID, nil
	case "dept":
		var ok bool
		err := s.db.QueryRow(ctx, `SELECT `+scope("dept", "$2::uuid", "$1"), userID, responsibleID).Scan(&ok)
		return ok, err
	}
	return false, nil
}

// Check is Can as an error.
func (s *Store) Check(ctx context.Context, userID, entity, action, responsibleID string) error {
	ok, err := s.Can(ctx, userID, entity, action, responsibleID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// readScope is the reading condition of a person on a kind of card (alias.responsible_id, and the cards they
// observe), the argument numbered n.
func (s *Store) readScope(ctx context.Context, userID, entity, alias string, n int) (string, error) {
	p, err := s.PermsOf(ctx, userID)
	if err != nil {
		return "", err
	}
	n2 := "$" + strconv.Itoa(n)
	sc, all := scope(p.Level(entity, "read"), alias+".responsible_id", n2), p.Level(entity, "read") == "all"
	if entity == "deal" && len(p.Pipelines()) > 0 { // the deals of a pipeline with its own level
		c := "CASE"
		for _, id := range p.Pipelines() {
			l := p.Level("deal:"+id, "read")
			c += " WHEN " + alias + ".pipeline_id = '" + id + "' THEN " + scope(l, alias+".responsible_id", n2)
			all = all && l == "all"
		}
		sc = c + " ELSE " + sc + " END"
	}
	if watched(entity) && !all { // and the cards they observe
		sc = "(" + sc + " OR " + observerSQL(entity, alias, n2) + ")"
	}
	return sc, nil
}

// viewScope is readScope for a viewer; "" — go24 itself, which sees everything.
func (s *Store) viewScope(ctx context.Context, viewer, entity, alias string, n int) (string, error) {
	if viewer == "" {
		return "$" + strconv.Itoa(n) + "::text IS NULL OR true", nil
	}
	return s.readScope(ctx, viewer, entity, alias, n)
}

// ReadScope is the reading condition of a viewer on a kind of card as alias, the viewer the argument numbered n
// ("" — go24 itself, which sees everything): for the reports of other sections (BI).
func (s *Store) ReadScope(ctx context.Context, viewer, entity, alias string, n int) (string, error) {
	return s.viewScope(ctx, viewer, entity, alias, n)
}

// CheckMove: may the person put the deal into the stage — one of another pipeline asks for adding deals there
// («Сменить воронку»).
func (s *Store) CheckMove(ctx context.Context, userID, dealID, stageID string) error {
	var pipe, resp string
	err := s.db.QueryRow(ctx, `SELECT st.pipeline_id::text, d.responsible_id::text FROM crm_deals d JOIN crm_stages st ON st.id::text = $2
		WHERE d.id::text = $1 AND st.pipeline_id <> d.pipeline_id`, dealID, stageID).Scan(&pipe, &resp)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.Check(ctx, userID, PipelineKey(pipe), "add", resp)
}

// Exportable says which cards of a kind (key — an entity or PipelineKey) the person may export by their
// responsible: none at all — ErrForbidden.
func (s *Store) Exportable(ctx context.Context, userID, key string) (func(responsibleID string) bool, error) {
	p, err := s.PermsOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	switch p.Level(key, "export") {
	case "all":
		return func(string) bool { return true }, nil
	case "own":
		return func(r string) bool { return r == userID }, nil
	case "dept":
		rows, err := s.db.Query(ctx, `SELECT o.user_id::text FROM user_departments o
			WHERE o.department_id IN (SELECT m.department_id FROM user_departments m WHERE m.user_id::text = $1)`, userID)
		if err != nil {
			return nil, err
		}
		team, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		return func(r string) bool { return r == userID || slices.Contains(team, r) }, nil
	}
	return nil, ErrForbidden
}
