package telephony

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Whom not to ring, as the queues: the clients' calls (lines, queue groups, voice menus, calls back,
// «first the responsible one») pass by an employee who is «Не беспокоить», absent by the absence chart today or (when
// asked) has not started the working day, is on a break or has finished it. go24 tells Asterisk the inner numbers of
// such employees in the global GO24_OFF (",101,105,"), go24-ring takes them as offline: they do not ring and are not
// forwarded to a mobile. The colleagues' calls, picking up and listening ring them as before.

const settingSkip = "telephony_skip"

// Skip is which employees the clients' calls pass by.
type Skip struct {
	DND     bool `json:"dnd"`     // status «Не беспокоить»
	Absent  bool `json:"absent"`  // absent by the absence chart today
	Workday bool `json:"workday"` // the working day is not going (not started, a break, finished)
}

// Skipping is the company's rule; by default «Не беспокоить» and the absent ones are passed by.
func (s *Store) Skipping(ctx context.Context) (Skip, error) {
	sk := Skip{DND: true, Absent: true}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingSkip).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return sk, nil
	}
	if err != nil {
		return sk, err
	}
	return sk, json.Unmarshal(raw, &sk)
}

// SetSkipping stores the rule.
func (s *Store) SetSkipping(ctx context.Context, userID string, sk Skip) error {
	raw, err := json.Marshal(sk)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2::jsonb, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingSkip, string(raw), userID)
	return err
}

// Reasons why the clients' calls pass an employee by now.
const (
	SkipDND     = "dnd"
	SkipAbsent  = "absent"
	SkipWorkday = "workday"
)

// Unavailable are the employees with an inner number the clients' calls pass by now, with the reason (the first of
// «Не беспокоить», absent, the working day), by the employees' ids.
func (s *Store) Unavailable(ctx context.Context, now time.Time) (map[string]string, error) {
	sk, err := s.Skipping(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if !sk.DND && !sk.Absent && !sk.Workday {
		return out, nil
	}
	// the day of the absence chart and of the working day is the portal's one; a day started yesterday (a night
	// shift) and not finished still goes
	today := now.In(time.Local).Format(time.DateOnly)
	rows, err := s.db.Query(ctx, `SELECT u.id::text, CASE
			WHEN $2 AND u.presence = 'dnd' THEN 'dnd'
			WHEN $3 AND EXISTS (SELECT 1 FROM absences a WHERE a.user_id = u.id AND $1::date BETWEEN a.starts_on AND a.ends_on) THEN 'absent'
			WHEN $4 AND NOT EXISTS (SELECT 1 FROM workdays w WHERE w.user_id = u.id AND w.day >= $1::date - 1
				AND w.ended_at IS NULL AND w.paused_at IS NULL) THEN 'workday'
			ELSE '' END
		FROM users u WHERE u.active AND NOT u.guest AND u.inner_phone <> ''`, today, sk.DND, sk.Absent, sk.Workday)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, why string
		if err := rows.Scan(&id, &why); err != nil {
			return nil, err
		}
		if why != "" {
			out[id] = why
		}
	}
	return out, rows.Err()
}

// OffList is GO24_OFF: the inner numbers of the unavailable employees between commas (",101,105,"; "," — nobody).
func OffList(lines []Line, off map[string]string) string {
	var b strings.Builder
	b.WriteString(",")
	for _, l := range lines {
		if !l.Dup && off[l.UserID] != "" {
			b.WriteString(l.Number + ",")
		}
	}
	return b.String()
}

// SetGlobal sets a global variable of Asterisk's dialplan.
func (a *ARI) SetGlobal(ctx context.Context, name, value string) error {
	return a.do(ctx, "POST", "/asterisk/variable?"+url.Values{"variable": {name}, "value": {value}}.Encode(), nil)
}
