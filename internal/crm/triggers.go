package crm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Триггеры»: an event of the client moves the card on by itself — a letter from the client, the site's
// form filled again, a message in the open line, a deed done, an invoice paid, a quote accepted, a booking of the
// deal made, confirmed, the client come or not, the booking cancelled or moved by the client («Онлайн-запись»), the client's call or a call
// of theirs missed, fields of the card changed («Изменение полей»), an outside service's call («Webhook»), the
// client viewed or signed the card's document, a task linked to the card changed its status, the client followed a
// link of a robot's letter («Переход по ссылке», linktrigger.go). A trigger
// stands on the stage the card goes to; the card only goes forward (a card already further, or closed, stays) unless
// the trigger allows going back («Разрешить переход на предыдущую стадию»); a trigger with «Условие» moves only a card
// that meets it; a converted lead's events move its deal, a contact's or a company's calls move their open deals.

// TriggerKinds in the order the settings offer them.
var TriggerKinds = []string{"mail", "form", "openline", "activity", "invoice_paid", "quote_accepted",
	"booking", "booking_confirmed", "booking_came", "booking_no_show", "booking_cancelled", "booking_moved",
	"call", "call_missed", "field", "webhook", "doc_viewed", "doc_signed", "task", "link"}

// Trigger of a stage.
type Trigger struct {
	ID, StageID, Kind string
	Params            TriggerParams
}

// TriggerParams: the condition and «back» of any trigger, the fields of «Изменение полей», the code of «Webhook», the
// status of «Изменение статуса задачи», the address of «Переход по ссылке».
type TriggerParams struct {
	If     *RobotCond `json:"if,omitempty"`
	Back   bool       `json:"back,omitempty"`
	Fields []string   `json:"fields,omitempty"` // "field": the built-in fields (RobotFieldKeys) and own ones («uf:» and its id)
	Code   string     `json:"code,omitempty"`   // "webhook": what the outside service names the trigger by
	Status string     `json:"status,omitempty"` // "task": the status the task came to ("" — any change)
	URL    string     `json:"url,omitempty"`    // "link": the address the robots' letters track
}

// maxTriggerFields is how many fields one «Изменение полей» may watch.
const maxTriggerFields = 20

// Triggers are the triggers of the stages given.
func (s *Store) Triggers(ctx context.Context, stageIDs []string) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, stage_id::text, kind, params FROM crm_triggers WHERE stage_id::text = ANY($1) ORDER BY created_at`, stageIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Trigger, error) {
		var t Trigger
		var raw []byte
		if err := r.Scan(&t.ID, &t.StageID, &t.Kind, &raw); err != nil {
			return t, err
		}
		return t, json.Unmarshal(raw, &t.Params)
	})
}

// AddTrigger sets a trigger on a stage (administrators; checked by the web layer); one of a kind per pipeline's stage
// (another one of the kind is let be). A webhook trigger gets its code here.
func (s *Store) AddTrigger(ctx context.Context, userID, stageID, kind string, p TriggerParams) error {
	if !slices.Contains(TriggerKinds, kind) {
		return i18n.E("err.bad_form")
	}
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
	fields := []string{}
	if kind == "field" {
		for _, f := range p.Fields {
			if f = strings.TrimSpace(f); f == "" || slices.Contains(fields, f) {
				continue
			}
			if !s.checkRobotField(ctx, entity, f) {
				return i18n.E("err.trigger_fields")
			}
			fields = append(fields, f)
		}
		if len(fields) == 0 || len(fields) > maxTriggerFields {
			return i18n.E("err.trigger_fields")
		}
	}
	p.Fields, p.Code = fields, ""
	if kind != "task" {
		p.Status = ""
	} else if p.Status != "" && !slices.Contains(tasks.AllStatuses, tasks.Status(p.Status)) {
		return i18n.E("err.bad_form")
	}
	if kind != "link" {
		p.URL = ""
	} else if p.URL = linkURL(p.URL); p.URL == "" {
		return i18n.E("err.trigger_link")
	}
	if len(p.Fields) == 0 {
		p.Fields = nil
	}
	if kind == "webhook" {
		p.Code = formToken()
	}
	raw, _ := json.Marshal(p)
	_, err := s.db.Exec(ctx, `INSERT INTO crm_triggers (stage_id, kind, params, created_by) SELECT id, $2, $3, $4 FROM crm_stages WHERE id::text = $1
		ON CONFLICT DO NOTHING`, stageID, kind, raw, userID)
	return err
}

func (s *Store) DeleteTrigger(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_triggers WHERE id::text = $1`, id)
	return err
}

// Fire tells the card's triggers of an event: the card goes to the furthest stage ahead of it with a trigger of
// the kind whose condition it meets — or, when none is ahead, to the nearest one behind that allows going back (the
// card's responsible moves it; the history says which trigger). Other cards than deals and leads have no stages.
func (s *Store) Fire(ctx context.Context, entity, id, kind string) error {
	return s.fire(ctx, entity, id, kind, nil)
}

// fire is Fire with the trigger's own check of its params (nil — none).
func (s *Store) fire(ctx context.Context, entity, id, kind string, match func(TriggerParams) bool) error {
	var cur Stage
	var pipeline, responsible string
	switch entity {
	case "deal":
		d, err := s.Deal(ctx, id)
		if err != nil || d.ClosedAt != nil {
			return err
		}
		cur, pipeline, responsible = d.Stage, d.PipelineID, d.Responsible.ID
	case "lead":
		l, err := s.Lead(ctx, id)
		if err != nil {
			return err
		}
		if l.DealID != "" {
			return s.fire(ctx, "deal", l.DealID, kind, match)
		}
		if l.Stage.Closed() {
			return nil
		}
		cur, responsible = l.Stage, l.Responsible.ID
	default:
		return nil
	}
	type cand struct {
		stage  string
		ahead  bool
		params TriggerParams
	}
	rows, err := s.db.Query(ctx, `SELECT st.id::text, st.sort > cs.sort, t.params FROM crm_triggers t
		JOIN crm_stages st ON st.id = t.stage_id JOIN crm_stages cs ON cs.id::text = $4
		WHERE t.kind = $1 AND st.entity = $2 AND st.pipeline_id IS NOT DISTINCT FROM nullif($3, '')::uuid AND st.id <> cs.id
		ORDER BY st.sort > cs.sort DESC, st.sort DESC`, kind, entity, pipeline, cur.ID)
	if err != nil {
		return err
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		var raw []byte
		if err := r.Scan(&c.stage, &c.ahead, &raw); err != nil {
			return c, err
		}
		return c, json.Unmarshal(raw, &c.params)
	})
	if err != nil {
		return err
	}
	var values map[string]string
	target := ""
	for _, c := range cands {
		if (!c.ahead && !c.params.Back) || (match != nil && !match(c.params)) {
			continue
		}
		if c.params.If != nil {
			if values == nil {
				if values, err = s.cardValues(ctx, card{entity: entity, id: id}); err != nil {
					return err
				}
			}
			if !robotCondOK(*c.params.If, values) {
				continue
			}
		}
		target = c.stage
		break
	}
	if target == "" {
		return nil
	}
	if err := s.event(ctx, s.db, entity, id, responsible, "robot", "trigger:"+kind); err != nil {
		return err
	}
	if entity == "deal" {
		_, err = s.MoveDeal(ctx, responsible, id, target)
	} else {
		_, err = s.MoveLead(ctx, responsible, id, target)
	}
	return err
}

// FireCall tells the triggers of a client's call («Входящий звонок», or missed — «Пропущенный звонок»): a lead's
// moves it (or its deal), a contact's or a company's moves their open deals.
func (s *Store) FireCall(ctx context.Context, entity, id string, missed bool) error {
	kind := "call"
	if missed {
		kind = "call_missed"
	}
	switch entity {
	case "deal", "lead":
		return s.Fire(ctx, entity, id, kind)
	case "contact", "company":
	default:
		return nil
	}
	col := map[string]string{"contact": "contact_id", "company": "company_id"}[entity]
	rows, err := s.db.Query(ctx, `SELECT d.id::text FROM crm_deals d WHERE d.closed_at IS NULL AND (d.`+col+`::text = $1`+
		map[string]string{"contact": ` OR EXISTS (SELECT 1 FROM crm_deal_contacts dc WHERE dc.deal_id = d.id AND dc.contact_id::text = $1)`}[entity]+
		`) ORDER BY d.created_at`, id)
	if err != nil {
		return err
	}
	deals, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, d := range deals {
		if err := s.Fire(ctx, "deal", d, kind); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// CardSnapshot is the card's fields as «Изменение полей» compares them before and after a change (nil — not a deal
// or a lead).
func (s *Store) CardSnapshot(ctx context.Context, entity, id string) (map[string]string, error) {
	if entity != "deal" && entity != "lead" {
		return nil, nil
	}
	return s.cardValues(ctx, card{entity: entity, id: id})
}

// FireChanged tells «Изменение полей» which fields of the card changed since the snapshot before: a trigger fires
// when one of its fields is among them.
func (s *Store) FireChanged(ctx context.Context, entity, id string, before map[string]string) error {
	if before == nil {
		return nil
	}
	after, err := s.CardSnapshot(ctx, entity, id)
	if err != nil {
		return err
	}
	changed := map[string]bool{}
	for k, v := range after {
		if strings.TrimSpace(before[k]) != strings.TrimSpace(v) {
			changed[k] = true
		}
	}
	for k, v := range before {
		if _, ok := after[k]; !ok && strings.TrimSpace(v) != "" {
			changed[k] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}
	return s.fire(ctx, entity, id, "field", func(p TriggerParams) bool {
		return slices.ContainsFunc(p.Fields, func(f string) bool { return changed[f] })
	})
}

// FireHook is «Webhook»: an outside service names the trigger by its code for the card (crm.automation.trigger of
// the portal); an unknown code — ErrNotFound.
func (s *Store) FireHook(ctx context.Context, entity, id, code string) error {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM crm_triggers t JOIN crm_stages st ON st.id = t.stage_id
		WHERE t.kind = 'webhook' AND t.params->>'code' = $1 AND st.entity = $2`, code, entity).Scan(&n); err != nil {
		return err
	}
	if code == "" || n == 0 {
		return ErrNotFound
	}
	return s.fire(ctx, entity, id, "webhook", func(p TriggerParams) bool { return p.Code == code })
}

// RunTriggersDue fires the mail triggers of the deals and leads that got a client's letter since the last run (the
// mail service puts letters into cards; this looks at what it put), then the task triggers (runTaskTriggers).
func (s *Store) RunTriggersDue(ctx context.Context, now time.Time) (int, error) {
	n, err := s.runMailTriggers(ctx, now)
	if err != nil {
		return n, err
	}
	m, err := s.runTaskTriggers(ctx)
	return n + m, err
}

func (s *Store) runMailTriggers(ctx context.Context, now time.Time) (int, error) {
	var raw []byte
	var last time.Time
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'crm_trigger_mail'`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &last)
	}
	until := now.Add(-5 * time.Second) // letters still being put in come next time
	if errors.Is(err, pgx.ErrNoRows) {
		last, err = until, nil // the first run: from now on
	}
	if err != nil {
		return 0, err
	}
	type hit struct {
		entity, id string
		at         time.Time
	}
	rows, err := s.db.Query(ctx, `SELECT entity, entity_id::text, max(created_at) FROM crm_mail
		WHERE NOT outgoing AND entity IN ('deal', 'lead') AND created_at > $1 AND created_at <= $2 GROUP BY 1, 2`, last, until)
	if err != nil {
		return 0, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
		var h hit
		return h, r.Scan(&h.entity, &h.id, &h.at)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, h := range hits {
		if err := s.Fire(ctx, h.entity, h.id, "mail"); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	raw, _ = json.Marshal(until)
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value) VALUES ('crm_trigger_mail', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, raw)
	return n, err
}

// runTaskTriggers fires «Изменение статуса задачи» of the deals and leads of go24's CRM linked to tasks («Привязка к
// CRM») whose status changed since the last run, in the order of the changes: the trigger with a status moves the
// card only when the task came to it. The tasks' history is read from where the last run stopped.
func (s *Store) runTaskTriggers(ctx context.Context) (int, error) {
	var raw []byte
	var last int64
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = 'crm_trigger_task'`).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &last)
	}
	if errors.Is(err, pgx.ErrNoRows) { // the first run: from now on
		if err = s.db.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM task_history`).Scan(&last); err == nil {
			raw, _ = json.Marshal(last)
			_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value) VALUES ('crm_trigger_task', $1)
				ON CONFLICT (key) DO NOTHING`, raw)
		}
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	var upto int64 // changes still being written come next time
	if err := s.db.QueryRow(ctx, `SELECT coalesce(max(id), $1) FROM task_history WHERE id > $1 AND created_at <= now() - interval '5 seconds'`,
		last).Scan(&upto); err != nil || upto == last {
		return 0, err
	}
	type hit struct {
		hid            int64
		entity, id, to string
	}
	rows, err := s.db.Query(ctx, `SELECT h.id, l.url, h.new_value FROM task_history h JOIN task_crm_links l ON l.task_id = h.task_id
		WHERE h.field = 'status' AND h.id > $1 AND h.id <= $2 AND h.old_value <> h.new_value AND (l.url LIKE '/crm/deals/%' OR l.url LIKE '/crm/leads/%')
		ORDER BY h.id, l.created_at LIMIT 500`, last, upto)
	if err != nil {
		return 0, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (hit, error) {
		var h hit
		var url string
		err := r.Scan(&h.hid, &url, &h.to)
		if parts := strings.Split(url, "/"); len(parts) == 4 { // /crm/deals/<id>
			h.entity, h.id = strings.TrimSuffix(parts[2], "s"), parts[3]
		}
		return h, err
	})
	if err != nil {
		return 0, err
	}
	if len(hits) == 500 && hits[len(hits)-1].hid > hits[0].hid {
		upto = hits[len(hits)-1].hid - 1 // the last change may have more cards: next time whole
	}
	n := 0
	for _, h := range hits {
		if h.hid > upto {
			break
		}
		to := h.to
		if err := s.fire(ctx, h.entity, h.id, "task", func(p TriggerParams) bool { return p.Status == "" || p.Status == to }); err != nil && !errors.Is(err, ErrNotFound) {
			return n, err
		}
		n++
	}
	raw, _ = json.Marshal(upto)
	_, err = s.db.Exec(ctx, `UPDATE company_settings SET value = $1, updated_at = now() WHERE key = 'crm_trigger_task'`, raw)
	return n, err
}
