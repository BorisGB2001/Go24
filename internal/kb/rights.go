package kb

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// Accesses of «Права доступа» of a space: read pages, also write them, also manage the space.
var Accesses = []string{"read", "edit", "full"}

// Right is a line of «Права доступа» of a space: to whom (Kind: all — every employee, user, department with its
// sub-departments) and the access.
type Right struct {
	ID           string
	Kind         string
	WhomID, Whom string
	Access       string
}

// managed is the space the user sees, if they may manage it.
func (s *Store) managed(ctx context.Context, userID, id string) (Space, error) {
	sp, err := s.Space(ctx, userID, id)
	if err == nil && !sp.CanManage {
		err = ErrForbidden
	}
	return sp, err
}

// UpdateSpace renames a space and changes its description (those who manage it).
func (s *Store) UpdateSpace(ctx context.Context, userID, id, name, description string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return i18n.E("err.kb_name")
	}
	if _, err := s.managed(ctx, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE kb_spaces SET name = $2, description = $3 WHERE id = $1`, id, name, strings.TrimSpace(description))
	return err
}

// Rights are «Права доступа» of the space: everyone first, then departments, then people.
func (s *Store) Rights(ctx context.Context, spaceID string) ([]Right, error) {
	rows, err := s.db.Query(ctx, `SELECT r.id::text,
			CASE WHEN r.user_id IS NOT NULL THEN 'user' WHEN r.department_id IS NOT NULL THEN 'department' ELSE 'all' END,
			coalesce(r.user_id, r.department_id)::text, coalesce(u.name, d.name, ''), r.access
		FROM kb_space_rights r LEFT JOIN users u ON u.id = r.user_id LEFT JOIN departments d ON d.id = r.department_id
		WHERE r.space_id = $1 ORDER BY r.user_id IS NOT NULL, r.department_id IS NOT NULL, r.created_at`, spaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Right])
}

// SetRight opens the space to everyone (kind all), an employee or a department with the access, or changes it. The
// first line closes the space to all the others; its creator, the project's managers and administrators keep it.
func (s *Store) SetRight(ctx context.Context, userID, spaceID, kind, whomID, access string) error {
	if !slices.Contains(Accesses, access) || (kind != "all" && kind != "user" && kind != "department") || (kind != "all" && whomID == "") {
		return ErrNotFound
	}
	if _, err := s.managed(ctx, userID, spaceID); err != nil {
		return err
	}
	var user, dept *string
	switch kind {
	case "user":
		user = &whomID
	case "department":
		dept = &whomID
	}
	_, err := s.db.Exec(ctx, `INSERT INTO kb_space_rights (space_id, user_id, department_id, access, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (space_id, coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET access = EXCLUDED.access`,
		spaceID, user, dept, access, userID)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && (pe.Code == "23503" || pe.Code == "22P02") { // no such employee or department
		return ErrNotFound
	}
	return err
}

// DeleteRight takes a line out of «Права доступа» of the space; with none left it has its own rules again.
func (s *Store) DeleteRight(ctx context.Context, userID, spaceID, rightID string) error {
	if _, err := s.managed(ctx, userID, spaceID); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM kb_space_rights WHERE id::text = $1 AND space_id = $2`, rightID, spaceID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClearRights takes all «Права доступа» of the space away: it has its own rules again.
func (s *Store) ClearRights(ctx context.Context, userID, spaceID string) error {
	if _, err := s.managed(ctx, userID, spaceID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM kb_space_rights WHERE space_id = $1`, spaceID)
	return err
}
