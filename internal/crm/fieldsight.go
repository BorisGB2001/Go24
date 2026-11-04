package crm

import (
	"context"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// «Видимость поля» of the own fields (GO24DEV-992): a field shown only to the chosen employees and
// departments (with the ones below) is left out of the card, the list, the kanban, the export and REST for the rest.
// Administrators see every field.

// FieldSight is what a person is in the fields' «Видимость»: their id and «dept:<id>» of their departments and the
// ones above. The zero value sees only the fields shown to everyone.
type FieldSight struct {
	all bool
	me  map[string]bool
}

// FieldSightOf is the person's sight; an administrator (or the admin mode of ctx) sees every field.
func (s *Store) FieldSightOf(ctx context.Context, userID string, admin bool) (FieldSight, error) {
	if admin || tasks.IsAdmin(ctx) {
		return FieldSight{all: true}, nil
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
			UNION SELECT id FROM departments WHERE head_id::text = $1
			UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL)
		SELECT 'dept:' || id::text FROM up`, userID)
	if err != nil {
		return FieldSight{}, err
	}
	depts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return FieldSight{}, err
	}
	v := FieldSight{me: map[string]bool{userID: true}}
	for _, d := range depts {
		v.me[d] = true
	}
	return v, nil
}

// Sees: is the field shown to the person?
func (v FieldSight) Sees(f tasks.Field) bool {
	if v.all || len(f.Visible) == 0 {
		return true
	}
	for _, w := range f.Visible {
		if v.me[w] {
			return true
		}
	}
	return false
}

// Fields are the fields of the list the person sees.
func (v FieldSight) Fields(list []tasks.Field) []tasks.Field {
	out := make([]tasks.Field, 0, len(list))
	for _, f := range list {
		if v.Sees(f) {
			out = append(out, f)
		}
	}
	return out
}

// Values are the fields with values the person sees.
func (v FieldSight) Values(list []tasks.FieldValue) []tasks.FieldValue {
	out := make([]tasks.FieldValue, 0, len(list))
	for _, f := range list {
		if v.Sees(f.Field) {
			out = append(out, f)
		}
	}
	return out
}
