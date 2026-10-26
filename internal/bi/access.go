package bi

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Accesses of «Права доступа» of a dashboard or a group: view it, also change it.
var Accesses = []string{"view", "edit"}

// Statuses of a dashboard: a draft is seen only by those who may change it.
var Statuses = []string{"draft", "published"}

// Viewer is who looks at the dashboards: an employee, an administrator.
type Viewer struct {
	ID    string
	Admin bool
}

// Group is a group of dashboards: its «Права доступа» are of all of them.
type Group struct {
	ID, Title, CreatedBy string
	Dashboards           int
}

// CanManage: the one who made the group and the administrators rename it, give it rights, delete it.
func (g Group) CanManage(v Viewer) bool { return v.Admin || g.CreatedBy == v.ID }

// Right is a line of «Права доступа»: to whom (Kind: all — every employee, user, department with its sub-departments)
// and the access.
type Right struct {
	ID           string
	Kind         string
	WhomID, Whom string
	Access       string
}

// On is what the lines of rights are of: a dashboard or a group.
type On struct{ Dashboard, Group string }

func (o On) ids() (dash, group *string) {
	if o.Dashboard != "" {
		return &o.Dashboard, nil
	}
	return nil, &o.Group
}

// Groups of dashboards by title, each with how many dashboards it has.
func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	rows, err := s.db.Query(ctx, `SELECT g.id::text, g.title, coalesce(g.created_by::text, ''),
			(SELECT count(*) FROM bi_dashboards d WHERE d.group_id = g.id)::int
		FROM bi_groups g ORDER BY lower(g.title), g.created_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Group])
}

func (s *Store) Group(ctx context.Context, id string) (Group, error) {
	var g Group
	err := s.db.QueryRow(ctx, `SELECT g.id::text, g.title, coalesce(g.created_by::text, ''),
			(SELECT count(*) FROM bi_dashboards d WHERE d.group_id = g.id)::int
		FROM bi_groups g WHERE g.id::text = $1`, id).Scan(&g.ID, &g.Title, &g.CreatedBy, &g.Dashboards)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return g, err
}

// SaveGroup adds a group (no id) by the user or renames one.
func (s *Store) SaveGroup(ctx context.Context, by, id, title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ErrTitle
	}
	if id == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO bi_groups (title, created_by) VALUES ($1, $2) RETURNING id`, title, by).Scan(&id)
		return id, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE bi_groups SET title = $2 WHERE id::text = $1`, id, title)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return id, err
}

// DeleteGroup deletes a group; its dashboards stay, out of any group, with their own rights.
func (s *Store) DeleteGroup(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM bi_groups WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Publish sets the status of a dashboard and its group ("" — none).
func (s *Store) Publish(ctx context.Context, id, status, group string) error {
	if !slices.Contains(Statuses, status) {
		return ErrNotFound
	}
	tag, err := s.db.Exec(ctx, `UPDATE bi_dashboards SET status = $2, group_id = nullif($3, '')::uuid, updated_at = now()
		WHERE id::text = $1 AND code IS NULL`, id, status, group)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "23503" || pe.Code == "22P02") { // no such group
		return ErrNotFound
	}
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Rights are «Права доступа» of a dashboard or a group: everyone first, then departments, then people.
func (s *Store) Rights(ctx context.Context, on On) ([]Right, error) {
	dash, group := on.ids()
	rows, err := s.db.Query(ctx, `SELECT r.id::text,
			CASE WHEN r.user_id IS NOT NULL THEN 'user' WHEN r.department_id IS NOT NULL THEN 'department' ELSE 'all' END,
			coalesce(r.user_id, r.department_id)::text, coalesce(u.name, d.name, ''), r.access
		FROM bi_rights r LEFT JOIN users u ON u.id = r.user_id LEFT JOIN departments d ON d.id = r.department_id
		WHERE r.dashboard_id = $1 OR r.group_id = $2 ORDER BY r.user_id IS NOT NULL, r.department_id IS NOT NULL, r.created_at`,
		dash, group)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Right])
}

// SetRight opens a dashboard or a group to everyone (kind all), an employee or a department with the access, or
// changes it. The first line of a dashboard or of its group closes the dashboard to all the others; its author and
// the administrators keep it.
func (s *Store) SetRight(ctx context.Context, by string, on On, kind, whomID, access string) error {
	if !slices.Contains(Accesses, access) || (kind != "all" && kind != "user" && kind != "department") || (kind != "all" && whomID == "") {
		return ErrNotFound
	}
	var user, dept *string
	switch kind {
	case "user":
		user = &whomID
	case "department":
		dept = &whomID
	}
	dash, group := on.ids()
	_, err := s.db.Exec(ctx, `INSERT INTO bi_rights (dashboard_id, group_id, user_id, department_id, access, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (coalesce(dashboard_id, group_id), coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid))
		DO UPDATE SET access = EXCLUDED.access`, dash, group, user, dept, access, by)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "23503" || pe.Code == "22P02") { // no such employee or department
		return ErrNotFound
	}
	return err
}

// DeleteRight takes a line out of «Права доступа»; with none left the dashboard is seen by every employee again.
func (s *Store) DeleteRight(ctx context.Context, on On, id string) error {
	dash, group := on.ids()
	tag, err := s.db.Exec(ctx, `DELETE FROM bi_rights WHERE id::text = $1 AND (dashboard_id = $2 OR group_id = $3)`, id, dash, group)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClearRights takes all «Права доступа» of a dashboard or a group away.
func (s *Store) ClearRights(ctx context.Context, on On) error {
	dash, group := on.ids()
	_, err := s.db.Exec(ctx, `DELETE FROM bi_rights WHERE dashboard_id = $1 OR group_id = $2`, dash, group)
	return err
}
