package crm

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Триггеры»: an event of the client moves the card on by itself — a letter from the client, the site's
// form filled again, a message in the open line, a deed done, an invoice paid, a quote accepted, a booking of the
// deal made, confirmed, the client come or not, the booking cancelled («Онлайн-запись»). A trigger stands on
// the stage the card goes to; the card only goes forward (a card already further, or closed, stays), and a converted
// lead's events move its deal.

// TriggerKinds in the order the settings offer them.
var TriggerKinds = []string{"mail", "form", "openline", "activity", "invoice_paid", "quote_accepted",
	"booking", "booking_confirmed", "booking_came", "booking_no_show", "booking_cancelled"}

// Trigger of a stage.
type Trigger struct {
	ID, StageID, Kind string
}

// Triggers are the triggers of the stages given.
func (s *Store) Triggers(ctx context.Context, stageIDs []string) ([]Trigger, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, stage_id::text, kind FROM crm_triggers WHERE stage_id::text = ANY($1) ORDER BY created_at`, stageIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Trigger])
}

// AddTrigger sets a trigger on a stage (administrators; checked by the web layer); one of a kind per pipeline's stage.
func (s *Store) AddTrigger(ctx context.Context, userID, stageID, kind string) error {
	if !slices.Contains(TriggerKinds, kind) {
		return i18n.E("err.bad_form")
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO crm_triggers (stage_id, kind, created_by) SELECT id, $2, $3 FROM crm_stages WHERE id::text = $1
		ON CONFLICT DO NOTHING`, stageID, kind, userID)
	if err == nil && tag.RowsAffected() == 0 {
		var n int
		if s.db.QueryRow(ctx, `SELECT count(*) FROM crm_stages WHERE id::text = $1`, stageID).Scan(&n); n == 0 {
			err = i18n.E("err.crm_stage")
		}
	}
	return err
}

func (s *Store) DeleteTrigger(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_triggers WHERE id::text = $1`, id)
	return err
}

// Fire tells the card's triggers of an event: the card goes to the furthest stage ahead of it with a trigger of
// the kind (the card's responsible moves it; the history says which trigger). Other cards than deals and leads have
// no stages.
func (s *Store) Fire(ctx context.Context, entity, id, kind string) error {
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
			return s.Fire(ctx, "deal", l.DealID, kind)
		}
		if l.Stage.Closed() {
			return nil
		}
		cur, responsible = l.Stage, l.Responsible.ID
	default:
		return nil
	}
	var target string
	err := s.db.QueryRow(ctx, `SELECT st.id::text FROM crm_triggers t JOIN crm_stages st ON st.id = t.stage_id
		WHERE t.kind = $1 AND st.entity = $2 AND st.pipeline_id IS NOT DISTINCT FROM nullif($3, '')::uuid
			AND st.sort > (SELECT sort FROM crm_stages WHERE id::text = $4)
		ORDER BY st.sort DESC LIMIT 1`, kind, entity, pipeline, cur.ID).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
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

// RunTriggersDue fires the mail triggers of the deals and leads that got a client's letter since the last run (the
// mail service puts letters into cards; this looks at what it put).
func (s *Store) RunTriggersDue(ctx context.Context, now time.Time) (int, error) {
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
