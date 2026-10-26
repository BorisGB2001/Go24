package crm

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Access rights: a level for each kind of card and action. Administrators in admin mode have every right.

// Levels from the least: none, own, dept, all.
var Levels = []string{"none", "own", "dept", "all"}

// Actions a level is given for.
var Actions = []string{"read", "write", "delete"}

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
	return "none"
}

// Role is a set of rights given to people.
type Role struct {
	ID, Code, Name string
	Default        bool
	Perms          Perms
	Members        []Ref
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
	rows, err = s.db.Query(ctx, `SELECT m.role_id::text, u.id, u.name FROM crm_role_members m JOIN users u ON u.id = m.user_id ORDER BY u.name`)
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

func cleanPerms(p Perms) Perms {
	out := Perms{}
	for e := range Entities {
		out[e] = map[string]string{}
		for _, a := range Actions {
			l := p[e][a]
			if rank(l) == 0 {
				l = "none"
			}
			out[e][a] = l
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

// SetMember gives a person a role or takes it away.
func (s *Store) SetMember(ctx context.Context, roleID, userID string, on bool) error {
	var err error
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO crm_role_members (role_id, user_id) SELECT id, $2 FROM crm_roles WHERE id::text = $1 ON CONFLICT DO NOTHING`, roleID, userID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM crm_role_members WHERE role_id::text = $1 AND user_id = $2`, roleID, userID)
	}
	return err
}

// PermsOf are a person's rights: the highest level of their roles, or the default role's when they have none.
func (s *Store) PermsOf(ctx context.Context, userID string) (Perms, error) {
	if tasks.IsAdmin(ctx) {
		all := Perms{}
		for e := range Entities {
			all[e] = map[string]string{"read": "all", "write": "all", "delete": "all"}
		}
		return all, nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.perms FROM crm_roles r WHERE r.id IN (SELECT role_id FROM crm_role_members WHERE user_id::text = $1)
		UNION ALL SELECT r.perms FROM crm_roles r WHERE r.is_default AND NOT EXISTS (SELECT 1 FROM crm_role_members WHERE user_id::text = $1)`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Perms, error) {
		var raw []byte
		var p Perms
		if err := r.Scan(&raw); err != nil {
			return nil, err
		}
		return p, json.Unmarshal(raw, &p)
	})
	if err != nil {
		return nil, err
	}
	out := cleanPerms(nil)
	for _, p := range list {
		for e := range Entities {
			for _, a := range Actions {
				if rank(p.Level(e, a)) > rank(out[e][a]) {
					out[e][a] = p.Level(e, a)
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
	sc := scope(p.Level(entity, "read"), alias+".responsible_id", n2)
	if watched(entity) && p.Level(entity, "read") != "all" { // and the cards they observe
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
