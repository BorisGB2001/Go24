package company

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// «Кадровые изменения»: who was taken on, moved to other departments, dismissed, returned — by dates.

// StaffKinds are the kinds of changes.
var StaffKinds = []string{"hired", "moved", "fired", "returned"}

// StaffChange is one change of an employee.
type StaffChange struct {
	Person
	Kind        string
	At          time.Time
	Departments []string // after the change (taken on: the departments now)
	Was         []string // before it (moved)
	ActorID     string
	ActorName   string
}

// StaffFilter narrows the changes.
type StaffFilter struct {
	DepartmentID string // with its sub-departments (by the names the change kept)
	Kind         string
	From, To     time.Time // days, both included; zero — open
}

// deptNamesOf: the names of the person's departments, in order.
func deptNamesOf(ctx context.Context, q pgx.Tx, userID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT d.name FROM user_departments ud JOIN departments d ON d.id = ud.department_id
		WHERE ud.user_id::text = $1 ORDER BY d.name`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// logStaff records a change (the departments are the person's now).
func logStaff(ctx context.Context, tx pgx.Tx, userID, kind, actor string, was []string) error {
	now, err := deptNamesOf(ctx, tx, userID)
	if err != nil {
		return err
	}
	if was == nil {
		was = []string{}
	}
	var by any
	if actor != "" {
		by = actor
	}
	_, err = tx.Exec(ctx, `INSERT INTO staff_changes (user_id, kind, departments, was, actor_id) VALUES ($1, $2, $3, $4, $5)`,
		userID, kind, now, was, by)
	return err
}

// StaffChanges lists the changes, latest first (at most 1000). Those taken on before the log began are shown by their
// employment date (or the day they came to go24) and their departments now.
func (s *Store) StaffChanges(ctx context.Context, f StaffFilter) ([]StaffChange, error) {
	var where []string
	var args []any
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if slices.Contains(StaffKinds, f.Kind) {
		where = append(where, "c.kind = "+arg(f.Kind))
	}
	if !f.From.IsZero() {
		where = append(where, "c.at >= "+arg(f.From.Format(time.DateOnly))+"::date")
	}
	if !f.To.IsZero() {
		where = append(where, "c.at < "+arg(f.To.Format(time.DateOnly))+"::date + 1")
	}
	if f.DepartmentID != "" {
		n := arg(f.DepartmentID)
		where = append(where, `(c.departments || c.was) && ARRAY(WITH RECURSIVE sub AS (SELECT id, name FROM departments WHERE id::text = `+n+`
			UNION ALL SELECT d.id, d.name FROM departments d JOIN sub ON d.parent_id = sub.id) SELECT name FROM sub)`)
	}
	q := `WITH c AS (
			SELECT user_id, kind, at, departments, was, actor_id FROM staff_changes
			UNION ALL
			SELECT u.id, 'hired', coalesce(u.employment_date::timestamptz, u.created_at),
				ARRAY(SELECT d.name FROM user_departments ud JOIN departments d ON d.id = ud.department_id WHERE ud.user_id = u.id ORDER BY d.name),
				'{}', NULL
			FROM users u WHERE NOT u.guest AND u.oidc_sub <> 'legacy:0'
				AND NOT EXISTS (SELECT 1 FROM staff_changes h WHERE h.user_id = u.id AND h.kind = 'hired'))
		SELECT u.id::text, u.name, u.position, u.photo_key, c.kind, c.at, c.departments, c.was,
			coalesce(c.actor_id::text, ''), coalesce(a.name, '')
		FROM c JOIN users u ON u.id = c.user_id LEFT JOIN users a ON a.id = c.actor_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(ctx, q+" ORDER BY c.at DESC, u.name LIMIT 1000", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (StaffChange, error) {
		var c StaffChange
		err := row.Scan(&c.ID, &c.Name, &c.Position, &c.PhotoKey, &c.Kind, &c.At, &c.Departments, &c.Was, &c.ActorID, &c.ActorName)
		return c, err
	})
}
