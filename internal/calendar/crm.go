package calendar

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// ---------- «CRM» of a meeting: the deals, leads, contacts and companies it is about ----------

// CRMKinds are the cards a meeting is linked to.
var CRMKinds = []string{"deal", "lead", "contact", "company"}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Valid: a card of a known kind with an id that may be one.
func (r CRMRef) Valid() bool { return slices.Contains(CRMKinds, r.Entity) && uuidRe.MatchString(r.ID) }

// CRMRef is a CRM card of a meeting.
type CRMRef struct {
	Entity, ID string
	Title      string // the card's title or name
}

// Path is the card's page.
func (r CRMRef) Path() string {
	return "/crm/" + map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}[r.Entity] + "/" + r.ID
}

// crmTitle is the title of the card the row of event_crm points to (NULL — the card is gone, e.g. in the recycle bin).
const crmTitle = `CASE c.entity WHEN 'deal' THEN (SELECT title FROM crm_deals WHERE id = c.entity_id)
		WHEN 'lead' THEN (SELECT title FROM crm_leads WHERE id = c.entity_id)
		WHEN 'contact' THEN (SELECT name FROM crm_contacts WHERE id = c.entity_id)
		WHEN 'company' THEN (SELECT title FROM crm_companies WHERE id = c.entity_id) END`

// CRM are the cards of the meeting, in the order they were linked.
func (s *Store) CRM(ctx context.Context, eventID string) ([]CRMRef, error) {
	rows, err := s.db.Query(ctx, `SELECT entity, entity_id::text, title FROM (
		SELECT c.entity, c.entity_id, `+crmTitle+` AS title, c.created_at FROM event_crm c WHERE c.event_id::text = $1) x
		WHERE title IS NOT NULL ORDER BY created_at, entity, entity_id`, eventID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CRMRef])
}

// SetCRM links the organizer's meeting to exactly these cards (the web layer checks the person may see them). A card
// already linked that is not shown now (in the recycle bin) stays linked.
func (s *Store) SetCRM(ctx context.Context, viewer, eventID string, refs []CRMRef) error {
	var owner string
	err := s.db.QueryRow(ctx, `SELECT owner_id::text FROM events WHERE id::text = $1`, eventID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != viewer && !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	var kinds, ids []string
	for _, r := range refs {
		if r.Valid() {
			kinds, ids = append(kinds, r.Entity), append(ids, r.ID)
		}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM event_crm c WHERE c.event_id::text = $1 AND `+crmTitle+` IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM unnest($2::text[], $3::uuid[]) w(entity, id) WHERE w.entity = c.entity AND w.id = c.entity_id)`,
			eventID, kinds, ids); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO event_crm (event_id, entity, entity_id)
			SELECT $1::uuid, w.entity, w.id FROM unnest($2::text[], $3::uuid[]) w(entity, id) ON CONFLICT DO NOTHING`, eventID, kinds, ids)
		return err
	})
}

// OfCard are the meetings of a CRM card, the newest first: the card's people see them (a private one as «Занят»),
// a repeated one by its next occurrence (or its last).
func (s *Store) OfCard(ctx context.Context, viewer, entity, id string) ([]Event, error) {
	rows, err := s.db.Query(ctx, selectSQL+` WHERE EXISTS (SELECT 1 FROM event_crm c
		WHERE c.event_id = e.id AND c.entity = $1 AND c.entity_id::text = $2) ORDER BY e.starts_at DESC LIMIT 100`, entity, id)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i, e := range list {
		if e.Repeat == "" {
			continue
		}
		next := e.expand(now, now.AddDate(2, 0, 0))
		if len(next) > 0 {
			list[i] = next[0]
		}
	}
	return list, nil
}
