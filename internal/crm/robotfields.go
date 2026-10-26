package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// the «Изменить элемент» (a fields robot sets fields of the card), «Webhook» (a webhook robot posts the card to
// an address) and the «Условие» any robot may have: it runs only when a field of the card compares as told.

// RobotSet is a field a fields robot sets: a built-in one (RobotFieldKeys) or a card's own («uf:» and its id).
type RobotSet struct {
	Field string `json:"field"`
	Value string `json:"value"` // may hold {title}, {amount}, … as robot texts; "" clears the field
}

// RobotCond is a robot's condition.
type RobotCond struct {
	Field string `json:"field"`
	Op    string `json:"op"` // RobotCondOps
	Value string `json:"value,omitempty"`
}

// RobotCondOps in the order the settings offer them.
var RobotCondOps = []string{"eq", "ne", "gt", "lt", "contains", "empty", "filled"}

// RobotFieldKeys are the built-in fields robots know of the cards of the entity; responsible is for conditions only
// (a responsible robot changes it), origin and deal_type hold the id of an item of the CRM's dictionaries.
var RobotFieldKeys = map[string][]string{
	"deal": {"title", "amount", "origin", "deal_type", "comment", "responsible"},
	"lead": {"title", "name", "company_title", "phone", "email", "amount", "origin", "source_info", "comment", "responsible"},
}

// robotDicts: the built-in fields that are dictionary items, by the dictionary's kind.
var robotDicts = map[string]string{"origin": "source", "deal_type": "deal_type"}

// maxRobotSets is how many fields one fields robot may set.
const maxRobotSets = 10

// webhookClient waits a while for the address to answer; the robot waits in the background.
var webhookClient = &http.Client{Timeout: 15 * time.Second}

// checkRobotField: is the field one of the entity's (built-in or own)?
func (s *Store) checkRobotField(ctx context.Context, entity, field string) bool {
	if id, ok := strings.CutPrefix(field, "uf:"); ok {
		fields, err := s.tasksStore().FieldsOf(ctx, tasks.CRMFieldSets[entity])
		return err == nil && slices.ContainsFunc(fields, func(f tasks.Field) bool { return f.ID == id })
	}
	return slices.Contains(RobotFieldKeys[entity], field)
}

func (s *Store) tasksStore() *tasks.Store {
	if s.Tasks != nil {
		return s.Tasks
	}
	return tasks.NewStore(s.db)
}

// checkRobotExtra checks (and tidies) what the fields and webhook robots and a condition add to a robot's settings.
func (s *Store) checkRobotExtra(ctx context.Context, stageID, kind string, p *RobotParams) error {
	var entity string
	if err := s.db.QueryRow(ctx, `SELECT entity FROM crm_stages WHERE id::text = $1`, stageID).Scan(&entity); err != nil {
		return i18n.E("err.crm_stage")
	}
	if c := p.If; c != nil {
		c.Field, c.Value = strings.TrimSpace(c.Field), strings.TrimSpace(c.Value)
		if c.Field == "" {
			p.If = nil
		} else if !s.checkRobotField(ctx, entity, c.Field) || !slices.Contains(RobotCondOps, c.Op) || len([]rune(c.Value)) > 500 {
			return i18n.E("err.robot_cond")
		} else if c.Op == "empty" || c.Op == "filled" {
			c.Value = ""
		}
	}
	switch kind {
	case "fields":
		sets := p.Set[:0]
		for _, f := range p.Set {
			if f.Field = strings.TrimSpace(f.Field); f.Field == "" {
				continue
			}
			if f.Field == "responsible" || !s.checkRobotField(ctx, entity, f.Field) || len([]rune(f.Value)) > 2000 {
				return i18n.E("err.robot_fields")
			}
			sets = append(sets, RobotSet{f.Field, strings.TrimSpace(f.Value)})
		}
		if len(sets) == 0 || len(sets) > maxRobotSets {
			return i18n.E("err.robot_fields")
		}
		p.Set = sets
	case "webhook":
		p.URL = strings.TrimSpace(p.URL)
		if u, err := url.Parse(p.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(p.URL) > 1000 {
			return i18n.E("err.hook_url")
		}
	default:
		p.Set, p.URL = nil, ""
	}
	return nil
}

// cardValues are the card's fields as robots compare them: the built-in ones by RobotFieldKeys, its own by «uf:id».
func (s *Store) cardValues(ctx context.Context, c card) (map[string]string, error) {
	v := map[string]string{}
	if c.entity == "deal" {
		d, err := s.Deal(ctx, c.id)
		if err != nil {
			return nil, err
		}
		v["title"], v["origin"], v["deal_type"], v["comment"], v["responsible"] = d.Title, d.Origin.ID, d.DealType.ID, d.Comment, d.Responsible.ID
		v["amount"] = strconv.FormatFloat(d.Amount, 'f', -1, 64)
	} else {
		l, err := s.Lead(ctx, c.id)
		if err != nil {
			return nil, err
		}
		v["title"], v["name"], v["company_title"], v["phone"], v["email"] = l.Title, l.Name, l.CompanyTitle, l.Phone, l.Email
		v["origin"], v["source_info"], v["comment"], v["responsible"] = l.Origin.ID, l.Source, l.Comment, l.Responsible.ID
		v["amount"] = strconv.FormatFloat(l.Amount, 'f', -1, 64)
	}
	own, err := s.tasksStore().FieldValuesOf(ctx, tasks.CRMFieldSets[c.entity], c.id)
	if err != nil {
		return nil, err
	}
	for _, f := range own {
		v["uf:"+f.ID] = f.Value
	}
	return v, nil
}

// robotCondOK: does the card meet the condition? Numbers compare as numbers, the rest as text without case.
func robotCondOK(cond RobotCond, values map[string]string) bool {
	have := strings.TrimSpace(values[cond.Field])
	switch cond.Op {
	case "empty":
		return have == ""
	case "filled":
		return have != ""
	case "gt", "lt":
		a, errA := strconv.ParseFloat(have, 64)
		b, errB := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(cond.Value, " ", ""), ",", "."), 64)
		if errA != nil || errB != nil {
			return false
		}
		return (cond.Op == "gt" && a > b) || (cond.Op == "lt" && a < b)
	case "contains":
		return strings.Contains(strings.ToLower(have), strings.ToLower(cond.Value))
	}
	same := strings.EqualFold(have, cond.Value)
	if a, err := strconv.ParseFloat(have, 64); err == nil {
		if b, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(cond.Value, " ", ""), ",", "."), 64); err == nil {
			same = a == b
		}
	}
	return same == (cond.Op == "eq")
}

// robotFields sets the card's fields: the built-in ones as the card's form does, its own as «Поля CRM» do; the
// history shows the changes as anyone's.
func (s *Store) robotFields(ctx context.Context, actorID string, c card, sets []RobotSet, respName string) error {
	own := map[string]string{}
	builtin := map[string]string{}
	for _, f := range sets {
		v := c.fill(f.Value, respName)
		if id, ok := strings.CutPrefix(f.Field, "uf:"); ok {
			own[id] = v
		} else {
			builtin[f.Field] = v
		}
	}
	if len(builtin) > 0 {
		amount := func(old float64) (float64, error) {
			v, ok := builtin["amount"]
			if !ok {
				return old, nil
			}
			n, err := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(v, " ", ""), ",", "."), 64)
			if v == "" {
				n, err = 0, nil
			}
			if err != nil {
				return 0, i18n.E("err.crm_amount")
			}
			return n, nil
		}
		set := func(dst *string, key string) {
			if v, ok := builtin[key]; ok {
				*dst = v
			}
		}
		if c.entity == "deal" {
			d, err := s.Deal(ctx, c.id)
			if err != nil {
				return err
			}
			if d.Amount, err = amount(d.Amount); err != nil {
				return err
			}
			set(&d.Title, "title")
			set(&d.Comment, "comment")
			if v, ok := builtin["origin"]; ok {
				d.Origin = Dict{ID: v}
			}
			if v, ok := builtin["deal_type"]; ok {
				d.DealType = Dict{ID: v}
			}
			if _, err := s.SaveDeal(ctx, actorID, d); err != nil {
				return err
			}
		} else {
			l, err := s.Lead(ctx, c.id)
			if err != nil {
				return err
			}
			if l.Amount, err = amount(l.Amount); err != nil {
				return err
			}
			for key, dst := range map[string]*string{"title": &l.Title, "name": &l.Name, "company_title": &l.CompanyTitle,
				"phone": &l.Phone, "email": &l.Email, "source_info": &l.Source, "comment": &l.Comment} {
				set(dst, key)
			}
			if v, ok := builtin["origin"]; ok {
				l.Origin = Dict{ID: v}
			}
			if _, err := s.SaveLead(ctx, actorID, l); err != nil {
				return err
			}
		}
	}
	if len(own) > 0 {
		return s.SetFields(ctx, actorID, c.entity, c.id, own)
	}
	return nil
}

// RobotHook is what a webhook robot posts (JSON).
type RobotHook struct {
	Event       string            `json:"event"` // "crm.robot"
	Entity      string            `json:"entity"`
	ID          string            `json:"id"`
	TypeID      string            `json:"type_id,omitempty"` // a smart process's card
	Title       string            `json:"title"`
	Stage       string            `json:"stage_id"`
	Amount      float64           `json:"amount"`
	Currency    string            `json:"currency"`
	Responsible string            `json:"responsible_id"`
	Client      string            `json:"client,omitempty"`
	Link        string            `json:"link"`
	Fields      map[string]string `json:"fields"` // all the fields robots know (cardValues)
	At          time.Time         `json:"at"`
}

// robotWebhook posts the card to the address in the background (the kanban does not wait for it); the answer is
// written into the card's history: the address on success, the failure as any robot's.
func (s *Store) robotWebhook(ctx context.Context, actorID string, c card, target string) error {
	values, err := s.cardValues(ctx, c)
	if err != nil {
		return err
	}
	var typeID string
	if c.entity == "deal" {
		_ = s.db.QueryRow(ctx, `SELECT coalesce(type_id::text, '') FROM crm_deals WHERE id::text = $1`, c.id).Scan(&typeID)
	}
	body, err := json.Marshal(RobotHook{Event: "crm.robot", Entity: c.entity, ID: c.id, TypeID: typeID, Title: c.title, Stage: c.stage,
		Amount: c.amount, Currency: c.currency, Responsible: c.responsible, Client: c.client, Link: c.link, Fields: values, At: time.Now()})
	if err != nil {
		return err
	}
	host := target
	if u, err := url.Parse(target); err == nil {
		host = u.Host
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), webhookClient.Timeout)
		defer cancel()
		res := "webhook:" + host
		if err := postHook(ctx, target, body); err != nil {
			res = "fail:webhook:" + err.Error()
		}
		_ = s.event(ctx, s.db, c.entity, c.id, actorID, "robot", res)
	}()
	return nil
}

func postHook(ctx context.Context, target string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go24-robot")
	res, err := webhookClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", res.StatusCode)
	}
	return nil
}
