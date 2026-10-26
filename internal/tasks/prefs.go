package tasks

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ListColumns are the optional columns of task lists, in display order (the title is always shown).
var ListColumns = []string{"id", "status", "activity", "deadline", "creator", "responsible", "project", "priority", "created", "closed", "plan"}

// DefaultColumns is what a list shows until the user picks columns with the gear.
var DefaultColumns = []string{"id", "status", "activity", "deadline", "creator", "responsible", "project"}

const prefColumns = "list.columns"

// Columns returns the user's task list columns.
func (s *Store) Columns(ctx context.Context, userID string) (map[string]bool, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefColumns).Scan(&raw)
	cols := DefaultColumns
	if err == nil {
		var saved []string
		if json.Unmarshal(raw, &saved) == nil {
			cols = saved
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := map[string]bool{}
	for _, c := range cols {
		out[c] = true
	}
	return out, nil
}

// SetColumns saves the user's choice; unknown names are an error.
func (s *Store) SetColumns(ctx context.Context, userID string, cols []string) error {
	known := map[string]bool{}
	for _, c := range ListColumns {
		known[c] = true
	}
	clean := []string{}
	seen := map[string]bool{}
	for _, c := range cols {
		if !known[c] {
			return invalid("err.bad_form")
		}
		if !seen[c] {
			seen[c] = true
			clean = append(clean, c)
		}
	}
	raw, _ := json.Marshal(clean)
	_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefColumns, raw)
	return err
}

// Widths of list columns and page panes the user dragged (static/js/widths.js) grids: one
// user_prefs row per list, "widths:NAME" → {column: px}.
const prefWidths = "widths:"

// Widths returns every list's saved widths by its name.
func (s *Store) Widths(ctx context.Context, userID string) (map[string]map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT substr(key, $2), value FROM user_prefs WHERE user_id = $1 AND starts_with(key, $3)`,
		userID, len(prefWidths)+1, prefWidths)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]int{}
	for rows.Next() {
		var name string
		var raw []byte
		if err := rows.Scan(&name, &raw); err != nil {
			return nil, err
		}
		var w map[string]int
		if json.Unmarshal(raw, &w) == nil {
			out[name] = w
		}
	}
	return out, rows.Err()
}

// SetWidths keeps a list's widths; none — the list goes back to its usual look.
func (s *Store) SetWidths(ctx context.Context, userID, name string, widths map[string]int) error {
	if name == "" || len(name) > 200 || len(widths) > 60 {
		return invalid("err.bad_form")
	}
	for col, px := range widths {
		if col == "" || len(col) > 60 || px < 20 || px > 4000 {
			return invalid("err.bad_form")
		}
	}
	if len(widths) == 0 {
		_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefWidths+name)
		return err
	}
	raw, _ := json.Marshal(widths)
	_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefWidths+name, raw)
	return err
}
