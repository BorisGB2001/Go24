package bp

import (
	"context"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Глобальные константы» and «Глобальные переменные» of the designer: values of the company every template
// reads. A template refers to one as the field GlobalPrefix+KEY — {global.KEY} in a text, a condition on it, the
// employee of a user one (field:global.KEY); a step («Изменить переменную», «Выбор сотрудника», «Случайная строка»)
// changes a variable for every process at once, a constant only the administrator changes.

// GlobalPrefix of a global's key among a process's fields.
const GlobalPrefix = "global."

// MaxGlobals of the company.
const MaxGlobals = 200

var globalKeyRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,50}$`)

// Globals of the company as fields: Key without GlobalPrefix, Scope "var" or "const", Default — the value.
func (s *Store) Globals(ctx context.Context) ([]Field, error) {
	rows, err := s.db.Query(ctx, `SELECT key, label, kind, options, is_var, value FROM bp_globals ORDER BY position, key`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Field, error) {
		var f Field
		var isVar bool
		err := r.Scan(&f.Key, &f.Label, &f.Kind, &f.Options, &isVar, &f.Default)
		f.Scope = "const"
		if isVar {
			f.Scope = "var"
		}
		if len(f.Options) == 0 {
			f.Options = nil
		}
		return f, err
	})
}

// GlobalFields are the globals as a template's steps see them: the keys with GlobalPrefix.
func (s *Store) GlobalFields(ctx context.Context) ([]Field, error) {
	list, err := s.Globals(ctx)
	for i := range list {
		list[i].Key = GlobalPrefix + list[i].Key
	}
	return list, err
}

// SaveGlobals replaces the company's globals with the list (in its order). A global a template uses is not removed,
// nor turned from a variable into a constant while a template's step changes it — the template would not save so.
func (s *Store) SaveGlobals(ctx context.Context, userID string, list []Field) error {
	if len(list) > MaxGlobals {
		return i18n.E("err.bp_field")
	}
	seen := map[string]bool{}
	for i, f := range list {
		if !globalKeyRe.MatchString(f.Key) || seen[strings.ToLower(f.Key)] || f.Scope != "var" && f.Scope != "const" {
			return i18n.E("err.bp_field")
		}
		seen[strings.ToLower(f.Key)] = true
		if f.Kind == "file" { // a file — of a process only
			return i18n.E("err.bp_field")
		}
		if err := checkField(f); err != nil {
			return err
		}
		if f.Kind == "datetime" && f.Default != "" { // as the form keeps it
			list[i].Default, _ = cleanValue(f, f.Default)
		}
	}
	old, err := s.Globals(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, o := range old {
		if seen[strings.ToLower(o.Key)] {
			continue
		}
		var name string
		err := tx.QueryRow(ctx, `SELECT name FROM bp_templates WHERE (steps::text || fields::text) ~ $1 ORDER BY name LIMIT 1`, usesRe(o.Key)).Scan(&name)
		if err == nil {
			return i18n.E("err.bp_global_used", o.Label, name)
		} else if err != pgx.ErrNoRows {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM bp_globals`); err != nil {
		return err
	}
	for i, f := range list {
		if _, err := tx.Exec(ctx, `INSERT INTO bp_globals (key, label, kind, options, is_var, value, position, updated_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			f.Key, f.Label, f.Kind, append([]string{}, f.Options...), f.Scope == "var", list[i].Default, i, optional(userID)); err != nil {
			return err
		}
	}
	// the templates must stay valid: a step that changes a global changes a variable
	rows, err := tx.Query(ctx, `SELECT name, steps FROM bp_templates`)
	if err != nil {
		return err
	}
	type named struct {
		Name  string
		Steps []Step
	}
	tpls, err := pgx.CollectRows(rows, pgx.RowToStructByPos[named])
	if err != nil {
		return err
	}
	for _, f := range list {
		for _, t := range tpls {
			for _, st := range t.Steps {
				if f.Scope == "const" && st.Field == GlobalPrefix+f.Key && contains([]string{"set", "pick", "random"}, st.Kind) {
					return i18n.E("err.bp_global_used", f.Label, t.Name)
				}
			}
		}
	}
	return tx.Commit(ctx)
}

// usesRe is the regular expression of a template's text that refers to the global.
func usesRe(key string) string {
	return regexp.QuoteMeta(GlobalPrefix+key) + `([^A-Za-z0-9_]|$)`
}

// withGlobals puts the globals into the process being run: their values among its values, their fields among its.
func (s *Store) withGlobals(ctx context.Context, x *Instance) error {
	list, err := s.GlobalFields(ctx)
	if err != nil || len(list) == 0 {
		return err
	}
	if x.Values == nil {
		x.Values = map[string]string{}
	}
	for _, f := range list {
		x.Values[f.Key] = f.Default
	}
	x.globals = list
	return nil
}

// ownValues are the process's values without the globals; a global variable a step changed is saved for all.
func (s *Store) ownValues(ctx context.Context, x Instance) (map[string]string, error) {
	own := make(map[string]string, len(x.Values))
	for k, v := range x.Values {
		key, ok := strings.CutPrefix(k, GlobalPrefix)
		if !ok {
			own[k] = v
			continue
		}
		if f, ok := fieldOf(x.globals, k); ok && f.Scope == "var" && v != f.Default {
			if _, err := s.db.Exec(ctx, `UPDATE bp_globals SET value = $2, updated_by = NULL, updated_at = now() WHERE key = $1`, key, v); err != nil {
				return nil, err
			}
		}
	}
	return own, nil
}
