package sites

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Role is what one may do with a site, as «Права доступа» of a site; each one includes the ones before it.
// Its title is site.role.<n>.
type Role int

const (
	RoleNone    Role = iota // the site is not there for them
	RoleView                // sees the site and its pages, previews them, copies the site
	RoleEdit                // changes the pages and their blocks, adds and deletes pages, rolls a page back
	RolePublish             // publishes the site and its pages and takes them off
	RoleFull                // the settings of the site, its rights, deleting it
)

// Roles are the roles one may be given, in order.
var Roles = []Role{RoleNone, RoleView, RoleEdit, RolePublish, RoleFull}

func (x Site) CanEdit() bool    { return x.Role >= RoleEdit }
func (x Site) CanPublish() bool { return x.Role >= RolePublish }
func (x Site) CanManage() bool  { return x.Role >= RoleFull }

// deptSQL: the departments of the user n (an SQL placeholder) with the departments above them — a right given to a
// department is its sub-departments' too.
func deptSQL(n string) string {
	return `(WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id::text = ` + n + `
		UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL) SELECT id FROM dep)`
}

// grants are the roles a user was given on sites (on all of them when ids is empty), personally or by a department.
func (s *Store) grants(ctx context.Context, userID string, ids ...string) (map[string]Role, error) {
	out := map[string]Role{}
	if userID == "" {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT site_id::text, max(role) FROM site_access
		WHERE (user_id::text = $1 OR department_id IN `+deptSQL("$1")+`) AND (coalesce(cardinality($2::text[]), 0) = 0 OR site_id::text = ANY($2))
		GROUP BY site_id`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var r Role
		if err := rows.Scan(&id, &r); err != nil {
			return nil, err
		}
		out[id] = r
	}
	return out, rows.Err()
}

// actor is the person the request is made by, else userID ("" — the system, which may do anything).
func actor(ctx context.Context, userID string) string {
	if v := tasks.Viewer(ctx); v != "" {
		return v
	}
	return userID
}

// roleOf is the role of a user on a site, given what they were granted: full for the system, administrators and the
// creator, else the highest of the grant and the site's «everyone».
func roleOf(ctx context.Context, userID string, x Site, granted map[string]Role) Role {
	if userID == "" || tasks.IsAdmin(ctx) || x.CreatedBy == userID {
		return RoleFull
	}
	return max(x.Everyone, granted[x.ID])
}

// need loads a site the actor (or userID, when the request names nobody) may see and checks they have the role.
func (s *Store) need(ctx context.Context, userID, siteID string, r Role) (Site, error) {
	x, err := s.Site(ctx, siteID)
	if err != nil {
		return x, err
	}
	if tasks.Viewer(ctx) == "" && userID != "" {
		g, err := s.grants(ctx, userID, x.ID)
		if err != nil {
			return x, err
		}
		x.Role = roleOf(ctx, userID, x, g)
	}
	if x.Role < r {
		return x, ErrForbidden
	}
	return x, nil
}

// Grant is a person or a department («dept:ID») with a role on a site.
type Grant struct {
	ID, Name string
	Role     Role
}

// Access is «Права доступа» of a site: the role of every employee and the people and departments given more.
func (s *Store) Access(ctx context.Context, siteID string) (Role, []Grant, error) {
	x, err := s.need(ctx, "", siteID, RoleFull)
	if err != nil {
		return 0, nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT coalesce(a.user_id::text, 'dept:' || a.department_id::text), coalesce(u.name, d.name, ''), a.role
		FROM site_access a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN departments d ON d.id = a.department_id
		WHERE a.site_id = $1 ORDER BY a.role DESC, a.department_id IS NULL, coalesce(d.name, u.name)`, x.ID)
	if err != nil {
		return 0, nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) {
		var g Grant
		return g, r.Scan(&g.ID, &g.Name, &g.Role)
	})
	return x.Everyone, list, err
}

// SetAccess replaces «Права доступа» of a site: the role of every employee and, for each role, its people and
// departments («dept:ID»); one chosen for several roles gets the highest. The creator needs none.
func (s *Store) SetAccess(ctx context.Context, userID, siteID string, everyone Role, given map[Role][]string) error {
	x, err := s.need(ctx, userID, siteID, RoleFull)
	if err != nil {
		return err
	}
	if everyone < RoleNone || everyone > RoleFull {
		return i18n.E("err.bad_form")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sites SET everyone = $2 WHERE id = $1`, x.ID, everyone); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM site_access WHERE site_id = $1`, x.ID); err != nil {
			return err
		}
		for r := RoleFull; r > RoleNone; r-- { // the highest first: the same one chosen for a lower role keeps it
			var users, depts []string
			for _, v := range given[r] {
				if d, ok := strings.CutPrefix(v, "dept:"); ok {
					depts = append(depts, d)
				} else if v != "" && v != x.CreatedBy {
					users = append(users, v)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO site_access (site_id, user_id, role)
				SELECT $1, u.id, $3 FROM users u WHERE u.id::text = ANY($2) AND NOT u.guest ON CONFLICT DO NOTHING`, x.ID, users, r); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO site_access (site_id, department_id, role)
				SELECT $1, d.id, $3 FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, x.ID, depts, r); err != nil {
				return err
			}
		}
		return nil
	})
}
