package tasks

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

// «Права доступа» of a task template: the author shares a base template with people and departments
// (with their sub-departments) — «Чтение»: they see it, make tasks by it and put it into a flow; «Полный доступ»:
// they also change it, its subtask templates and its rights, and delete it. Regular runs stay the author's.

// sharedTemplates are the base templates shared with the user: true — with «Полный доступ».
func (s *Store) sharedTemplates(ctx context.Context, userID string) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT a.template_id::text, bool_or(a.full_access) FROM task_template_access a
		WHERE a.user_id::text = $1 OR a.department_id IN (
			WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
				UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
			SELECT id FROM dep)
		GROUP BY a.template_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var full bool
		if err := rows.Scan(&id, &full); err != nil {
			return nil, err
		}
		out[id] = full
	}
	return out, rows.Err()
}

// baseTemplate is the base template a subtask template belongs to (the template itself when it is one).
func (s *Store) baseTemplate(ctx context.Context, id string) (string, error) {
	var base string
	err := s.db.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id, 0 AS depth FROM task_templates WHERE id = $1
			UNION ALL SELECT p.id, p.parent_id, up.depth + 1 FROM task_templates p JOIN up ON p.id = up.parent_id WHERE up.depth < 20)
		SELECT id::text FROM up WHERE parent_id IS NULL`, id).Scan(&base)
	return base, err
}

// templateToEdit loads a template the user may change: their own, an administrator's, or one shared with
// «Полный доступ».
func (s *Store) templateToEdit(ctx context.Context, userID, id string) (Template, error) {
	t, err := s.Template(ctx, userID, id)
	if err == nil && !t.CanEdit() {
		err = ErrForbidden
	}
	return t, err
}

// TemplateAccess is «Права доступа» of a base template: the people and the departments («dept:ID») with «Чтение»
// and with «Полный доступ».
func (s *Store) TemplateAccess(ctx context.Context, userID, id string) (read, full []User, err error) {
	if _, err := s.Template(ctx, userID, id); err != nil {
		return nil, nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT coalesce(a.user_id::text, 'dept:' || a.department_id::text), coalesce(u.name, d.name, ''), a.full_access
		FROM task_template_access a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN departments d ON d.id = a.department_id
		WHERE a.template_id = $1 ORDER BY a.department_id IS NULL, coalesce(d.name, u.name)`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u User
		var f bool
		if err := rows.Scan(&u.ID, &u.Name, &f); err != nil {
			return nil, nil, err
		}
		if f {
			full = append(full, u)
		} else {
			read = append(read, u)
		}
	}
	return read, full, rows.Err()
}

// SetTemplateAccess replaces «Права доступа» of a base template: people and departments («dept:ID»); one chosen in
// both lists gets «Полный доступ». The author needs none.
func (s *Store) SetTemplateAccess(ctx context.Context, userID, id string, read, full []string) error {
	t, err := s.templateToEdit(ctx, userID, id)
	if err != nil {
		return err
	}
	if t.ParentID != "" {
		return invalid("err.bad_form")
	}
	split := func(list []string) (users, depts []string) {
		for _, v := range list {
			if d, ok := strings.CutPrefix(v, "dept:"); ok {
				depts = append(depts, d)
			} else if v != "" && v != t.CreatorID {
				users = append(users, v)
			}
		}
		return
	}
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM task_template_access WHERE template_id = $1`, id); err != nil {
			return err
		}
		for _, l := range []struct {
			ids  []string
			full bool
		}{{full, true}, {read, false}} { // «Полный доступ» first: the same one in «Чтение» stays full
			users, depts := split(l.ids)
			if _, err := tx.Exec(ctx, `INSERT INTO task_template_access (template_id, user_id, full_access)
				SELECT $1, u.id, $3 FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, users, l.full); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_template_access (template_id, department_id, full_access)
				SELECT $1, d.id, $3 FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, id, depts, l.full); err != nil {
				return err
			}
		}
		return nil
	})
}
