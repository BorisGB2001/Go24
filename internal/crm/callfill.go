package crm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// the CoPilot «заполнение полей по звонку»: the AI reads a conversation with the client and finds the values of
// the fields of the card being worked on (the client's deal or the lead). An empty field is filled at once; a filled one
// that the conversation says otherwise is offered — «Применить» or «Отклонить», for one who may edit the card. All of it
// is one line of the card's timeline (kind "copilot_fill").

// FillField is a field of a card CoPilot may fill: a built-in one (amount, comment, …) or the card's own («uf:» + id).
type FillField struct {
	Key, Name string // Name — of an own field ("" — a built-in one: the locale crm.f.<key>)
	Kind      string // string, number, date, bool, list, url
	Options   []string
	Value     string // now
}

// fillBuiltin are the built-in fields CoPilot fills, by the entity.
var fillBuiltin = map[string][]string{
	"deal": {"amount", "comment"},
	"lead": {"name", "company_title", "email", "amount", "comment"},
}

// FillTarget is the card a conversation with the client fills: the deal, a lead (converted — its deal), for a contact
// or a company the newest of their open deals; "" — none.
func (s *Store) FillTarget(ctx context.Context, entity, id string) (string, string, error) {
	var deal string
	var err error
	switch entity {
	case "deal":
		return "deal", id, nil
	case "lead":
		var converted string
		err = s.db.QueryRow(ctx, `SELECT coalesce(deal_id::text, '') FROM crm_leads WHERE id::text = $1`, id).Scan(&converted)
		if converted == "" && err == nil {
			return "lead", id, nil
		}
		deal = converted
	case "contact", "company":
		err = s.db.QueryRow(ctx, `SELECT d.id::text FROM crm_deals d JOIN crm_pipelines p ON p.id = d.pipeline_id
			JOIN crm_stages st ON st.id = d.stage_id
			WHERE p.type_id IS NULL AND st.semantic = 'process' AND (CASE WHEN $1 = 'company' THEN d.company_id::text = $2
				ELSE d.contact_id::text = $2 OR EXISTS (SELECT 1 FROM crm_deal_contacts dc WHERE dc.deal_id = d.id AND dc.contact_id::text = $2) END)
			ORDER BY d.created_at DESC LIMIT 1`, entity, id).Scan(&deal)
	}
	if errors.Is(err, pgx.ErrNoRows) || deal == "" {
		return "", "", nil
	}
	return "deal", deal, err
}

// FillFields are the fields of the card CoPilot may fill, with their values now.
func (s *Store) FillFields(ctx context.Context, entity, id string) ([]FillField, error) {
	values, err := s.cardValues(ctx, card{entity: entity, id: id})
	if err != nil {
		return nil, err
	}
	var list []FillField
	for _, k := range fillBuiltin[entity] {
		kind := "string"
		if k == "amount" {
			kind = "number"
			if values[k] == "0" {
				values[k] = ""
			}
		}
		list = append(list, FillField{Key: k, Kind: kind, Value: values[k]})
	}
	own, err := s.tasksStore().FieldValuesOf(ctx, tasks.CRMFieldSets[entity], id)
	if err != nil {
		return nil, err
	}
	for _, f := range own {
		list = append(list, FillField{Key: "uf:" + f.ID, Name: f.Name, Kind: f.Kind, Options: f.Options, Value: f.Value})
	}
	return list, nil
}

// SetCardFields writes fields of a deal or a lead (FillField keys → values) as the card's form and «Поля CRM» do; the
// history shows the changes as the actor's.
func (s *Store) SetCardFields(ctx context.Context, actorID, entity, id string, values map[string]string) error {
	sets := make([]RobotSet, 0, len(values))
	for k, v := range values {
		sets = append(sets, RobotSet{Field: k, Value: v})
	}
	return s.setFields(ctx, actorID, card{entity: entity, id: id}, sets)
}

// FillItem is a field in CoPilot's line of the timeline.
type FillItem struct {
	Field string `json:"f"`              // a FillField key
	Name  string `json:"name,omitempty"` // an own field's name then
	Kind  string `json:"k,omitempty"`    // date, bool — shown as such
	Old   string `json:"o,omitempty"`
	New   string `json:"n"`
	State string `json:"s"` // filled (was empty) | offered | applied | rejected
}

// Fill is CoPilot's line of the timeline: what a call filled in the card and what it offers.
type Fill struct {
	Call  string     `json:"call"`
	Items []FillItem `json:"items"`
}

// ParseFill reads the body of a "copilot_fill" line.
func ParseFill(body string) Fill {
	var f Fill
	_ = json.Unmarshal([]byte(body), &f)
	return f
}

// Offered: how many values wait for a decision.
func (f Fill) Offered() int {
	n := 0
	for _, it := range f.Items {
		if it.State == "offered" {
			n++
		}
	}
	return n
}

// AddFill writes CoPilot's line into the card's timeline.
func (s *Store) AddFill(ctx context.Context, entity, id, author string, f Fill) error {
	body, err := json.Marshal(f)
	if err != nil {
		return err
	}
	return s.event(ctx, s.db, entity, id, author, "copilot_fill", string(body))
}

// DecideFill applies («apply») or rejects an offered value of CoPilot's line (its n-th field) of the card.
func (s *Store) DecideFill(ctx context.Context, userID, entity, id string, eventID int64, n int, apply bool) error {
	var body string
	err := s.db.QueryRow(ctx, `SELECT body FROM crm_timeline WHERE entity = $1 AND entity_id::text = $2 AND id = $3 AND kind = 'copilot_fill'`,
		entity, id, eventID).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	f := ParseFill(body)
	if n < 0 || n >= len(f.Items) {
		return ErrNotFound
	}
	it := &f.Items[n]
	if it.State != "offered" {
		return i18n.E("err.crm_fill_decided")
	}
	it.State = "rejected"
	if apply {
		if err := s.SetCardFields(ctx, userID, entity, id, map[string]string{it.Field: it.New}); err != nil {
			return err
		}
		it.State = "applied"
	}
	next, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE crm_timeline SET body = $2 WHERE id = $1`, eventID, string(next))
	return err
}

// FillValue tidies a value CoPilot found for the field: a number with a dot, a date as 2006-01-02, a list's own
// option, a yes as "1"; ok false — it does not fit the field.
func FillValue(f FillField, v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	switch f.Kind {
	case "number":
		n, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(v, " ", ""), " ", ""), ",", "."), 64)
		if err != nil || n < 0 {
			return "", false
		}
		return strconv.FormatFloat(n, 'f', -1, 64), true
	case "date":
		if len(v) < 10 {
			return "", false
		}
		v = v[:10]
		if _, err := time.Parse(time.DateOnly, v); err != nil {
			return "", false
		}
		return v, true
	case "bool":
		switch strings.ToLower(v) {
		case "1", "true", "yes", "да":
			return "1", true
		}
		return "", false
	case "list":
		for _, o := range f.Options {
			if strings.EqualFold(o, v) {
				return o, true
			}
		}
		return "", false
	}
	if len([]rune(v)) > 2000 {
		v = string([]rune(v)[:2000])
	}
	return v, true
}
