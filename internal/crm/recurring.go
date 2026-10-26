package crm

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Регулярная сделка»: a deal is repeated on schedule — every N days, weeks or months from a start date,
// until an end date or a number of copies. On the day the worker makes a copy: title, amount and currency, company
// and contacts, products, own fields, responsible, comment; in the first stage of the same pipeline. The timelines of
// both cards point to each other; the responsible gets a notification.

// Recurrence is the schedule of a deal.
type Recurrence struct {
	DealID      string
	Unit        string // day | week | month
	Every       int
	Start, Next time.Time
	Until       *time.Time
	Limit       int // 0: no limit
	Count       int // copies made
	CreatedBy   string
}

// RecurUnits are the steps of a schedule.
var RecurUnits = []string{"day", "week", "month"}

// nth is the day of the n-th copy (0 — the start); a month's day past its end is the last day of that month.
func (r Recurrence) nth(n int) time.Time {
	switch r.Unit {
	case "day":
		return r.Start.AddDate(0, 0, n*r.Every)
	case "week":
		return r.Start.AddDate(0, 0, 7*n*r.Every)
	}
	y, m, d := r.Start.Date()
	first := time.Date(y, m+time.Month(n*r.Every), 1, 0, 0, 0, 0, time.UTC)
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Recurrence of a deal: nil when it is not repeated.
func (s *Store) Recurrence(ctx context.Context, dealID string) (*Recurrence, error) {
	r := Recurrence{DealID: dealID}
	err := s.db.QueryRow(ctx, `SELECT unit, every, start_date, next_date, until_date, coalesce(limit_count, 0), created_count, created_by::text
		FROM crm_deal_recurrence WHERE deal_id::text = $1`, dealID).Scan(&r.Unit, &r.Every, &r.Start, &r.Next, &r.Until, &r.Limit, &r.Count, &r.CreatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &r, err
}

// SetRecurrence repeats a deal (or changes its schedule): the first copy is made on the start day (not before
// today); a changed schedule counts its copies anew.
func (s *Store) SetRecurrence(ctx context.Context, userID string, r Recurrence, today time.Time) error {
	if !validUnit(r.Unit) || r.Every < 1 || r.Every > 365 || r.Limit < 0 || r.Limit > 1000 {
		return i18n.E("err.crm_recur")
	}
	r.Start, today = dateOnly(r.Start), dateOnly(today)
	if r.Start.Before(today) {
		return i18n.E("err.crm_recur_start")
	}
	if r.Until != nil {
		if u := dateOnly(*r.Until); u.Before(r.Start) {
			return i18n.E("err.crm_recur_until")
		} else {
			r.Until = &u
		}
	}
	var limit *int
	if r.Limit > 0 {
		limit = &r.Limit
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO crm_deal_recurrence (deal_id, unit, every, start_date, next_date, until_date, limit_count, created_by)
		SELECT id, $2, $3, $4, $4, $5, $6, $7 FROM crm_deals WHERE id::text = $1
		ON CONFLICT (deal_id) DO UPDATE SET unit = $2, every = $3, start_date = $4, next_date = $4, until_date = $5, limit_count = $6,
			created_count = 0, created_by = $7`, r.DealID, r.Unit, r.Every, r.Start, r.Until, limit, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func validUnit(u string) bool {
	for _, x := range RecurUnits {
		if x == u {
			return true
		}
	}
	return false
}

// StopRecurrence: the deal is not repeated any more.
func (s *Store) StopRecurrence(ctx context.Context, dealID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_deal_recurrence WHERE deal_id::text = $1`, dealID)
	return err
}

// RunRecurringDue makes the copies whose day has come (today in the company's time zone) and moves the schedules
// on; a schedule that is over is removed. Returns the new deals.
func (s *Store) RunRecurringDue(ctx context.Context, now time.Time) (made []string, errs []error) {
	today := dateOnly(now)
	for range 1000 {
		var done bool
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			r := Recurrence{}
			err := tx.QueryRow(ctx, `SELECT deal_id::text, unit, every, start_date, next_date, until_date, coalesce(limit_count, 0), created_count,
					created_by::text FROM crm_deal_recurrence WHERE next_date <= $1 ORDER BY next_date LIMIT 1 FOR UPDATE SKIP LOCKED`, today).
				Scan(&r.DealID, &r.Unit, &r.Every, &r.Start, &r.Next, &r.Until, &r.Limit, &r.Count, &r.CreatedBy)
			if errors.Is(err, pgx.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if id, err := s.copyDeal(ctx, r); err != nil {
				errs = append(errs, fmt.Errorf("recurring deal %s: %w", r.DealID, err))
			} else {
				made = append(made, id)
			}
			// the next day after today (a schedule late for several steps makes one copy, not a pile)
			r.Count++
			n := r.Count
			for !r.nth(n).After(today) {
				n++
			}
			next := r.nth(n)
			if (r.Limit > 0 && r.Count >= r.Limit) || (r.Until != nil && next.After(*r.Until)) {
				_, err = tx.Exec(ctx, `DELETE FROM crm_deal_recurrence WHERE deal_id::text = $1`, r.DealID)
				return err
			}
			_, err = tx.Exec(ctx, `UPDATE crm_deal_recurrence SET next_date = $2, created_count = $3 WHERE deal_id::text = $1`, r.DealID, next, r.Count)
			return err
		})
		if err != nil {
			return made, append(errs, err)
		}
		if done {
			break
		}
	}
	return made, errs
}

// copyDeal makes the deal's copy as its schedule's author: in the first stage of its pipeline.
func (s *Store) copyDeal(ctx context.Context, r Recurrence) (string, error) {
	d, err := s.Deal(ctx, r.DealID)
	if err != nil {
		return "", err
	}
	id, err := s.cloneDeal(ctx, r.CreatedBy, d, d.PipelineID, "")
	if err != nil {
		return id, err
	}
	if err := s.event(ctx, s.db, "deal", id, r.CreatedBy, "recurred_from", d.ID+"|"+d.Title); err != nil {
		return id, err
	}
	if err := s.event(ctx, s.db, "deal", d.ID, r.CreatedBy, "recurred", id+"|"+d.Title); err != nil {
		return id, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_recurring', $3, $4)`,
		d.Responsible.ID, r.CreatedBy, d.Title, "/crm/deals/"+id)
	return id, err
}

// TunnelDeal makes the deal's copy in a stage of another pipeline of the same kind of cards («Туннель продаж»); the
// histories of both link each other.
func (s *Store) TunnelDeal(ctx context.Context, actorID, dealID, stageID string) (string, error) {
	d, err := s.Deal(ctx, dealID)
	if err != nil {
		return "", err
	}
	var pipeline string
	err = s.db.QueryRow(ctx, `SELECT p.id FROM crm_stages st JOIN crm_pipelines p ON p.id = st.pipeline_id
		WHERE st.id::text = $1 AND st.entity = 'deal' AND p.type_id IS NOT DISTINCT FROM $2`, stageID, optional(d.TypeID)).Scan(&pipeline)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", i18n.E("err.robot_tunnel")
	}
	if err != nil {
		return "", err
	}
	id, err := s.cloneDeal(ctx, actorID, d, pipeline, stageID)
	if err != nil {
		return id, err
	}
	if err := s.event(ctx, s.db, "deal", id, actorID, "tunnel_from", d.ID+"|"+d.Title); err != nil {
		return id, err
	}
	return id, s.event(ctx, s.db, "deal", d.ID, actorID, "tunnel_to", id+"|"+d.Title)
}

// cloneDeal saves a copy of the deal (client, contacts, products, own fields, responsible, observers) in a stage of a
// pipeline ("" — its first one); the stage's robots run on the copy.
func (s *Store) cloneDeal(ctx context.Context, actorID string, d Deal, pipelineID, stageID string) (string, error) {
	src := d.ID
	d.ID, d.Stage, d.CloseDate, d.PipelineID = "", Stage{ID: stageID}, nil, pipelineID
	rows, err := s.ProductRows(ctx, "deal", src)
	if err != nil {
		return "", err
	}
	id, err := s.SaveDeal(ctx, actorID, d)
	if err != nil {
		return "", err
	}
	if len(rows) > 0 {
		if _, err := s.SetProductRows(ctx, actorID, "deal", id, rows); err != nil {
			return id, err
		}
	}
	if s.Tasks != nil { // the administrator's own fields of deals
		set := tasks.CRMFieldSets["deal"]
		vals, err := s.Tasks.FieldValuesOf(ctx, set, src)
		if err != nil {
			return id, err
		}
		values := map[string]string{}
		for _, v := range vals {
			if v.Value != "" {
				values[v.ID] = v.Value
			}
		}
		if len(values) > 0 {
			if err := s.Tasks.SetFieldValuesOf(ctx, set, id, values); err != nil {
				return id, err
			}
		}
	}
	return id, nil
}
