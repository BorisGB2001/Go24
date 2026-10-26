package tasks

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// «Пользовательские поля» tasks: an administrator defines them (checked by the web layer); the people
// who may edit a task fill them in its card, and the history keeps the changes.

// FieldKinds are the field types an administrator can choose.
var FieldKinds = []string{"string", "number", "date", "bool", "list", "url"}

type Field struct {
	ID      string
	Name    string
	Kind    string
	Options []string // a list's values
	Sort    int
	// «Обязательное поле» (the CRM cards): required at the Stages of deals or leads, at every stage when none.
	Required bool
	Stages   []string
}

// RequiredAt: must the field be filled on a card at the stage? A yes/no field never must.
func (f Field) RequiredAt(stage string) bool {
	return f.Required && f.Kind != "bool" && (len(f.Stages) == 0 || slices.Contains(f.Stages, stage))
}

// FieldValue is a field with its value on one task ("" — not filled).
type FieldValue struct {
	Field
	Value string
}

// Text is the value as people read it (dates in the given layout).
func (v FieldValue) Text(dateLayout string) string {
	if v.Kind == "date" {
		if d, err := time.Parse(time.DateOnly, v.Value); err == nil {
			return d.Format(dateLayout)
		}
	}
	return v.Value
}

// FieldSet is what the fields belong to: tasks or employees' profiles (the same kinds of fields, own tables).
type FieldSet struct{ defs, values, owner string }

var (
	TaskFieldSet = FieldSet{"task_fields", "task_field_values", "task_id"}
	UserFieldSet = FieldSet{"user_fields", "user_field_values", "user_id"}
	// CRMFieldSets are the fields of the CRM cards by entity: deal, lead, contact, company.
	CRMFieldSets = map[string]FieldSet{
		"deal":    {"crm_deal_fields", "crm_deal_field_values", "deal_id"},
		"lead":    {"crm_lead_fields", "crm_lead_field_values", "lead_id"},
		"contact": {"crm_contact_fields", "crm_contact_field_values", "contact_id"},
		"company": {"crm_company_fields", "crm_company_field_values", "company_id"},
	}
)

// Fields are the defined task fields in their order.
func (s *Store) Fields(ctx context.Context) ([]Field, error) { return s.FieldsOf(ctx, TaskFieldSet) }

// FieldsOf are the fields of a set in their order.
func (s *Store) FieldsOf(ctx context.Context, set FieldSet) ([]Field, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, kind, options, sort, required, required_stages FROM `+set.defs+` ORDER BY sort, name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Field, error) {
		var f Field
		err := r.Scan(&f.ID, &f.Name, &f.Kind, &f.Options, &f.Sort, &f.Required, &f.Stages)
		return f, err
	})
}

// AddField defines a new task field.
func (s *Store) AddField(ctx context.Context, f Field) error {
	return s.AddFieldTo(ctx, TaskFieldSet, f)
}

// AddFieldTo defines a new field of the set; a list needs its values.
func (s *Store) AddFieldTo(ctx context.Context, set FieldSet, f Field) error {
	f.Name = strings.TrimSpace(f.Name)
	opts := []string{}
	for _, o := range f.Options {
		if o = strings.TrimSpace(o); o != "" && !slices.Contains(opts, o) {
			opts = append(opts, o)
		}
	}
	if f.Name == "" || len([]rune(f.Name)) > 100 || !slices.Contains(FieldKinds, f.Kind) || (f.Kind == "list") != (len(opts) > 0) {
		return invalid("err.field")
	}
	_, err := s.db.Exec(ctx, `INSERT INTO `+set.defs+` (name, kind, options, sort, required, required_stages) VALUES ($1, $2, $3, $4, $5, $6)`,
		f.Name, f.Kind, opts, f.Sort, f.Required, stageList(f.Required, f.Stages))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return invalid("err.field_exists")
	}
	return err
}

// SetFieldRequired makes a field of the set required (at the stages, or always) or not.
func (s *Store) SetFieldRequired(ctx context.Context, set FieldSet, id string, required bool, stages []string) error {
	tag, err := s.db.Exec(ctx, `UPDATE `+set.defs+` SET required = $2, required_stages = $3 WHERE id = $1`, id, required, stageList(required, stages))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// stageList: the stages of a required field without blanks and repeats (none for a field not required).
func stageList(required bool, stages []string) []string {
	out := []string{}
	for _, st := range stages {
		if st = strings.TrimSpace(st); required && st != "" && !slices.Contains(out, st) {
			out = append(out, st)
		}
	}
	return out
}

// DeleteField removes a task field with its values on every task.
func (s *Store) DeleteField(ctx context.Context, id string) error {
	return s.DeleteFieldOf(ctx, TaskFieldSet, id)
}

// DeleteFieldOf removes a field of the set with all its values.
func (s *Store) DeleteFieldOf(ctx context.Context, set FieldSet, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM `+set.defs+` WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FieldValues are all defined fields with the task's values.
func (s *Store) FieldValues(ctx context.Context, taskID string) ([]FieldValue, error) {
	return fieldValues(ctx, s.db, TaskFieldSet, taskID)
}

// FieldValuesOf are all fields of the set with the owner's values.
func (s *Store) FieldValuesOf(ctx context.Context, set FieldSet, ownerID string) ([]FieldValue, error) {
	return fieldValues(ctx, s.db, set, ownerID)
}

func fieldValues(ctx context.Context, q querier, set FieldSet, ownerID string) ([]FieldValue, error) {
	rows, err := q.Query(ctx, `SELECT f.id, f.name, f.kind, f.options, f.sort, f.required, f.required_stages, coalesce(v.value, '')
		FROM `+set.defs+` f LEFT JOIN `+set.values+` v ON v.field_id = f.id AND v.`+set.owner+` = $1
		ORDER BY f.sort, f.name`, ownerID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (FieldValue, error) {
		var v FieldValue
		err := r.Scan(&v.ID, &v.Name, &v.Kind, &v.Options, &v.Sort, &v.Required, &v.Stages, &v.Value)
		return v, err
	})
}

// Check is normalize for the other sections (a CRM form checks a client's answer with it).
func (f Field) Check(v string) (string, error) { return f.normalize(v) }

// normalize checks a value against its field's type ("" clears it).
func (f Field) normalize(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	bad := invalid("err.field_value", f.Name)
	switch f.Kind {
	case "string":
		if len([]rune(v)) > 2000 {
			return "", bad
		}
	case "number":
		n, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(v, " ", ""), ",", "."), 64)
		if err != nil {
			return "", bad
		}
		v = strconv.FormatFloat(n, 'f', -1, 64)
	case "date":
		if _, err := time.Parse(time.DateOnly, v); err != nil {
			return "", bad
		}
	case "bool":
		if v != "1" {
			return "", bad
		}
	case "list":
		if !slices.Contains(f.Options, v) {
			return "", bad
		}
	case "url":
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(v) > 2000 {
			return "", bad
		}
	}
	return v, nil
}

// SetFieldValues saves the values of the fields given (field id → value, "" clears it; fields not given are kept).
// The creator and project managers may, as for the other fields of the task; the history keeps the changes.
func (s *Store) SetFieldValues(ctx context.Context, userID, taskID string, values map[string]string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		a, err := loadAccess(ctx, tx, userID, taskID, true)
		if err != nil {
			return err
		}
		if !(a.Roles.Creator || a.Roles.Manager) {
			return ErrForbidden
		}
		err = saveFieldValues(ctx, tx, TaskFieldSet, taskID, values, func(old, next string) error {
			return addHistory(ctx, tx, taskID, userID, "field", old, next)
		})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE tasks SET updated_at = now() WHERE id = $1`, taskID)
		return err
	})
}

// SetUserFieldValues saves an employee's profile fields (the web layer lets the employee and administrators).
func (s *Store) SetUserFieldValues(ctx context.Context, userID string, values map[string]string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return saveFieldValues(ctx, tx, UserFieldSet, userID, values, nil)
	})
}

// SetFieldValuesOf writes the owner's values of a set (field id → value; "" clears, absent ones stay).
func (s *Store) SetFieldValuesOf(ctx context.Context, set FieldSet, ownerID string, values map[string]string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		return saveFieldValues(ctx, tx, set, ownerID, values, nil)
	})
}

// saveFieldValues writes the changed values; changed hears each change as "Name: value" before and after.
func saveFieldValues(ctx context.Context, tx pgx.Tx, set FieldSet, ownerID string, values map[string]string, changed func(old, next string) error) error {
	cur, err := fieldValues(ctx, tx, set, ownerID)
	if err != nil {
		return err
	}
	for _, f := range cur {
		raw, ok := values[f.ID]
		if !ok {
			continue
		}
		v, err := f.normalize(raw)
		if err != nil {
			return err
		}
		if v == f.Value {
			continue
		}
		if v == "" {
			_, err = tx.Exec(ctx, `DELETE FROM `+set.values+` WHERE `+set.owner+` = $1 AND field_id = $2`, ownerID, f.ID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO `+set.values+` (`+set.owner+`, field_id, value) VALUES ($1, $2, $3)
				ON CONFLICT (`+set.owner+`, field_id) DO UPDATE SET value = EXCLUDED.value`, ownerID, f.ID, v)
		}
		if err != nil {
			return err
		}
		if changed == nil {
			continue
		}
		var old, next string
		if f.Value != "" {
			old = f.Name + ": " + f.Value
		}
		if v != "" {
			next = f.Name + ": " + v
		}
		if err := changed(old, next); err != nil {
			return err
		}
	}
	return nil
}

// ValuesOf are the filled values of the set's fields of many owners: owner → field → value (the CRM list's columns).
func (s *Store) ValuesOf(ctx context.Context, set FieldSet, ownerIDs []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	if len(ownerIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+set.owner+`::text, field_id::text, value FROM `+set.values+`
		WHERE `+set.owner+`::text = ANY($1) AND value <> ''`, ownerIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var owner, field, value string
		if err := rows.Scan(&owner, &field, &value); err != nil {
			return nil, err
		}
		if out[owner] == nil {
			out[owner] = map[string]string{}
		}
		out[owner][field] = value
	}
	return out, rows.Err()
}
