package telephony

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

// «Перехват звонка», as in an office PBX: an employee takes a call ringing for a colleague. *8 — one
// ringing for someone of their departments or queue groups (the named call and pickup groups of their accounts), **101
// — the one ringing for the employee 101; «Перехватить» in «Текущие звонки» rings the employee's phones first
// (go24-pickup of the dialplan). The call log has the one who took the call as its answerer (go24-picked tells it the
// call's channel, go24-cdr reads it). The company may turn it off (on unless turned off).

const settingPickup = "telephony_pickup"

// Picking tells whether the employees may take their colleagues' calls.
func (s *Store) Picking(ctx context.Context) (bool, error) {
	var on bool
	err := s.db.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, settingPickup).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return on, err
}

// SetPicking turns taking the colleagues' calls on or off.
func (s *Store) SetPicking(ctx context.Context, userID string, on bool) error {
	_, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingPickup, on, userID)
	return err
}

// PickupGroups are the groups whose calls each employee may take with *8, by the employees' ids: their departments
// (with the ones they head or are deputies of) and their queue groups.
func (s *Store) PickupGroups(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text, 'd' || department_id::text FROM user_departments
		UNION SELECT head_id::text, 'd' || id::text FROM departments WHERE head_id IS NOT NULL
		UNION SELECT user_id::text, 'd' || department_id::text FROM department_deputies
		ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var u, g string
		if err := rows.Scan(&u, &g); err != nil {
			return nil, err
		}
		out[u] = append(out[u], strings.ReplaceAll(g, "-", ""))
	}
	return out, rows.Err()
}

// pickupGroups adds the queue groups to the departments' pickup groups.
func pickupGroups(deps map[string][]string, groups []Group) map[string][]string {
	out := make(map[string][]string, len(deps))
	for u, gs := range deps {
		out[u] = append([]string(nil), gs...)
	}
	for _, g := range groups {
		for _, u := range g.Users {
			out[u] = append(out[u], GroupKey(g.ID))
		}
	}
	return out
}

// Pick rings the employee's phones (their inner number); the one who answers takes the call ringing for the
// employee whose inner number is whose.
func (a *ARI) Pick(ctx context.Context, inner, whose string) error {
	v := url.Values{"endpoint": {"Local/" + inner + "@go24-call/n"}, "extension": {whose}, "context": {"go24-pickup"},
		"priority": {"1"}, "callerId": {inner}, "timeout": {"30"}}
	body := map[string]any{"variables": map[string]string{"GO24_PICKER": inner, "GO24_PICKOK": "1"}}
	return a.send(ctx, http.MethodPost, "/channels?"+v.Encode(), body, nil)
}
