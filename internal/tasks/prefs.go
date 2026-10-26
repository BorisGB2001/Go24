package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

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

// SortColumns are the list columns a click on the header sorts by grids. A sort is the column name,
// "-name" — descending; "" is the usual order of the list.
var SortColumns = []string{"id", "title", "status", "activity", "deadline", "creator", "responsible", "project", "priority", "created", "closed", "plan"}

// sortDescFirst are the columns whose first click puts the newest on top.
var sortDescFirst = map[string]bool{"activity": true, "created": true, "closed": true, "priority": true}

const prefSort = "list.sort"

// NextSort is the sort a click on the column's header gives after cur: the column in its first direction, then the
// other way round.
func NextSort(cur, col string) string {
	switch {
	case cur == col:
		return "-" + col
	case cur == "-"+col:
		return col
	case sortDescFirst[col]:
		return "-" + col
	}
	return col
}

// Sort returns the user's sort of task lists ("" — the usual order).
func (s *Store) Sort(ctx context.Context, userID string) (string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefSort).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var v string
	if json.Unmarshal(raw, &v) != nil || !validSort(v) {
		return "", nil
	}
	return v, nil
}

// SetSort keeps the user's sort; "" — back to the usual order.
func (s *Store) SetSort(ctx context.Context, userID, v string) error {
	if !validSort(v) {
		return invalid("err.bad_form")
	}
	if v == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefSort)
		return err
	}
	raw, _ := json.Marshal(v)
	_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefSort, raw)
	return err
}

func validSort(v string) bool {
	return v == "" || slices.Contains(SortColumns, strings.TrimPrefix(v, "-"))
}

// statusOrder is the way of a task: the status column sorts by it.
var statusOrder = map[Status]int{StatusPending: 0, StatusInProgress: 1, StatusAwaitingControl: 2, StatusDeferred: 3, StatusCompleted: 4}

// SortTasks orders a list by a sort of SortColumns (in place, stable). Pinned tasks stay on top, and tasks without
// the date sorted by go to the end either way.
func SortTasks(list []Task, by string) {
	col, desc := strings.TrimPrefix(by, "-"), strings.HasPrefix(by, "-")
	if !validSort(by) || by == "" {
		return
	}
	dates := func(a, b *time.Time) (int, bool) { // the compare, and whether one of them is missing
		switch {
		case a == nil && b == nil:
			return 0, true
		case a == nil:
			return 1, true
		case b == nil:
			return -1, true
		}
		return a.Compare(*b), false
	}
	text := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	slices.SortStableFunc(list, func(a, b Task) int {
		if a.Pinned != b.Pinned {
			if a.Pinned {
				return -1
			}
			return 1
		}
		var c int
		switch col {
		case "id":
			if c = text(a.ProjectKey, b.ProjectKey); c == 0 {
				c = a.Number - b.Number
			}
		case "title":
			c = text(a.Title, b.Title)
		case "status":
			c = statusOrder[a.Status] - statusOrder[b.Status]
		case "activity":
			c = a.UpdatedAt.Compare(b.UpdatedAt)
		case "creator":
			c = text(a.CreatorName, b.CreatorName)
		case "responsible":
			c = text(a.ResponsibleName, b.ResponsibleName)
		case "project":
			c = text(a.ProjectKey, b.ProjectKey)
		case "priority":
			c = int(a.Priority) - int(b.Priority)
		case "created":
			c = a.CreatedAt.Compare(b.CreatedAt)
		default: // the dates that may be missing
			pick := map[string]func(Task) *time.Time{
				"deadline": func(t Task) *time.Time { return t.Deadline },
				"closed":   func(t Task) *time.Time { return t.ClosedAt },
				"plan":     func(t Task) *time.Time { return t.StartPlan },
			}[col]
			var missing bool
			if c, missing = dates(pick(a), pick(b)); missing {
				return c
			}
		}
		if desc {
			return -c
		}
		return c
	})
}
