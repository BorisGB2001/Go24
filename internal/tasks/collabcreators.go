package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
)

// The company's settings of collabs «Настройки → Коллабы»: an administrator turns collabs on or off and
// chooses who makes new ones — every employee, the chosen employees and departments (with their sub-departments),
// or administrators only. Guests never make collabs. The collabs there are stay as they are when the rule changes.

const settingCollabs = "collabs" // company_settings

// CollabCreators is who makes collabs.
type CollabCreators struct {
	Off   bool     `json:"off,omitempty"` // collabs are turned off: nobody makes new ones
	Who   string   `json:"who"`           // all | some | admins
	Users []string `json:"users,omitempty"`
	Depts []string `json:"depts,omitempty"`
}

var collabCreatorsWho = []string{"all", "some", "admins"}

type administratorKey struct{}

// AsAdministrator marks the request as made by an administrator of the company (admin mode or not): the rule
// «administrators only» lets them make collabs.
func AsAdministrator(ctx context.Context) context.Context {
	return context.WithValue(ctx, administratorKey{}, true)
}

func administrator(ctx context.Context) bool {
	v, _ := ctx.Value(administratorKey{}).(bool)
	return v || IsAdmin(ctx)
}

// CollabCreators is the company's rule; by default every employee makes collabs.
func (s *Store) CollabCreators(ctx context.Context) (CollabCreators, error) {
	return collabCreators(ctx, s.db)
}

func collabCreators(ctx context.Context, q querier) (CollabCreators, error) {
	c := CollabCreators{Who: "all"}
	var raw []byte
	err := q.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingCollabs).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return CollabCreators{Who: "all"}, nil
	}
	if !slices.Contains(collabCreatorsWho, c.Who) {
		c.Who = "all"
	}
	return c, nil
}

// SetCollabCreators saves the rule (the caller checks the actor is an administrator); the chosen people and
// departments are kept only for «some».
func (s *Store) SetCollabCreators(ctx context.Context, actorID string, c CollabCreators) error {
	if !slices.Contains(collabCreatorsWho, c.Who) {
		return invalid("err.collab_creators_who")
	}
	if c.Who != "some" {
		c.Users, c.Depts = nil, nil
	} else if len(c.Users)+len(c.Depts) == 0 {
		return invalid("err.collab_creators_empty")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingCollabs, raw, actorID)
	return err
}

// MayCreateCollab reports whether the person makes new collabs by the company's rule (ctx says an administrator,
// see AsAdministrator).
func (s *Store) MayCreateCollab(ctx context.Context, userID string) (bool, error) {
	return mayCreateCollab(ctx, s.db, userID)
}

func mayCreateCollab(ctx context.Context, q querier, userID string) (bool, error) {
	var guest bool
	if err := q.QueryRow(ctx, `SELECT guest FROM users WHERE id = $1`, userID).Scan(&guest); err != nil {
		return false, err
	}
	if guest { // guests work in collabs they were invited to, they do not make new ones
		return false, nil
	}
	c, err := collabCreators(ctx, q)
	if err != nil || c.Off {
		return false, err
	}
	switch c.Who {
	case "admins":
		return administrator(ctx), nil
	case "some":
		if administrator(ctx) || slices.Contains(c.Users, userID) {
			return true, nil
		}
		var yes bool
		err := q.QueryRow(ctx, `WITH RECURSIVE dep AS (
				SELECT department_id AS id FROM user_departments WHERE user_id = $1
				UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
			SELECT EXISTS (SELECT 1 FROM dep WHERE id::text = ANY($2::text[]))`, userID, c.Depts).Scan(&yes)
		return yes, err
	}
	return true, nil
}

// DeptChoices are the departments as choices of the people picker («dept:ID» with the name), in the given order;
// a removed department is left out.
func (s *Store) DeptChoices(ctx context.Context, ids []string) ([]User, error) {
	rows, err := s.db.Query(ctx, `SELECT 'dept:' || d.id::text, d.name FROM unnest($1::text[]) WITH ORDINALITY AS i(id, n)
		JOIN departments d ON d.id::text = i.id ORDER BY i.n`, ids)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		return u, r.Scan(&u.ID, &u.Name)
	})
}
