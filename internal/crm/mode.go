package crm

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

// «Режим работы CRM»: «Классический» — requests (letters, forms, open lines, calls) come as leads that
// the managers convert; «Простой» — without leads: each new lead is converted at once into a deal and a contact (and
// a company when it names one), the «Лиды» tab is hidden. Going simple may convert the leads still in work too.

const settingMode = "crm_mode" // company_settings: {"simple": true}

// Simple tells the CRM works without leads.
func (s *Store) Simple(ctx context.Context) (bool, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingMode).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m struct{ Simple bool }
	return m.Simple, json.Unmarshal(raw, &m)
}

// SetSimple sets the mode (administrators; checked by the web layer); with convert, going simple converts the leads
// in work as well. It gives how many were converted and how many failed (they stay leads, told in their timelines).
func (s *Store) SetSimple(ctx context.Context, userID string, simple, convert bool) (done, failed int, err error) {
	raw, _ := json.Marshal(map[string]bool{"simple": simple})
	if _, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingMode, raw, userID); err != nil {
		return 0, 0, err
	}
	if !simple || !convert {
		return 0, 0, nil
	}
	rows, err := s.db.Query(ctx, `SELECT l.id::text FROM crm_leads l JOIN crm_stages st ON st.id = l.stage_id
		WHERE l.deal_id IS NULL AND st.semantic NOT IN ('success', 'failure') ORDER BY l.created_at`)
	if err != nil {
		return 0, 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, 0, err
	}
	for _, id := range ids {
		if s.autoConvert(ctx, userID, id) {
			done++
		} else {
			failed++
		}
	}
	return done, failed, nil
}

// autoConvert converts a lead the simple way: a deal in the first pipeline, the contact (whenever the lead names a
// person or gives a phone or an e-mail) and the company — existing clients when the lead matches them. A failure
// (a required field of the deal, say) leaves the lead as it is, with a line in its timeline.
func (s *Store) autoConvert(ctx context.Context, userID, id string) bool {
	l, err := s.Lead(ctx, id)
	if err != nil {
		return false
	}
	to, err := s.ConvertDefaults(ctx, l)
	if err == nil {
		to.Deal = true
		to.Contact = to.Contact || l.Phone != "" || l.Email != "" || len(l.Comms) > 0
		_, err = s.ConvertLeadTo(ctx, userID, id, to)
	}
	if err != nil {
		_ = s.event(ctx, s.db, "lead", id, userID, "auto_convert_failed", err.Error())
		return false
	}
	return true
}
