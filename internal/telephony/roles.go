package telephony

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Access rights of telephony (the portal «Телефония → Права доступа», migration 456): roles give each right a level;
// a person has the highest level of their roles (as an employee or by a department, with the ones below), one
// without roles — the default role's. Administrators have every right (the web layer).

// Rights are the rights of a role and their levels from the least, in the order of the settings page:
// calls — «Детализация звонков» and the statistics, records — listening to the records and their text (own: the
// person's calls, dept: of the people of their departments and the ones below, all: everyone's); outgoing — calls
// outside (crm: to the clients of CRM only); settings — «Настройки → Телефония».
var Rights = []struct {
	Key    string
	Levels []string
}{
	{"calls", []string{"none", "own", "dept", "all"}},
	{"records", []string{"none", "own", "dept", "all"}},
	{"outgoing", []string{"none", "crm", "all"}},
	{"settings", []string{"none", "all"}},
}

// Perms are levels by right.
type Perms map[string]string

// AllPerms are the administrators' rights.
func AllPerms() Perms {
	p := Perms{}
	for _, r := range Rights {
		p[r.Key] = r.Levels[len(r.Levels)-1]
	}
	return p
}

func levels(key string) []string {
	for _, r := range Rights {
		if r.Key == key {
			return r.Levels
		}
	}
	return nil
}

// Has: is the level of the right at least the given one?
func (p Perms) Has(key, level string) bool {
	l := levels(key)
	i := slices.Index(l, p[key])
	return i >= 0 && i >= slices.Index(l, level)
}

// clean keeps the known rights with their levels (the least one for anything else).
func (p Perms) clean() Perms {
	out := Perms{}
	for _, r := range Rights {
		out[r.Key] = r.Levels[0]
		if slices.Contains(r.Levels, p[r.Key]) {
			out[r.Key] = p[r.Key]
		}
	}
	return out
}

// higher keeps the higher level of each right.
func (p Perms) higher(q Perms) {
	for _, r := range Rights {
		if slices.Index(r.Levels, q[r.Key]) > slices.Index(r.Levels, p[r.Key]) {
			p[r.Key] = q[r.Key]
		}
	}
}

// Member of a role: an employee (ID — theirs) or a department (ID «dept:<id>»), as the people picker gives them.
type Member struct{ ID, Name string }

// Role is a set of rights given to people.
type Role struct {
	ID, Code, Name string
	Default        bool
	Perms          Perms
	Members        []Member
}

// Roles with their members, the default one first.
func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, coalesce(code, ''), name, is_default, perms FROM telephony_roles
		ORDER BY is_default DESC, code IS NULL, created_at`)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Role, error) {
		var x Role
		var raw []byte
		if err := r.Scan(&x.ID, &x.Code, &x.Name, &x.Default, &raw); err != nil {
			return x, err
		}
		if err := json.Unmarshal(raw, &x.Perms); err != nil {
			return x, err
		}
		x.Perms = x.Perms.clean()
		return x, nil
	})
	if err != nil {
		return nil, err
	}
	idx := map[string]int{}
	for i, r := range roles {
		idx[r.ID] = i
	}
	rows, err = s.db.Query(ctx, `SELECT m.role_id::text, coalesce(m.user_id::text, 'dept:' || m.department_id::text),
			coalesce(CASE WHEN u.name <> '' THEN u.name ELSE u.email END, d.name, '')
		FROM telephony_role_members m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN departments d ON d.id = m.department_id
		ORDER BY m.department_id IS NULL, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var m Member
		if err := rows.Scan(&role, &m.ID, &m.Name); err != nil {
			return nil, err
		}
		roles[idx[role]].Members = append(roles[idx[role]].Members, m)
	}
	return roles, rows.Err()
}

// SaveRole creates (ID "") or changes a role with its members: employees and departments («dept:<id>»); the
// default role has no members — it is everyone else's.
func (s *Store) SaveRole(ctx context.Context, r Role, members []string) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if len([]rune(r.Name)) > 100 || (r.ID == "" && r.Name == "") {
		return "", i18n.E("err.tel_role")
	}
	raw, _ := json.Marshal(r.Perms.clean())
	var users, depts []string
	for _, m := range members {
		if d, ok := strings.CutPrefix(m, "dept:"); ok {
			depts = append(depts, d)
		} else if m != "" {
			users = append(users, m)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var def bool
	if r.ID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO telephony_roles (name, perms) VALUES ($1, $2) RETURNING id`, r.Name, raw).Scan(&r.ID)
	} else {
		// a built-in role keeps its name from the locales unless one is given
		err = tx.QueryRow(ctx, `UPDATE telephony_roles SET perms = $2, name = CASE WHEN $3 = '' AND code IS NOT NULL THEN name ELSE $3 END
			WHERE id::text = $1 RETURNING is_default`, r.ID, raw, r.Name).Scan(&def)
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM telephony_role_members WHERE role_id::text = $1`, r.ID); err != nil {
		return "", err
	}
	if !def {
		if _, err := tx.Exec(ctx, `INSERT INTO telephony_role_members (role_id, user_id)
			SELECT $1::uuid, id FROM users WHERE id::text = ANY($2) AND NOT guest ON CONFLICT DO NOTHING`, r.ID, users); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO telephony_role_members (role_id, department_id)
			SELECT $1::uuid, id FROM departments WHERE id::text = ANY($2) ON CONFLICT DO NOTHING`, r.ID, depts); err != nil {
			return "", err
		}
	}
	return r.ID, tx.Commit(ctx)
}

// DeleteRole removes a role (not the default one).
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_roles WHERE id::text = $1 AND NOT is_default`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.tel_role_default")
	}
	return err
}

// rolesOfSQL are the roles of person $1: as an employee or by a department they are in (with the ones above).
const rolesOfSQL = `WITH RECURSIVE up AS (SELECT department_id AS id FROM user_departments WHERE user_id = $1
		UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL)
	SELECT DISTINCT m.role_id FROM telephony_role_members m WHERE m.user_id = $1 OR m.department_id IN (SELECT id FROM up)`

// PermsOf are the person's rights (not counting the administrators').
func (s *Store) PermsOf(ctx context.Context, userID string) (Perms, error) {
	rows, err := s.db.Query(ctx, `WITH mine AS (`+rolesOfSQL+`)
		SELECT perms FROM telephony_roles WHERE id IN (SELECT role_id FROM mine)
			OR (is_default AND NOT EXISTS (SELECT 1 FROM mine))`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	p := Perms{}.clean()
	for _, raw := range list {
		var q Perms
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		p.higher(q.clean())
	}
	return p, nil
}

// Team are the people of the person's departments (and their heads) with the departments below — whose calls the
// level «dept» shows; the person is among them.
func (s *Store) Team(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE down AS (SELECT department_id AS id FROM user_departments WHERE user_id = $1
			UNION SELECT id FROM departments WHERE head_id = $1
			UNION SELECT department_id FROM department_deputies WHERE user_id = $1
			UNION SELECT d.id FROM departments d JOIN down ON d.parent_id = down.id)
		SELECT $1::text UNION SELECT user_id::text FROM user_departments WHERE department_id IN (SELECT id FROM down)
		UNION SELECT head_id::text FROM departments WHERE id IN (SELECT id FROM down) AND head_id IS NOT NULL`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// OutLimits are the employees whose outgoing calls are limited: the level of the right outgoing below «all» by user.
func (s *Store) OutLimits(ctx context.Context, users []string) (map[string]string, error) {
	out := map[string]string{}
	for _, u := range users {
		p, err := s.PermsOf(ctx, u)
		if err != nil {
			return nil, err
		}
		if l := p["outgoing"]; l != "all" {
			out[u] = l
		}
	}
	return out, nil
}
