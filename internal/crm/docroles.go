package crm

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Access rights of the document generator (the portal «Генератор документов → Права доступа», migration 963): roles give
// each right a level; a person has the highest level of their roles (as an employee or by a department, with the ones
// above), one without roles — the default role's. Administrators have every right (the web layer).

// DocRights are the rights of a role and their levels from the least, in the order of the settings page, as the
// permissions of documentgenerator: templates — changing the templates (TEMPLATES MODIFY); view — seeing the documents
// of the cards one sees (DOCUMENTS VIEW); modify — making, making again and deleting them, the public link (DOCUMENTS
// MODIFY); settings — «Моя компания», the stamp and the facsimiles, the numerators (SETTINGS MODIFY).
var DocRights = []struct {
	Key    string
	Levels []string
}{
	{"templates", []string{"none", "all"}},
	{"view", []string{"none", "all"}},
	{"modify", []string{"none", "all"}},
	{"settings", []string{"none", "all"}},
}

// DocPerms are levels by right.
type DocPerms map[string]string

// AllDocPerms are the administrators' rights.
func AllDocPerms() DocPerms {
	p := DocPerms{}
	for _, r := range DocRights {
		p[r.Key] = r.Levels[len(r.Levels)-1]
	}
	return p
}

// Can: is the right given at all (its level above the least)?
func (p DocPerms) Can(key string) bool {
	for _, r := range DocRights {
		if r.Key == key {
			return slices.Index(r.Levels, p[key]) > 0
		}
	}
	return false
}

// Clean keeps the known rights with their levels (the least one for anything else); seeing the documents comes with
// changing them.
func (p DocPerms) Clean() DocPerms {
	out := DocPerms{}
	for _, r := range DocRights {
		out[r.Key] = r.Levels[0]
		if slices.Contains(r.Levels, p[r.Key]) {
			out[r.Key] = p[r.Key]
		}
	}
	if out.Can("modify") {
		out["view"] = "all"
	}
	return out
}

func (p DocPerms) higher(q DocPerms) {
	for _, r := range DocRights {
		if slices.Index(r.Levels, q[r.Key]) > slices.Index(r.Levels, p[r.Key]) {
			p[r.Key] = q[r.Key]
		}
	}
}

// DocRole is a set of rights given to people: employees (Ref.ID — theirs) and departments (Ref.ID «dept:<id>»).
type DocRole struct {
	ID, Code, Name string
	Num            int64
	Default        bool
	Perms          DocPerms
	Members        []Ref
}

// DocRoles with their members, the default one first.
func (s *Store) DocRoles(ctx context.Context) ([]DocRole, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, num, coalesce(code, ''), name, is_default, perms FROM docgen_roles
		ORDER BY is_default DESC, code IS NULL, created_at, num`)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (DocRole, error) {
		var x DocRole
		var raw []byte
		if err := r.Scan(&x.ID, &x.Num, &x.Code, &x.Name, &x.Default, &raw); err != nil {
			return x, err
		}
		if err := json.Unmarshal(raw, &x.Perms); err != nil {
			return x, err
		}
		x.Perms = x.Perms.Clean()
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
		FROM docgen_role_members m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN departments d ON d.id = m.department_id
		ORDER BY m.department_id IS NULL, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var m Ref
		if err := rows.Scan(&role, &m.ID, &m.Name); err != nil {
			return nil, err
		}
		roles[idx[role]].Members = append(roles[idx[role]].Members, m)
	}
	return roles, rows.Err()
}

// SaveDocRole creates (ID "") or changes a role; members (employees and «dept:<id>») replace its members unless nil.
// The default role has no members — it is everyone else's.
func (s *Store) SaveDocRole(ctx context.Context, r DocRole, members []string) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if len([]rune(r.Name)) > 100 || (r.ID == "" && r.Name == "") {
		return "", i18n.E("err.doc_role")
	}
	raw, _ := json.Marshal(r.Perms.Clean())
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if r.ID == "" {
		err = tx.QueryRow(ctx, `INSERT INTO docgen_roles (name, perms) VALUES ($1, $2) RETURNING id`, r.Name, raw).Scan(&r.ID)
	} else {
		// a built-in role keeps its name from the locales unless one is given
		err = tx.QueryRow(ctx, `UPDATE docgen_roles SET perms = $2, name = CASE WHEN $3 = '' AND code IS NOT NULL THEN name ELSE $3 END
			WHERE id::text = $1 RETURNING id`, r.ID, raw, r.Name).Scan(&r.ID)
		if err == pgx.ErrNoRows {
			err = ErrNotFound
		}
	}
	if err != nil {
		return "", err
	}
	if members != nil {
		if err := setDocRoleMembers(ctx, tx, r.ID, members); err != nil {
			return "", err
		}
	}
	return r.ID, tx.Commit(ctx)
}

func setDocRoleMembers(ctx context.Context, tx pgx.Tx, role string, members []string) error {
	var users, depts []string
	for _, m := range members {
		if d, ok := strings.CutPrefix(m, "dept:"); ok {
			depts = append(depts, d)
		} else if m != "" {
			users = append(users, m)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM docgen_role_members WHERE role_id::text = $1`, role); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO docgen_role_members (role_id, user_id)
		SELECT r.id, u.id FROM docgen_roles r, users u WHERE r.id::text = $1 AND NOT r.is_default AND u.id::text = ANY($2) AND NOT u.guest
		ON CONFLICT DO NOTHING`, role, users); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO docgen_role_members (role_id, department_id)
		SELECT r.id, d.id FROM docgen_roles r, departments d WHERE r.id::text = $1 AND NOT r.is_default AND d.id::text = ANY($2)
		ON CONFLICT DO NOTHING`, role, depts)
	return err
}

// SetDocRoleMembers gives the roles their members anew, as documentgenerator.role.fillaccesses: the roles not given
// are left with none.
func (s *Store) SetDocRoleMembers(ctx context.Context, members map[string][]string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM docgen_role_members`); err != nil {
		return err
	}
	for role, list := range members {
		if err := setDocRoleMembers(ctx, tx, role, list); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// DeleteDocRole removes a role (not the default one).
func (s *Store) DeleteDocRole(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM docgen_roles WHERE id::text = $1 AND NOT is_default`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.doc_role_default")
	}
	return err
}

// DocPermsOf are the person's rights (not counting the administrators'): of the roles they are given as an employee
// or by a department they are in (with the ones above), else of the default role.
func (s *Store) DocPermsOf(ctx context.Context, userID string) (DocPerms, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (SELECT department_id AS id FROM user_departments WHERE user_id = $1
			UNION SELECT id FROM departments WHERE head_id = $1
			UNION SELECT department_id FROM department_deputies WHERE user_id = $1
			UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL),
		mine AS (SELECT DISTINCT m.role_id FROM docgen_role_members m WHERE m.user_id = $1 OR m.department_id IN (SELECT id FROM up))
		SELECT perms FROM docgen_roles WHERE id IN (SELECT role_id FROM mine)
			OR (is_default AND NOT EXISTS (SELECT 1 FROM mine))`, userID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, err
	}
	p := DocPerms{}.Clean()
	for _, raw := range list {
		var q DocPerms
		if err := json.Unmarshal(raw, &q); err != nil {
			return nil, err
		}
		p.higher(q.Clean())
	}
	return p, nil
}
