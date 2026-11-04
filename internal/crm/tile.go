package crm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Настроить вид карточки» of the portal kanban: which fields a tile of deals, leads or a smart process shows and in
// what order — the built-in ones and the custom ones ("f:" + the field's id); the title is always on top. The amount
// stays under the title, the nearest deed and the responsible at the tile's foot; the other fields go between them
// with their names. The view is the person's own; an administrator makes theirs the common one, which everybody sees
// until they change their own.

// TileFields are the fields a tile may show by its kind: deal, item (a smart process), lead.
var TileFields = map[string][]string{
	"deal": {"amount", "client", "activities", "responsible", "created", "close_date", "deal_type", "origin", "observers"},
	"item": {"amount", "client", "activities", "responsible", "created", "close_date", "origin", "observers"},
	"lead": {"amount", "contact_data", "activities", "responsible", "created", "name", "company_title", "phone", "email",
		"origin", "source_info", "observers"},
}

// DefaultTile is what a tile shows until anybody picks its fields.
var DefaultTile = []string{"amount", "activities", "responsible"}

const (
	prefTile    = "crm.tile:" // user_prefs, the person's view
	settingTile = "crm_tile:" // company_settings, the common view
)

// tileKnown tells a field a tile of the scope may show.
func tileKnown(scope, c string, fields []string) bool {
	return slices.Contains(TileFields[ColumnKind(scope)], c) || strings.HasPrefix(c, "f:") && slices.Contains(fields, c[2:])
}

// Tile is the view of the tiles the person sees: their own, else the common one, else the standard; own tells it is
// theirs, common that the company has a common one. A deleted field drops out.
func (s *Store) Tile(ctx context.Context, userID, scope string, fields []string) (cols []string, own, common bool, err error) {
	cols = DefaultTile
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingTile+scope).Scan(&raw)
	if err == nil {
		var all []string
		if common = json.Unmarshal(raw, &all) == nil; common {
			cols = all
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, err
	}
	raw = nil
	err = s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefTile+scope).Scan(&raw)
	if err == nil {
		var mine []string
		if own = json.Unmarshal(raw, &mine) == nil; own {
			cols = mine
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, false, err
	}
	out := []string{}
	for _, c := range cols {
		if tileKnown(scope, c, fields) {
			out = append(out, c)
		}
	}
	return out, own, common, nil
}

// SetTile saves the view of the tiles: the person's own, or (common) the company's one for everybody — then the
// person's own view gives way to it. The fields go in their order, once each.
func (s *Store) SetTile(ctx context.Context, userID, scope string, cols, fields []string, common bool) error {
	if TileFields[ColumnKind(scope)] == nil {
		return i18n.E("err.bad_form")
	}
	clean := []string{}
	for _, c := range cols {
		if !tileKnown(scope, c, fields) {
			return i18n.E("err.bad_form")
		}
		if !slices.Contains(clean, c) {
			clean = append(clean, c)
		}
	}
	raw, _ := json.Marshal(clean)
	if !common {
		_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefTile+scope, raw)
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingTile+scope, raw, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefTile+scope); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResetTile: the person's view back to the common one («Вернуть общий вид»), or (common) the common one back to the
// standard.
func (s *Store) ResetTile(ctx context.Context, userID, scope string, common bool) error {
	if common {
		_, err := s.db.Exec(ctx, `DELETE FROM company_settings WHERE key = $1`, settingTile+scope)
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefTile+scope)
	return err
}
