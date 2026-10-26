package tasks

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Preset is a saved filter of the task list: its name and the list's URL query.
type Preset struct {
	ID    string
	Name  string
	Query string
}

// Presets are the person's own presets by name.
func (s *Store) Presets(ctx context.Context, userID string) ([]Preset, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, query FROM task_filter_presets WHERE user_id = $1 ORDER BY lower(name)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Preset, error) {
		var p Preset
		err := r.Scan(&p.ID, &p.Name, &p.Query)
		return p, err
	})
}

// SavePreset keeps the filter under a name (the same name replaces it).
func (s *Store) SavePreset(ctx context.Context, userID, name, query string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || len(query) > 2000 {
		return invalid("err.preset")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO task_filter_presets (user_id, name, query) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, lower(name)) DO UPDATE SET query = EXCLUDED.query`, userID, name, query)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return invalid("err.preset")
	}
	return err
}

func (s *Store) DeletePreset(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM task_filter_presets WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
