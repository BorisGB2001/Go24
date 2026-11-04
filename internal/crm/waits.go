package crm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Ожидание» of the portal timeline: «Ждать 3 дня после текущей даты» or «… до даты завершения», with a comment.
// A card waits once at a time: the open wait is shown above its timeline and on its kanban tile; it ends by
// «Завершить», by a new wait, or by itself on its day (go24 worker), when the card's responsible is told.

// WaitUnits of «Ждать N …» in the order of the form.
var WaitUnits = []string{"day", "week"}

// MaxWait is the longest wait in days.
const MaxWait = 3650

// Wait is a card's waiting.
type Wait struct {
	ID, Entity, EntityID string
	Until                time.Time
	Description          string
	By                   Ref // who set it
	CreatedAt            time.Time
}

// WaitUntil is the end of «Ждать n unit» after now (anchor nil) or before *anchor; a day before a past date is an error.
func WaitUntil(n int, unit string, anchor *time.Time, now time.Time) (time.Time, error) {
	days := n
	if unit == "week" {
		days = n * 7
	}
	if n < 1 || days > MaxWait {
		return time.Time{}, i18n.E("err.crm_wait_length")
	}
	if anchor == nil {
		return now.AddDate(0, 0, days), nil
	}
	until := anchor.AddDate(0, 0, -days)
	if !until.After(now) {
		return time.Time{}, i18n.E("err.crm_wait_past")
	}
	return until, nil
}

const waitSQL = `SELECT w.id::text, w.entity, w.entity_id::text, w.until, w.description, w.created_by::text, u.name, w.created_at
	FROM crm_waits w JOIN users u ON u.id = w.created_by WHERE w.done_at IS NULL`

func scanWaits(rows pgx.Rows, err error) ([]Wait, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Wait, error) {
		var w Wait
		err := r.Scan(&w.ID, &w.Entity, &w.EntityID, &w.Until, &w.Description, &w.By.ID, &w.By.Name, &w.CreatedAt)
		return w, err
	})
}

// waitBody is the timeline line of a wait: its end and the comment.
func waitBody(until time.Time, description string) string {
	return until.UTC().Format(time.RFC3339) + "|" + description
}

// WaitAt is the end of the wait of a timeline line wait, wait_done or wait_over.
func (e Event) WaitAt() time.Time {
	at, _, _ := strings.Cut(e.Body, "|")
	t, _ := time.Parse(time.RFC3339, at)
	return t
}

// WaitNote is the comment of the wait of a timeline line wait, wait_done or wait_over.
func (e Event) WaitNote() string {
	_, text, _ := strings.Cut(e.Body, "|")
	return text
}

// AddWait makes a card wait until w.Until; the card's open wait, if any, ends.
func (s *Store) AddWait(ctx context.Context, userID string, w Wait) (string, error) {
	if err := clean(&w.Description, 10000); err != nil {
		return "", err
	}
	if !w.Until.After(time.Now()) {
		return "", i18n.E("err.crm_wait_past")
	}
	if plural[w.Entity] == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.endWait(ctx, tx, userID, w.Entity, w.EntityID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `INSERT INTO crm_waits (entity, entity_id, until, description, created_by)
			SELECT $1, id, $3, $4, $5 FROM crm_`+plural[w.Entity]+` WHERE id::text = $2 RETURNING id::text`,
			w.Entity, w.EntityID, w.Until, w.Description, userID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		return s.event(ctx, tx, w.Entity, w.EntityID, userID, "wait", waitBody(w.Until, w.Description))
	})
	return id, err
}

// endWait ends the card's open wait by hand: the timeline tells «Ожидание завершено».
func (s *Store) endWait(ctx context.Context, tx pgx.Tx, userID, entity, id string) error {
	var until time.Time
	var text string
	err := tx.QueryRow(ctx, `UPDATE crm_waits SET done_at = now(), done_by = $3 WHERE entity = $1 AND entity_id::text = $2
		AND done_at IS NULL RETURNING until, description`, entity, id, userID).Scan(&until, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.event(ctx, tx, entity, id, userID, "wait_done", waitBody(until, text))
}

// EndWait ends a card's open wait («Завершить»); no wait — nothing.
func (s *Store) EndWait(ctx context.Context, userID, entity, id string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error { return s.endWait(ctx, tx, userID, entity, id) })
}

// WaitOf is a card's open wait, nil — none.
func (s *Store) WaitOf(ctx context.Context, entity, id string) (*Wait, error) {
	list, err := scanWaits(s.db.Query(ctx, waitSQL+` AND w.entity = $1 AND w.entity_id::text = $2`, entity, id))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// WaitsOf are the open waits of cards (the kanban's tiles) by card.
func (s *Store) WaitsOf(ctx context.Context, entity string, ids []string) (map[string]*Wait, error) {
	list, err := scanWaits(s.db.Query(ctx, waitSQL+` AND w.entity = $1 AND w.entity_id::text = ANY($2)`, entity, ids))
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Wait, len(list))
	for i := range list {
		out[list[i].EntityID] = &list[i]
	}
	return out, nil
}

// EndWaitsDue ends the waits whose day has come (go24 worker): the timeline tells «Ожидание закончилось» and the
// card's responsible gets a notification.
func (s *Store) EndWaitsDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.Query(ctx, `UPDATE crm_waits SET done_at = $1 WHERE done_at IS NULL AND until <= $1
		RETURNING entity, entity_id::text, until, description, created_by::text`, now)
	if err != nil {
		return 0, err
	}
	type due struct {
		entity, id, text, by string
		until                time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.entity, &d.id, &d.until, &d.text, &d.by)
	})
	if err != nil {
		return 0, err
	}
	for _, d := range list {
		if err := s.event(ctx, s.db, d.entity, d.id, d.by, "wait_over", waitBody(d.until, d.text)); err != nil {
			return 0, err
		}
		title := `title`
		if d.entity == "contact" {
			title = `name`
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
			SELECT responsible_id, 'crm_wait_over', `+title+`, $2 FROM crm_`+plural[d.entity]+` WHERE id::text = $1`,
			d.id, "/crm/"+plural[d.entity]+"/"+d.id); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
