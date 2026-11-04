package company

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// «Права доступа» of the company structure: an administrator sets what the heads of departments,
// their deputies and every employee may do — nothing, in their own departments (with the sub-departments) or
// everywhere. Administrators may do everything; guests nothing.

// StructRoles are the roles the rights are set for; StructActions — what they allow.
var (
	StructRoles   = []string{"head", "deputy", "employee"}
	StructActions = []string{"create", "edit", "delete", "staff", "chats"}
	StructScopes  = []string{"", "own", "all"}
)

// StructRights: role → action → scope ("own" or "all"; none — not there).
type StructRights map[string]map[string]string

// Scope is the role's scope of the action ("" — none).
func (r StructRights) Scope(role, action string) string { return r[role][action] }

// StructRights reads the rights.
func (s *Store) StructRights(ctx context.Context) (StructRights, error) {
	return s.readRights(ctx, "structure_rights", StructActions)
}

// SetStructRights replaces the rights (an administrator; checked by the caller): unknown roles, actions and scopes
// are dropped.
func (s *Store) SetStructRights(ctx context.Context, in StructRights) error {
	return s.writeRights(ctx, "structure_rights", StructActions, func(string) []string { return StructScopes }, in)
}

// readRights reads a table of role rights (role, action, scope).
func (s *Store) readRights(ctx context.Context, table string, actions []string) (StructRights, error) {
	rows, err := s.db.Query(ctx, `SELECT role, action, scope FROM `+table)
	if err != nil {
		return nil, err
	}
	out := StructRights{}
	for _, role := range StructRoles {
		out[role] = map[string]string{}
	}
	var role, action, scope string
	_, err = pgx.ForEachRow(rows, []any{&role, &action, &scope}, func() error {
		if out[role] != nil && slices.Contains(actions, action) {
			out[role][action] = scope
		}
		return nil
	})
	return out, err
}

// writeRights replaces a table of role rights; scopes tells the scopes allowed for an action.
func (s *Store) writeRights(ctx context.Context, table string, actions []string, scopes func(string) []string, in StructRights) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table); err != nil {
			return err
		}
		for _, role := range StructRoles {
			for _, action := range actions {
				scope := in[role][action]
				if scope == "" || !slices.Contains(scopes(action), scope) {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (role, action, scope) VALUES ($1, $2, $3)`,
					role, action, scope); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StructAccess is what a person may do in the structure: everywhere, or in the listed departments.
type StructAccess struct {
	All  map[string]bool            // action → everywhere
	Own  map[string]map[string]bool // action → department ids (own ones with their sub-departments)
	none bool
}

// Can: the action in the department.
func (a StructAccess) Can(action, deptID string) bool {
	return !a.none && (a.All[action] || a.Own[action][deptID])
}

// Any: the action somewhere.
func (a StructAccess) Any(action string) bool {
	return !a.none && (a.All[action] || len(a.Own[action]) > 0)
}

// FullStructAccess is an administrator's: everything everywhere.
func FullStructAccess() StructAccess { return fullAccess(StructActions) }

func fullAccess(actions []string) StructAccess {
	a := StructAccess{All: map[string]bool{}, Own: map[string]map[string]bool{}}
	for _, action := range actions {
		a.All[action] = true
	}
	return a
}

// StructAccessOf works out the person's access: the rights of the roles they hold — head and deputy where they are
// ones, employee for everyone not a guest.
func (s *Store) StructAccessOf(ctx context.Context, userID string, admin bool) (StructAccess, error) {
	if admin {
		return FullStructAccess(), nil
	}
	a := StructAccess{All: map[string]bool{}, Own: map[string]map[string]bool{}}
	var guest bool
	err := s.db.QueryRow(ctx, `SELECT guest OR NOT active FROM users WHERE id::text = $1`, userID).Scan(&guest)
	if err == pgx.ErrNoRows || guest {
		return StructAccess{none: true}, nil
	}
	if err != nil {
		return a, err
	}
	rights, err := s.StructRights(ctx)
	if err != nil {
		return a, err
	}
	return a, s.roleAccess(ctx, userID, rights, StructActions, &a)
}

// roleAccess fills the access from the rights of the roles the person holds — head and deputy where they are ones,
// employee for everyone: «own» are the departments of the role with their sub-departments.
func (s *Store) roleAccess(ctx context.Context, userID string, rights StructRights, actions []string, a *StructAccess) error {
	// the departments of each role with their sub-departments
	rows, err := s.db.Query(ctx, `WITH RECURSIVE base AS (
			SELECT 'head' AS role, id FROM departments WHERE head_id::text = $1
			UNION SELECT 'deputy', department_id FROM department_deputies WHERE user_id::text = $1
			UNION SELECT 'employee', department_id FROM user_departments WHERE user_id::text = $1),
		sub AS (SELECT role, id FROM base UNION SELECT sub.role, d.id FROM departments d JOIN sub ON d.parent_id = sub.id)
		SELECT DISTINCT role, id::text FROM sub`, userID)
	if err != nil {
		return err
	}
	own := map[string][]string{}
	var role, id string
	if _, err := pgx.ForEachRow(rows, []any{&role, &id}, func() error {
		own[role] = append(own[role], id)
		return nil
	}); err != nil {
		return err
	}
	for _, role := range StructRoles {
		held := role == "employee" || len(own[role]) > 0
		for _, action := range actions {
			switch rights.Scope(role, action) {
			case "all":
				a.All[action] = a.All[action] || held
			case "own":
				for _, id := range own[role] {
					if a.Own[action] == nil {
						a.Own[action] = map[string]bool{}
					}
					a.Own[action][id] = true
				}
			}
		}
	}
	return nil
}

// StructCan: may the person do the action in the department.
func (s *Store) StructCan(ctx context.Context, userID string, admin bool, action, deptID string) (bool, error) {
	a, err := s.StructAccessOf(ctx, userID, admin)
	return a.Can(action, deptID), err
}
