package crm

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// «Пресеты фильтра» of the deal and lead lists: the built-in ones every list has and the person's own,
// saved under a name; one own preset may be the default — the list opens filtered by it.

// Preset is a filter of a list under a name: Query is the filter as a URL query; Key names a built-in one.
type Preset struct {
	ID, Key, Name, Query string
	Default              bool
}

// BuiltInPresets are the portal ones: «В работе», «Мои», «Закрытые» («me» is whoever looks).
var BuiltInPresets = []Preset{{Key: "process", Query: "semantic=process"}, {Key: "mine", Query: "responsible=me"},
	{Key: "closed", Query: "semantic=closed"}}

// PresetScope is the list a preset belongs to: "lead", "deal" or the smart process "type:ID".
func PresetScope(entity, typeID string) string {
	if entity == "deal" && typeID != "" {
		return "type:" + typeID
	}
	return entity
}

// Presets are the person's own presets of the list, by name.
func (s *Store) Presets(ctx context.Context, userID, scope string) ([]Preset, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, query, is_default FROM crm_filter_presets WHERE user_id = $1 AND scope = $2
		ORDER BY lower(name)`, userID, scope)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Preset, error) {
		var p Preset
		err := r.Scan(&p.ID, &p.Name, &p.Query, &p.Default)
		return p, err
	})
}

// SavePreset keeps the filter under a name (the same name replaces it); def makes it the list's default.
func (s *Store) SavePreset(ctx context.Context, userID, scope, name, query string, def bool) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 60 || len(query) > 2000 {
		return i18n.E("err.preset")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if def {
		if _, err := tx.Exec(ctx, `UPDATE crm_filter_presets SET is_default = false WHERE user_id = $1 AND scope = $2`, userID, scope); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO crm_filter_presets (user_id, scope, name, query, is_default) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, scope, lower(name)) DO UPDATE SET query = EXCLUDED.query, is_default = EXCLUDED.is_default`,
		userID, scope, name, query, def)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23514" {
		return i18n.E("err.preset")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetDefaultPreset makes the person's preset the default of its list, or none when it is already (a toggle).
func (s *Store) SetDefaultPreset(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `UPDATE crm_filter_presets p SET is_default = (p.id::text = $2 AND NOT p.is_default)
		WHERE p.user_id = $1 AND p.scope = (SELECT scope FROM crm_filter_presets WHERE id::text = $2 AND user_id = $1)`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePreset removes the person's preset.
func (s *Store) DeletePreset(ctx context.Context, userID, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM crm_filter_presets WHERE id::text = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
