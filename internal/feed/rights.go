package feed

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// The rights of the feed, as «Настройки ленты»: who may write to all employees and who may make a post
// important — everyone (nobody chosen) or the chosen employees and departments (with their sub-departments).
// Administrators may always; the others still write to their projects and to chosen addressees.
const keyRights = "feed_rights" // company_settings

type Rights struct {
	All       Audience `json:"all"`       // «Всем сотрудникам»
	Important Audience `json:"important"` // «Важное»
}

var (
	ErrNoPostAll   = i18n.E("err.feed_no_all")
	ErrNoImportant = i18n.E("err.feed_no_important")
)

// open: nobody chosen — everyone may.
func (a Audience) open() bool { return len(a.Users)+len(a.Depts) == 0 }

// GetRights reads the rights of the feed (none saved — everyone may everything).
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

// SaveRights keeps the rights of the feed (an administrator).
func (s *Store) SaveRights(ctx context.Context, userID string, r Rights) error {
	if !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, keyRights, raw, userID)
	return err
}

// May tells what the user may in the feed: write to all employees, make a post important.
func (s *Store) May(ctx context.Context, userID string) (all, important bool, err error) {
	r, err := s.GetRights(ctx)
	if err != nil {
		return false, false, err
	}
	if all, err = s.in(ctx, userID, r.All); err != nil {
		return false, false, err
	}
	important, err = s.in(ctx, userID, r.Important)
	return all, important, err
}

// in: the user is one of the chosen (by name or through a department they are in, or one above it).
func (s *Store) in(ctx context.Context, userID string, a Audience) (bool, error) {
	if a.open() || tasks.IsAdmin(ctx) {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT coalesce($1::text = ANY($2::text[]), false) OR EXISTS (
		WITH RECURSIVE up AS (
			SELECT department_id AS id FROM user_departments WHERE user_id = $1::uuid
			UNION SELECT d.parent_id FROM departments d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL)
		SELECT 1 FROM up WHERE id::text = ANY($3::text[]))`, userID, a.Users, a.Depts).Scan(&ok)
	return ok, err
}

// checkRights: a post of the user to all employees (no project, no addressees) or an important one is by the rights.
func (s *Store) checkRights(ctx context.Context, userID string, toAll, important bool) error {
	if !toAll && !important {
		return nil
	}
	all, imp, err := s.May(ctx, userID)
	if err != nil {
		return err
	}
	if toAll && !all {
		return ErrNoPostAll
	}
	if important && !imp {
		return ErrNoImportant
	}
	return nil
}

// Names: the chosen of the rights with their names (for the pickers of the settings).
func (s *Store) Names(ctx context.Context, a Audience) ([]Recipient, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, false FROM users WHERE id::text = ANY($1::text[])
		UNION ALL SELECT id::text, name, true FROM departments WHERE id::text = ANY($2::text[]) ORDER BY 3, 2`, a.Users, a.Depts)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recipient, error) {
		var r Recipient
		return r, row.Scan(&r.ID, &r.Name, &r.Dept)
	})
}
