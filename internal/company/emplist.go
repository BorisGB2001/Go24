package company

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The columns and the order of the «Сотрудники» grid, as «Настроить колонки» and the sortable headers of the list.

// ListColumns are the columns one may show beside the name, in the grid's order.
var ListColumns = []string{"position", "department", "email", "phone", "inner_phone", "city", "birthday", "employment",
	"last_login", "teams", "interests"}

// DefaultColumns are the columns of one who has chosen none.
var DefaultColumns = []string{"position", "department", "email", "phone", "inner_phone"}

// ListSorts are the columns the list is sorted by ("" — by name).
var ListSorts = []string{"name", "position", "department", "birthday", "employment", "last_login"}

const prefColumns = "company.columns" // user_prefs

// Columns are the employee's columns of the list: the known ones of what they saved, in the grid's order.
func (s *Store) Columns(ctx context.Context, userID string) ([]string, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefColumns).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultColumns, nil
	}
	if err != nil {
		return nil, err
	}
	var cols []string
	if err := json.Unmarshal(raw, &cols); err != nil {
		return DefaultColumns, nil
	}
	return knownColumns(cols), nil
}

// SetColumns keeps the employee's columns; none chosen — back to the default ones.
func (s *Store) SetColumns(ctx context.Context, userID string, cols []string) error {
	cols = knownColumns(cols)
	if len(cols) == 0 {
		_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefColumns)
		return err
	}
	raw, err := json.Marshal(cols)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefColumns, raw)
	return err
}

func knownColumns(cols []string) []string {
	out := []string{}
	for _, c := range ListColumns {
		if slices.Contains(cols, c) {
			out = append(out, c)
		}
	}
	return out
}

// SortEmployees orders the list by a column of ListSorts, the name breaking ties; empty values go last either way.
func SortEmployees(list []Profile, by string, desc bool) {
	name := func(a, b Profile) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }
	key := func(p Profile) (string, bool) {
		switch by {
		case "position":
			return strings.ToLower(p.Position), p.Position != ""
		case "department":
			if len(p.Departments) == 0 {
				return "", false
			}
			return strings.ToLower(p.Departments[0].Name), true
		case "birthday": // by the day of the year, as the birthdays are
			if p.Birthday == nil {
				return "", false
			}
			return p.Birthday.Format("01-02"), true
		case "employment":
			if p.Employment == nil {
				return "", false
			}
			return p.Employment.Format(time.DateOnly), true
		case "last_login":
			if p.LastLogin.IsZero() {
				return "", false
			}
			return p.LastLogin.UTC().Format(time.RFC3339Nano), true
		}
		return strings.ToLower(p.Name), true
	}
	slices.SortStableFunc(list, func(a, b Profile) int {
		ka, oka := key(a)
		kb, okb := key(b)
		if oka != okb {
			if oka {
				return -1
			}
			return 1
		}
		c := cmp.Compare(ka, kb)
		if desc {
			c = -c
		}
		if c == 0 {
			return name(a, b)
		}
		return c
	})
}
