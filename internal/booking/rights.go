package booking

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The rights of «Онлайн-запись», as its «Права доступа»: who works with it (sees the section, books
// clients) — everyone (nobody chosen) or the chosen employees and departments (with their sub-departments); who
// manages it besides administrators — the resources, their types and the links for booking. A manager works with it
// too. Administrators may everything.
const keyRights = "booking_rights" // company_settings

// Audience: the chosen employees and departments (a department — with its sub-departments).
type Audience struct {
	Users []string `json:"users,omitempty"`
	Depts []string `json:"depts,omitempty"`
}

func (a Audience) empty() bool { return len(a.Users)+len(a.Depts) == 0 }

type Rights struct {
	Use    Audience `json:"use"`    // nobody chosen — everyone
	Manage Audience `json:"manage"` // nobody chosen — administrators only
}

// GetRights reads the rights (none saved — everyone works with it, administrators manage).
func (s *Store) GetRights(ctx context.Context) (Rights, error) {
	var r Rights
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, keyRights).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	return r, json.Unmarshal(raw, &r)
}

// SaveRights keeps the rights; the caller checks the user is an administrator.
func (s *Store) SaveRights(ctx context.Context, userID string, r Rights) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, keyRights, raw, userID)
	return err
}

// May tells what the user (admin — an administrator) may: work with «Онлайн-запись», manage it.
func (s *Store) May(ctx context.Context, userID string, admin bool) (use, manage bool, err error) {
	if admin {
		return true, true, nil
	}
	r, err := s.GetRights(ctx)
	if err != nil {
		return false, false, err
	}
	if !r.Manage.empty() {
		if manage, err = s.in(ctx, userID, r.Manage); err != nil {
			return false, false, err
		}
	}
	if manage || r.Use.empty() {
		return true, manage, nil
	}
	use, err = s.in(ctx, userID, r.Use)
	return use, false, err
}

// in: the user is one of the chosen (by name or through a department they are in, or one above it).
func (s *Store) in(ctx context.Context, userID string, a Audience) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT coalesce($1::text = ANY($2::text[]), false) OR EXISTS (
		WITH RECURSIVE up AS (
			SELECT department_id AS id FROM user_departments WHERE user_id = $1::uuid
			UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL)
		SELECT 1 FROM up WHERE id::text = ANY($3::text[]))`, userID, a.Users, a.Depts).Scan(&ok)
	return ok, err
}

// Chosen: one of the chosen with their name (for the pickers of «Права доступа»).
type Chosen struct {
	ID, Name string
	Dept     bool
}

// Names: the chosen of a, departments after the employees.
func (s *Store) Names(ctx context.Context, a Audience) ([]Chosen, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, false FROM users WHERE id::text = ANY($1::text[])
		UNION ALL SELECT id::text, name, true FROM departments WHERE id::text = ANY($2::text[]) ORDER BY 3, 2`, a.Users, a.Depts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Chosen, error) {
		var c Chosen
		return c, row.Scan(&c.ID, &c.Name, &c.Dept)
	})
}
