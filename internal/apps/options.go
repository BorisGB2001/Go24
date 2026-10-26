package apps

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// MaxOptions is how many settings of its own (or of one employee) an application keeps.
const MaxOptions = 200

var ErrOptionName = i18n.E("err.app_option")

// Options of the application, as app.option.get (userID "") and user.option.get (the employee's).
func (s *Store) Options(ctx context.Context, appID, userID string) (map[string]json.RawMessage, error) {
	rows, err := s.db.Query(ctx, `SELECT name, value FROM app_options
		WHERE app_id::text = $1 AND user_id IS NOT DISTINCT FROM nullif($2, '')::uuid ORDER BY name`, appID, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	var (
		name  string
		value []byte
	)
	_, err = pgx.ForEachRow(rows, []any{&name, &value}, func() error {
		out[name] = json.RawMessage(value)
		return nil
	})
	return out, err
}

// SetOptions sets the settings of the application (userID "") or of the employee, as app.option.set and
// user.option.set: the named ones only, a null value removes the setting.
func (s *Store) SetOptions(ctx context.Context, appID, userID string, set map[string]json.RawMessage) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for name, v := range set {
		if name == "" || len(name) > 128 {
			return ErrOptionName
		}
		if v == nil || string(v) == "null" {
			_, err = tx.Exec(ctx, `DELETE FROM app_options WHERE app_id::text = $1 AND user_id IS NOT DISTINCT FROM nullif($2, '')::uuid
				AND name = $3`, appID, userID, name)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO app_options (app_id, user_id, name, value) VALUES ($1::uuid, nullif($2, '')::uuid, $3, $4)
				ON CONFLICT (app_id, coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid), name) DO UPDATE SET value = EXCLUDED.value`,
				appID, userID, name, []byte(v))
		}
		if err != nil {
			return err
		}
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app_options WHERE app_id::text = $1 AND user_id IS NOT DISTINCT FROM nullif($2, '')::uuid`,
		appID, userID).Scan(&n); err != nil {
		return err
	}
	if n > MaxOptions {
		return i18n.E("err.app_options_many", MaxOptions)
	}
	return tx.Commit(ctx)
}
