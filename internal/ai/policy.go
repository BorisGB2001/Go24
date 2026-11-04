package ai

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ---------- «Настройки CoPilot»: the tools where AI works and who may use it (GO24DEV-1005) ----------

// Tools are the places AI works in, by the agent names their calls give; names are locale keys ai.tool.<name>.
var Tools = []string{"tasks", "mail", "im", "openlines", "calls", "telephony"}

const policyKey = "ai_policy" // company_settings

// Policy is the company's: Off — the tools switched off; Users and Depts (with the departments below) — who may use
// AI, none of both — every employee.
type Policy struct {
	Off   []string `json:"off"`
	Users []string `json:"users"`
	Depts []string `json:"depts"`
}

// Everyone: no list of who may use AI.
func (p Policy) Everyone() bool { return len(p.Users) == 0 && len(p.Depts) == 0 }

// Policy is the saved policy (none saved — AI everywhere for everyone).
func (s *Store) Policy(ctx context.Context) (Policy, error) {
	var p Policy
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, policyKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

// SetPolicy keeps the policy (unknown tools are dropped); the caller checks the right.
func (s *Store) SetPolicy(ctx context.Context, by string, p Policy) error {
	var off []string
	for _, t := range Tools {
		if slices.Contains(p.Off, t) {
			off = append(off, t)
		}
	}
	p.Off = off
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		policyKey, raw, by)
	return err
}

// Permit: nil when the user may use AI in the tool ("" — anywhere they may at all); the error says why not.
func (s *Store) Permit(ctx context.Context, userID, tool string) error {
	p, err := s.Policy(ctx)
	if err != nil {
		return err
	}
	if tool != "" && slices.Contains(p.Off, tool) {
		return i18n.E("err.ai_tool_off")
	}
	if p.Everyone() || slices.Contains(p.Users, userID) {
		return nil
	}
	if len(p.Depts) > 0 {
		rows, err := s.db.Query(ctx, `WITH RECURSIVE dep AS (
				SELECT department_id AS id FROM user_departments WHERE user_id::text = $1
				UNION SELECT d.id FROM departments d WHERE d.head_id::text = $1
				UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
			SELECT id::text FROM dep`, userID)
		if err != nil {
			return err
		}
		depts, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if slices.ContainsFunc(p.Depts, func(d string) bool { return slices.Contains(depts, d) }) {
			return nil
		}
	}
	return i18n.E("err.ai_denied")
}
