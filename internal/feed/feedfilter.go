package feed

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Periods are the dates of the feed's filter: "" — any; range — «с» and «по».
var Periods = []string{"today", "yesterday", "week", "month", "quarter", "range"}

// Period is the time of a period of the filter for the moment now: from — its start, till — its end (exclusive);
// from and to (YYYY-MM-DD, either may be empty) are the days of «range». Zero times — no bound.
func Period(name, from, to string, now time.Time) (time.Time, time.Time) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch name {
	case "today":
		return day, day.AddDate(0, 0, 1)
	case "yesterday":
		return day.AddDate(0, 0, -1), day
	case "week": // from Monday
		start := day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return start, start.AddDate(0, 0, 7)
	case "month":
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 1, 0)
	case "quarter":
		start := time.Date(now.Year(), (now.Month()-1)/3*3+1, 1, 0, 0, 0, 0, now.Location())
		return start, start.AddDate(0, 3, 0)
	case "range", "":
		var a, b time.Time
		if t, err := time.ParseInLocation("2006-01-02", from, now.Location()); err == nil {
			a = t
		}
		if t, err := time.ParseInLocation("2006-01-02", to, now.Location()); err == nil {
			b = t.AddDate(0, 0, 1)
		}
		return a, b
	}
	return time.Time{}, time.Time{}
}

// SavedFilter is a filter of the feed the person saved under a name («Сохранить фильтр»): Query is the
// query string of /feed.
type SavedFilter struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

const (
	prefFilters = "feed.filters" // user_prefs
	maxFilters  = 20
)

// ErrFilterName: the saved filter has no name.
var ErrFilterName = errors.New("feed: filter name")

// SavedFilters are the person's saved filters, in the order they were saved.
func (s *Store) SavedFilters(ctx context.Context, userID string) ([]SavedFilter, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefFilters).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []SavedFilter
	return list, json.Unmarshal(raw, &list)
}

// SaveFilter saves a filter under its name; a filter of the same name is replaced (in its place).
func (s *Store) SaveFilter(ctx context.Context, userID string, f SavedFilter) error {
	f.Name = strings.TrimSpace(f.Name)
	if f.Name == "" || len([]rune(f.Name)) > 100 {
		return ErrFilterName
	}
	list, err := s.SavedFilters(ctx, userID)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(list, func(x SavedFilter) bool { return x.Name == f.Name }); i >= 0 {
		list[i] = f
	} else {
		list = append(list, f)
	}
	if len(list) > maxFilters {
		list = list[len(list)-maxFilters:]
	}
	return s.putFilters(ctx, userID, list)
}

// DeleteFilter forgets the saved filter of that name.
func (s *Store) DeleteFilter(ctx context.Context, userID, name string) error {
	list, err := s.SavedFilters(ctx, userID)
	if err != nil {
		return err
	}
	return s.putFilters(ctx, userID, slices.DeleteFunc(list, func(x SavedFilter) bool { return x.Name == name }))
}

func (s *Store) putFilters(ctx context.Context, userID string, list []SavedFilter) error {
	if len(list) == 0 {
		_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefFilters)
		return err
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefFilters, raw)
	return err
}

// DeptNames are the names of the departments of the refs «dept:ID», by ref (for the picker's chips).
func (s *Store) DeptNames(ctx context.Context, refs []string) (map[string]string, error) {
	out := map[string]string{}
	var ids []string
	for _, ref := range refs {
		if id, ok := strings.CutPrefix(ref, "dept:"); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, name FROM departments WHERE id::text = ANY($1::text[])`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out["dept:"+id] = name
	}
	return out, rows.Err()
}
