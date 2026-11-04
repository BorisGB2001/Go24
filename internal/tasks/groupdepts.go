package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---------- Departments of a group (GO24DEV-833) ----------
//
// As: the owner and moderators add whole departments to a group or project; the people of a department and
// of its sub-departments (with the heads and deputies) become its members by themselves, those who come into the
// department later too, and those who left it (or were dismissed) go out — only those the departments brought, not
// those added by hand. A collab has no departments.

// GroupDepts are the group's departments as the people picker's choices «dept:ID».
func (s *Store) GroupDepts(ctx context.Context, projectID string) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT 'dept:' || d.id::text, d.name FROM project_departments pd
		JOIN departments d ON d.id = pd.department_id WHERE pd.project_id = $1 ORDER BY d.name, d.id`, projectID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		err := r.Scan(&u.ID, &u.Name)
		return u, err
	})
}

// SetGroupDepts replaces the group's departments (IDs, «dept:ID» too) and brings its members in line with them.
func (s *Store) SetGroupDepts(ctx context.Context, p Project, ids []string) error {
	if !p.CanManage() || p.Collab() || p.Archived {
		return ErrForbidden
	}
	depts := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimPrefix(strings.TrimSpace(id), "dept:"); id != "" {
			depts = append(depts, id)
		}
	}
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM project_departments WHERE project_id = $1 AND NOT department_id::text = ANY($2)`,
			p.ID, depts); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO project_departments (project_id, department_id)
			SELECT $1, unnest($2::text[])::uuid ON CONFLICT DO NOTHING`, p.ID, depts)
		return err
	})
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && (pe.Code == "23503" || pe.Code == "22P02") {
		return invalid("err.group_dept_unknown")
	}
	if err != nil {
		return err
	}
	_, err = s.SyncGroupDepts(ctx, p.ID)
	return err
}

// SyncGroupDepts brings the members of the group (projectID "" — of all groups) in line with their departments: the
// people of the departments come in as members, those the departments brought who are no longer there go out (a
// moderator among them stays, as one by hand). It says whether anybody came or went.
func (s *Store) SyncGroupDepts(ctx context.Context, projectID string) (bool, error) {
	changed := false
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var ps, us []string
		rows, err := tx.Query(ctx, `WITH RECURSIVE down AS (
				SELECT pd.project_id, pd.department_id AS id FROM project_departments pd JOIN projects p ON p.id = pd.project_id
				WHERE ($1 = '' OR pd.project_id::text = $1) AND p.kind <> 'collab'
				UNION SELECT down.project_id, d.id FROM departments d JOIN down ON d.parent_id = down.id)
			SELECT DISTINCT down.project_id::text, u.id::text FROM down JOIN users u ON u.active AND NOT u.guest AND (
				u.id IN (SELECT user_id FROM user_departments WHERE department_id = down.id)
				OR u.id IN (SELECT head_id FROM departments WHERE id = down.id)
				OR u.id IN (SELECT user_id FROM department_deputies WHERE department_id = down.id))`, projectID)
		if err != nil {
			return err
		}
		var pid, uid string
		if _, err := pgx.ForEachRow(rows, []any{&pid, &uid}, func() error {
			ps, us = append(ps, pid), append(us, uid)
			return nil
		}); err != nil {
			return err
		}
		const want = `unnest($1::uuid[], $2::uuid[]) AS w(p, u)`
		if _, err := tx.Exec(ctx, `INSERT INTO project_department_people (project_id, user_id) SELECT w.p, w.u FROM `+want+`
			WHERE NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = w.p AND m.user_id = w.u) ON CONFLICT DO NOTHING`,
			ps, us); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) SELECT w.p, w.u, 'member' FROM `+want+`
			ON CONFLICT DO NOTHING`, ps, us)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() > 0
		for _, q := range []string{ // the invitations and requests of those who came in are answered
			`DELETE FROM project_invites i USING ` + want + ` WHERE i.project_id = w.p AND i.user_id = w.u`,
			`DELETE FROM project_join_requests j USING ` + want + ` WHERE j.project_id = w.p AND j.user_id = w.u`,
		} {
			if _, err := tx.Exec(ctx, q, ps, us); err != nil {
				return err
			}
		}
		const gone = `($3 = '' OR b.project_id::text = $3)
			AND NOT EXISTS (SELECT 1 FROM ` + want + ` WHERE w.p = b.project_id AND w.u = b.user_id)`
		tag, err = tx.Exec(ctx, `DELETE FROM project_members m USING project_department_people b
			WHERE m.project_id = b.project_id AND m.user_id = b.user_id AND m.role IN ('member', 'viewer') AND `+gone, ps, us, projectID)
		if err != nil {
			return err
		}
		changed = changed || tag.RowsAffected() > 0
		_, err = tx.Exec(ctx, `DELETE FROM project_department_people b WHERE (`+gone+`)
			OR NOT EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = b.project_id AND m.user_id = b.user_id)`,
			ps, us, projectID)
		return err
	})
	return changed, err
}
