package crm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Настроить колонки» of the portal CRM grids: each person picks which columns of a list are shown and in what
// order — the card's own fields and the custom ones ("f:" + the field's id); the title is always the first column.

// ListColumns are the optional built-in columns of a list by its kind: deal, item (a smart process), lead, contact,
// company.
var ListColumns = map[string][]string{
	"deal": {"stage", "amount", "client", "activities", "responsible", "created", "close_date", "deal_type", "origin",
		"observers", "closed_at"},
	"item": {"stage", "amount", "client", "activities", "responsible", "created", "close_date", "origin", "observers",
		"closed_at"},
	"lead": {"stage", "amount", "contact_data", "activities", "responsible", "created", "name", "company_title", "phone",
		"email", "origin", "source_info", "observers"},
	"contact": {"company", "phone", "email", "contact_type", "responsible", "position", "origin", "created"},
	"company": {"contacts", "deals", "phone", "company_type", "responsible", "industry", "email", "website", "address",
		"created"},
}

// DefaultListColumns is what a list shows until the person picks its columns (or after «Сбросить»).
var DefaultListColumns = map[string][]string{
	"deal":    {"stage", "amount", "client", "activities", "responsible", "created"},
	"item":    {"stage", "amount", "client", "activities", "responsible", "created"},
	"lead":    {"stage", "amount", "contact_data", "activities", "responsible", "created"},
	"contact": {"company", "phone", "email", "contact_type", "responsible"},
	"company": {"contacts", "deals", "phone", "company_type", "responsible"},
}

// ColumnScope is the list the choice belongs to: "deal", "lead", "contact", "company" or the smart process "type:ID".
func ColumnScope(entity, typeID string) string { return PresetScope(entity, typeID) }

// ColumnKind is the kind of columns of a scope.
func ColumnKind(scope string) string {
	if strings.HasPrefix(scope, "type:") {
		return "item"
	}
	return scope
}

const prefColumns = "crm.columns:"

// Columns are the person's columns of the list in their order; fields are the custom fields one may show.
func (s *Store) Columns(ctx context.Context, userID, scope string, fields []string) ([]string, error) {
	kind := ColumnKind(scope)
	cols := DefaultListColumns[kind]
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefColumns+scope).Scan(&raw)
	if err == nil {
		var saved []string
		if json.Unmarshal(raw, &saved) == nil {
			cols = saved
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out := []string{}
	for _, c := range cols { // a deleted field or a column no more known drops out
		if slices.Contains(ListColumns[kind], c) || strings.HasPrefix(c, "f:") && slices.Contains(fields, c[2:]) {
			out = append(out, c)
		}
	}
	return out, nil
}

// SetColumns saves the person's columns of the list in their order; nil — back to the default ones.
func (s *Store) SetColumns(ctx context.Context, userID, scope string, cols, fields []string) error {
	kind := ColumnKind(scope)
	if ListColumns[kind] == nil {
		return i18n.E("err.bad_form")
	}
	if cols == nil {
		_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefColumns+scope)
		return err
	}
	clean := []string{}
	for _, c := range cols {
		if !slices.Contains(ListColumns[kind], c) && !(strings.HasPrefix(c, "f:") && slices.Contains(fields, c[2:])) {
			return i18n.E("err.bad_form")
		}
		if !slices.Contains(clean, c) {
			clean = append(clean, c)
		}
	}
	raw, _ := json.Marshal(clean)
	_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefColumns+scope, raw)
	return err
}

// ObserversOf are the observers of the listed cards, by card and name (the list's «Наблюдатели» column).
func (s *Store) ObserversOf(ctx context.Context, entity string, ids []string) (map[string][]Ref, error) {
	out := map[string][]Ref{}
	if !watched(entity) || len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT o.item_id::text, u.id::text, u.name FROM crm_`+entity+`_observers o
		JOIN users u ON u.id = o.user_id WHERE o.item_id::text = ANY($1) ORDER BY u.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var x Ref
		if err := rows.Scan(&id, &x.ID, &x.Name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], x)
	}
	return out, rows.Err()
}
