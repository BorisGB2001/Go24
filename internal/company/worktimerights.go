package company

import (
	"context"
	"maps"
	"slices"

	"github.com/jackc/pgx/v5"
)

// «Права доступа» of working time: an administrator sets what the heads of departments, their
// deputies and every employee may do — see the working time of others (days, the time sheet, the team today,
// work reports, time control), change it (days at once, approvals of changes, report marks, shifts) in their own
// departments (with the sub-departments) or everywhere, and change the settings and the work schedules.
// Administrators may do everything; one's own working time is always one's own.

// WorktimeActions are the actions of working time; «settings» has no «own» scope.
var WorktimeActions = []string{"view", "edit", "settings"}

// WorktimeScopes are the scopes of the action.
func WorktimeScopes(action string) []string {
	if action == "settings" {
		return []string{"", "all"}
	}
	return StructScopes
}

// WorktimeRights reads the rights.
func (s *Store) WorktimeRights(ctx context.Context) (StructRights, error) {
	return s.readRights(ctx, "worktime_rights", WorktimeActions)
}

// SetWorktimeRights replaces the rights (an administrator; checked by the caller): unknown roles, actions and scopes
// are dropped.
func (s *Store) SetWorktimeRights(ctx context.Context, in StructRights) error {
	return s.writeRights(ctx, "worktime_rights", WorktimeActions, WorktimeScopes, in)
}

// WorktimeAccessOf works out the person's access to working time; who may change it, sees it too.
func (s *Store) WorktimeAccessOf(ctx context.Context, userID string, admin bool) (StructAccess, error) {
	if admin {
		return fullAccess(WorktimeActions), nil
	}
	var guest bool
	err := s.db.QueryRow(ctx, `SELECT guest OR NOT active FROM users WHERE id::text = $1`, userID).Scan(&guest)
	if err == pgx.ErrNoRows || guest {
		return StructAccess{none: true}, nil
	}
	a := StructAccess{All: map[string]bool{}, Own: map[string]map[string]bool{}}
	if err != nil {
		return a, err
	}
	rights, err := s.WorktimeRights(ctx)
	if err != nil {
		return a, err
	}
	if err := s.roleAccess(ctx, userID, rights, WorktimeActions, &a); err != nil {
		return a, err
	}
	a.All["view"] = a.All["view"] || a.All["edit"]
	if len(a.Own["edit"]) > 0 {
		if a.Own["view"] == nil {
			a.Own["view"] = map[string]bool{}
		}
		maps.Copy(a.Own["view"], a.Own["edit"])
	}
	return a, nil
}

// WorktimeSettings: may the person change the settings of working time and the work schedules.
func (s *Store) WorktimeSettings(ctx context.Context, userID string, admin bool) (bool, error) {
	a, err := s.WorktimeAccessOf(ctx, userID, admin)
	return !a.none && a.All["settings"], err
}

// inDepts: the condition «the person u works in, heads or is a deputy of one of the departments $2».
const inDepts = `(EXISTS (SELECT 1 FROM user_departments ud WHERE ud.user_id = u.id AND ud.department_id::text = ANY($2))
	OR EXISTS (SELECT 1 FROM departments d WHERE d.head_id = u.id AND d.id::text = ANY($2))
	OR EXISTS (SELECT 1 FROM department_deputies dd WHERE dd.user_id = u.id AND dd.department_id::text = ANY($2)))`

// WorktimeCan: may the actor see («view») or change («edit») the working time of another person.
func (s *Store) WorktimeCan(ctx context.Context, actor, userID string, admin bool, action string) (bool, error) {
	if admin {
		return true, nil
	}
	if actor == userID {
		return false, nil
	}
	a, err := s.WorktimeAccessOf(ctx, actor, false)
	if err != nil || a.none {
		return false, err
	}
	if a.All[action] {
		return true, nil
	}
	if len(a.Own[action]) == 0 {
		return false, nil
	}
	var ok bool
	err = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id::text = $1 AND `+inDepts+`)`,
		userID, slices.Collect(maps.Keys(a.Own[action]))).Scan(&ok)
	return ok, err
}

// WorktimePeople are the active employees whose working time the person may see («view») or change («edit»),
// themself not counted.
func (s *Store) WorktimePeople(ctx context.Context, userID string, admin bool, action string) ([]Person, error) {
	a, err := s.WorktimeAccessOf(ctx, userID, admin)
	if err != nil || a.none || !a.All[action] && len(a.Own[action]) == 0 {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.position, u.photo_key FROM users u
		WHERE u.id::text <> $1 AND u.active AND NOT u.guest AND ($3 OR `+inDepts+`) ORDER BY u.name`,
		userID, slices.Collect(maps.Keys(a.Own[action])), a.All[action])
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}
