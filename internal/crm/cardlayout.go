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

// «Настроить карточку»: the fields of a card are grouped in sections («О сделке», «Дополнительно» and
// one's own), in the order one drags them to; a hidden field leaves the card and waits under «Скрытые поля» of its
// form. The view is the person's own; an administrator makes theirs the common one («Сделать общим видом для всех»),
// which everybody sees until they change their own.

// CardFields are the built-in fields of a card by its kind (deal, item — a smart process, lead, contact, company) in
// the standard order; the title (the contact's name) stays at the card's top and is not among them.
var CardFields = map[string][]string{
	"deal":    {"deal_type", "origin", "amount", "company", "contacts", "close_date", "responsible", "observers", "comment", "utm", "created"},
	"item":    {"origin", "amount", "company", "contacts", "close_date", "responsible", "observers", "comment", "utm", "created"},
	"lead":    {"name", "company_title", "origin", "phone", "email", "amount", "responsible", "observers", "comment", "utm", "created"},
	"contact": {"position", "contact_type", "origin", "phone", "email", "company", "responsible", "comment", "created"},
	"company": {"company_type", "industry", "phone", "email", "website", "address", "responsible", "comment", "created"},
}

// CardSection is a section of the card: Code "main" and "more" are the standard ones, named by the kind of card
// until renamed; Fields — the built-in codes and the custom fields ("f:" + the field's id).
type CardSection struct {
	Code   string   `json:"code,omitempty"`
	Name   string   `json:"name,omitempty"`
	Fields []string `json:"fields"`
}

// CardLayout is a view of the card: its sections in order and the hidden fields.
type CardLayout struct {
	Sections []CardSection `json:"sections"`
	Hidden   []string      `json:"hidden,omitempty"`
}

// CardScope is whose view it is: "deal", "lead", "contact", "company" or the smart process "type:ID".
func CardScope(entity, typeID string) string { return PresetScope(entity, typeID) }

const (
	prefCard    = "crm.card:" // user_prefs, the person's view
	settingCard = "crm_card:" // company_settings, the common view
)

// DefaultCardLayout is the standard view: the built-in fields in «О …», the custom ones in «Дополнительно».
func DefaultCardLayout(scope string, fields []string) CardLayout {
	main := slices.Clone(CardFields[ColumnKind(scope)])
	more := []string{}
	for _, f := range fields {
		more = append(more, "f:"+f)
	}
	return CardLayout{Sections: []CardSection{{Code: "main", Fields: main}, {Code: "more", Fields: more}}}
}

// clean keeps the known fields once each; a field not placed (a new custom field, a built-in one added later) goes to
// the last section for a custom field and to the first for a built-in one. A section keeps its place when empty.
func (l CardLayout) clean(scope string, fields []string) CardLayout {
	known := slices.Clone(CardFields[ColumnKind(scope)])
	for _, f := range fields {
		known = append(known, "f:"+f)
	}
	seen := map[string]bool{}
	take := func(list []string) []string {
		out := []string{}
		for _, c := range list {
			if slices.Contains(known, c) && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
		return out
	}
	out := CardLayout{}
	for _, s := range l.Sections {
		out.Sections = append(out.Sections, CardSection{Code: s.Code, Name: strings.TrimSpace(s.Name), Fields: take(s.Fields)})
	}
	out.Hidden = take(l.Hidden)
	if len(out.Sections) == 0 {
		out.Sections = []CardSection{{Code: "main", Fields: []string{}}}
	}
	for _, c := range known {
		if seen[c] {
			continue
		}
		if strings.HasPrefix(c, "f:") {
			last := &out.Sections[len(out.Sections)-1]
			last.Fields = append(last.Fields, c)
		} else {
			out.Sections[0].Fields = append(out.Sections[0].Fields, c)
		}
	}
	return out
}

// CardLayout is the view the person sees: their own, else the common one, else the standard; own tells it is theirs,
// common that the company has a common one.
func (s *Store) CardLayout(ctx context.Context, userID, scope string, fields []string) (l CardLayout, own, common bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingCard+scope).Scan(&raw)
	if err == nil {
		common = json.Unmarshal(raw, &l) == nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return l, false, false, err
	}
	raw = nil
	err = s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefCard+scope).Scan(&raw)
	if err == nil {
		var mine CardLayout
		if json.Unmarshal(raw, &mine) == nil {
			l, own = mine, true
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return l, false, false, err
	}
	if !own && !common {
		l = DefaultCardLayout(scope, fields)
	}
	return l.clean(scope, fields), own, common, nil
}

// SetCardLayout saves the view: the person's own, or (common) the company's one for everybody — then the person's
// own view gives way to it.
func (s *Store) SetCardLayout(ctx context.Context, userID, scope string, l CardLayout, fields []string, common bool) error {
	if CardFields[ColumnKind(scope)] == nil {
		return i18n.E("err.bad_form")
	}
	for _, x := range l.Sections {
		if len([]rune(strings.TrimSpace(x.Name))) > 100 || x.Code != "" && x.Code != "main" && x.Code != "more" {
			return i18n.E("err.bad_form")
		}
	}
	if len(l.Sections) > 30 {
		return i18n.E("err.bad_form")
	}
	raw, _ := json.Marshal(l.clean(scope, fields))
	if !common {
		_, err := s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefCard+scope, raw)
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingCard+scope, raw, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefCard+scope); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResetCardLayout: the person's view back to the common one («Вернуть общий вид»), or (common) the common one back to
// the standard («Сбросить на стандартный»).
func (s *Store) ResetCardLayout(ctx context.Context, userID, scope string, common bool) error {
	if common {
		_, err := s.db.Exec(ctx, `DELETE FROM company_settings WHERE key = $1`, settingCard+scope)
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefCard+scope)
	return err
}
