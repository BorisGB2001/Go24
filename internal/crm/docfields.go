package crm

import (
	"context"
	"strings"
	"unicode/utf8"

	"go24/internal/i18n"
)

// DocField is the setting of a field of a template, as b_documentgenerator_field: its title in
// «Изменить значения», the value by default (when the card has none), required, «Скрывать строку, если пусто».
type DocField struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Value    string `json:"value"`
	Required bool   `json:"required"`
	HideRow  bool   `json:"hide_row"`
}

// Set tells whether the field has any setting at all.
func (f DocField) Set() bool { return f.Title != "" || f.Value != "" || f.Required || f.HideRow }

// Label is its title, or {Name} with none.
func (f DocField) Label() string {
	if f.Title != "" {
		return f.Title
	}
	return "{" + f.Name + "}"
}

// DocTemplateFields are the settings of the fields of the template by their names (fields with none are absent).
func (s *Store) DocTemplateFields(ctx context.Context, id string) (map[string]DocField, error) {
	rows, err := s.db.Query(ctx, `SELECT name, title, value, required, hide_row FROM crm_doc_template_fields
		WHERE template_id::text = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DocField{}
	for rows.Next() {
		var f DocField
		if err := rows.Scan(&f.Name, &f.Title, &f.Value, &f.Required, &f.HideRow); err != nil {
			return nil, err
		}
		out[f.Name] = f
	}
	return out, rows.Err()
}

// SetDocTemplateFields saves the settings of the template's fields, all at once (those not given have none), changed
// by the user.
func (s *Store) SetDocTemplateFields(ctx context.Context, id, userID string, fields []DocField) error {
	for i, f := range fields {
		f.Name, f.Title = strings.TrimSpace(f.Name), strings.TrimSpace(f.Title)
		if f.Name == "" || utf8.RuneCountInString(f.Name) > 200 || utf8.RuneCountInString(f.Title) > 200 ||
			utf8.RuneCountInString(f.Value) > 2000 {
			return i18n.E("err.doc_field_invalid", f.Name)
		}
		fields[i] = f
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM crm_doc_templates WHERE id::text = $1`, id).Scan(&n); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM crm_doc_template_fields WHERE template_id::text = $1`, id); err != nil {
		return err
	}
	for _, f := range fields {
		if !f.Set() {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO crm_doc_template_fields (template_id, name, title, value, required, hide_row)
			VALUES ($1::uuid, $2, $3, $4, $5, $6) ON CONFLICT (template_id, name) DO UPDATE
			SET title = EXCLUDED.title, value = EXCLUDED.value, required = EXCLUDED.required, hide_row = EXCLUDED.hide_row`,
			id, f.Name, f.Title, f.Value, f.Required, f.HideRow); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE crm_doc_templates SET updated_by = $2, updated_at = now() WHERE id::text = $1`,
		id, optional(userID)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FieldDefaults puts the values by default of the fields into values where they are empty.
func FieldDefaults(fields map[string]DocField, values map[string]string) {
	for n, f := range fields {
		if f.Value != "" && strings.TrimSpace(values[n]) == "" {
			values[n] = f.Value
		}
	}
}

// MissingFields are the labels of the required fields of names left empty in values, in the order of names.
func MissingFields(fields map[string]DocField, names []string, values map[string]string) []string {
	var out []string
	for _, n := range names {
		if f, ok := fields[n]; ok && f.Required && strings.TrimSpace(values[n]) == "" {
			out = append(out, f.Label())
		}
	}
	return out
}

// HiddenFields are the names of the fields to hide when empty.
func HiddenFields(fields map[string]DocField) map[string]bool {
	out := map[string]bool{}
	for n, f := range fields {
		if f.HideRow {
			out[n] = true
		}
	}
	return out
}
