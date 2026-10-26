package tasks

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Access rights of the tasks (the portal «Права доступа»): roles give each right a level; the database function
// task_perm (migration 257) gives a person the highest of their roles, so lists (visibleSQL) and cards agree.
// Administrators in admin mode have every right.

// levelOrder ranks the levels from the least.
var levelOrder = []string{"none", "own", "sub", "all"}

// PermLevels are the rights of a role and the levels each can have, in the order of the settings page.
var PermLevels = []struct {
	Key    string
	Levels []string
}{
	{"view", []string{"own", "sub", "all"}},
	{"edit", []string{"own", "sub", "all"}},
	{"delete", []string{"own", "sub", "all"}},
	{"assign", []string{"sub", "all"}},
	{"export", []string{"none", "all"}},
	{"import", []string{"none", "all"}},
}

// AccessPerms are levels by right.
type AccessPerms map[string]string

// Has: is the level of the right at least the given one?
func (p AccessPerms) Has(key, level string) bool {
	return slices.Index(levelOrder, p[key]) >= slices.Index(levelOrder, level)
}

// clean keeps the known rights with their allowed levels (the least one for anything else).
func (p AccessPerms) clean() AccessPerms {
	out := AccessPerms{}
	for _, pl := range PermLevels {
		out[pl.Key] = pl.Levels[0]
		if slices.Contains(pl.Levels, p[pl.Key]) {
			out[pl.Key] = p[pl.Key]
		}
	}
	return out
}

// AccessRole is a set of rights given to people.
type AccessRole struct {
	ID, Code, Name string
	Default        bool
	Perms          AccessPerms
	Members        []User
}

// AccessRoles with their members.
func (s *Store) AccessRoles(ctx context.Context) ([]AccessRole, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, coalesce(code, ''), name, is_default, perms FROM task_roles ORDER BY is_default DESC, created_at`)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AccessRole, error) {
		var x AccessRole
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
	rows, err = s.db.Query(ctx, `SELECT m.role_id::text, u.id, u.name, u.email, u.guest FROM task_role_members m
		JOIN users u ON u.id = m.user_id ORDER BY u.name, u.email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var role string
		var u User
		if err := rows.Scan(&role, &u.ID, &u.Name, &u.Email, &u.Guest); err != nil {
			return nil, err
		}
		roles[idx[role]].Members = append(roles[idx[role]].Members, u)
	}
	return roles, rows.Err()
}

// SaveAccessRole creates (ID "") or changes a role (administrators; checked by the web layer).
func (s *Store) SaveAccessRole(ctx context.Context, r AccessRole) (string, error) {
	r.Name = strings.TrimSpace(r.Name)
	if len([]rune(r.Name)) > 100 || (r.ID == "" && r.Name == "") {
		return "", i18n.E("err.task_role")
	}
	raw, _ := json.Marshal(r.Perms.clean())
	if r.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO task_roles (name, perms) VALUES ($1, $2) RETURNING id`, r.Name, raw).Scan(&r.ID)
		return r.ID, err
	}
	// a built-in role keeps its name from the locales unless one is given
	tag, err := s.db.Exec(ctx, `UPDATE task_roles SET perms = $2, name = CASE WHEN $3 = '' AND code IS NOT NULL THEN name ELSE $3 END
		WHERE id::text = $1`, r.ID, raw, r.Name)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return r.ID, err
}

// DeleteAccessRole removes a role (not the default one).
func (s *Store) DeleteAccessRole(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM task_roles WHERE id::text = $1 AND NOT is_default`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.task_role_default")
	}
	return err
}

// SetAccessMember gives a person a role or takes it away.
func (s *Store) SetAccessMember(ctx context.Context, roleID, userID string, on bool) error {
	var err error
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO task_role_members (role_id, user_id) SELECT r.id, u.id FROM task_roles r, users u
			WHERE r.id::text = $1 AND u.id::text = $2 ON CONFLICT DO NOTHING`, roleID, userID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM task_role_members WHERE role_id::text = $1 AND user_id::text = $2`, roleID, userID)
	}
	return err
}

// PermsOf are the person's rights.
func (s *Store) PermsOf(ctx context.Context, userID string) (AccessPerms, error) {
	return permsOf(ctx, s.db, userID)
}

func permsOf(ctx context.Context, q querier, userID string) (AccessPerms, error) {
	p := AccessPerms{}
	if IsAdmin(ctx) {
		for _, pl := range PermLevels {
			p[pl.Key] = "all"
		}
		return p, nil
	}
	if userID == "" {
		return p.clean(), nil
	}
	vals := make([]string, len(PermLevels))
	cols, dest := make([]string, len(PermLevels)), make([]any, len(PermLevels))
	for i, pl := range PermLevels {
		cols[i], dest[i] = "task_perm($1::uuid, '"+pl.Key+"')", &vals[i]
	}
	if err := q.QueryRow(ctx, "SELECT "+strings.Join(cols, ", "), userID).Scan(dest...); err != nil {
		return nil, err
	}
	for i, pl := range PermLevels {
		p[pl.Key] = vals[i]
	}
	return p, nil
}

// mayAssign: may the person make someone else the responsible of a task (the portal «Кому можно ставить задачи»)?
// Robots, triggers and flows hand tasks out by their own rules.
func mayAssign(ctx context.Context, q querier, actorID, responsibleID string) error {
	if responsibleID == "" || responsibleID == actorID || isRobot(ctx) || IsAdmin(ctx) {
		return nil
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT task_perm($1::uuid, 'assign') = 'all' OR $2::uuid IN `+underSQL("$1::uuid"),
		actorID, responsibleID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return i18n.E("err.assign_forbidden")
	}
	return nil
}
