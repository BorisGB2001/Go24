package bp

import (
	"context"
	"encoding/json"

	"go24/internal/i18n"
)

// StdKeys are the standard templates of the «Процессы в ленте новостей», in the order they are offered.
var StdKeys = []string{"vacation", "trip", "invoice", "payout"}

// Standard are the standard templates in the installer's language (tr), with the accountant who pays and is told: each
// is approved by the starter's manager first. Отпуск and Командировка go to the absence chart when approved.
func Standard(tr func(string) string, accountant string) []Template {
	f := func(key, kind string, required bool) Field {
		return Field{Key: key, Label: tr("bp.std.f." + key), Kind: kind, Required: required}
	}
	text := func(key string) string { return tr("bp.std.s." + key) }
	tpl := func(key string, fields []Field, steps ...Step) Template {
		return Template{Name: tr("bp.std." + key), Description: tr("bp.std." + key + "_hint"), Entity: "none", Fields: fields,
			Steps: steps, Active: true, Std: key}
	}
	return []Template{
		tpl("vacation", []Field{f("from", "date", true), f("to", "date", true), f("comment", "text", false)},
			Step{Kind: "approve", To: "manager", Text: text("vacation_approve")},
			Step{Kind: "absence", Absence: "vacation", Start: "from", End: "to", Text: "{comment}"},
			Step{Kind: "notify", To: accountant, Text: text("vacation_accountant")},
			Step{Kind: "notify", To: "starter", Text: text("vacation_done")}),
		tpl("trip", []Field{f("from", "date", true), f("to", "date", true), f("city", "string", true), f("purpose", "text", true),
			f("sum", "number", false)},
			Step{Kind: "approve", To: "manager", Text: text("trip_approve")},
			Step{Kind: "absence", Absence: "trip", Start: "from", End: "to", Text: "{city}: {purpose}"},
			Step{Kind: "review", To: accountant, Text: text("trip_accountant")},
			Step{Kind: "notify", To: "starter", Text: text("trip_done")}),
		tpl("invoice", []Field{f("what", "string", true), f("supplier", "string", false), f("sum", "number", true), f("due", "date", false),
			f("comment", "text", false)},
			Step{Kind: "approve", To: "manager", Text: text("invoice_approve")},
			Step{Kind: "request", To: accountant, Text: text("invoice_pay"), Fields: []Field{f("paid", "date", true)}, Reject: true},
			Step{Kind: "notify", To: "starter", Text: text("invoice_done")}),
		tpl("payout", []Field{f("sum", "number", true), f("purpose", "text", true)},
			Step{Kind: "approve", To: "manager", Text: text("payout_approve")},
			Step{Kind: "request", To: accountant, Text: text("payout_pay"), Fields: []Field{f("paid", "date", true)}, Reject: true},
			Step{Kind: "notify", To: "starter", Text: text("payout_done")}),
	}
}

// InstallStandard adds the standard templates not installed yet, in the language of tr, with the accountant (an
// employee) — administrators, checked by the web layer; returns how many were added.
func (s *Store) InstallStandard(ctx context.Context, userID, accountant string, tr func(string) string) (int, error) {
	var ok bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active AND NOT guest)`, accountant).Scan(&ok); err != nil {
		return 0, err
	}
	if !ok {
		return 0, i18n.E("err.bp_accountant")
	}
	n := 0
	for _, t := range Standard(tr, accountant) {
		if err := checkTemplate(&t); err != nil {
			return n, err
		}
		f, _ := json.Marshal(t.Fields)
		st, _ := json.Marshal(t.Steps)
		tag, err := s.db.Exec(ctx, `INSERT INTO bp_templates (name, description, entity, fields, steps, active, created_by, std)
			VALUES ($1, $2, 'none', $3, $4, true, $5, $6) ON CONFLICT (std) WHERE std IS NOT NULL DO NOTHING`,
			t.Name, t.Description, f, st, userID, t.Std)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// Installed are the keys of the standard templates there are.
func (s *Store) Installed(ctx context.Context) ([]string, error) {
	var out []string
	err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(std), '{}') FROM bp_templates WHERE std IS NOT NULL`).Scan(&out)
	return out, err
}
