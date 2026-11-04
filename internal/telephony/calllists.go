package telephony

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Обзвон» of the CRM: the cards chosen in a list of leads or deals are a call list of their clients' phones.
// The employee calls them in turn from the softphone; a call made after «Позвонить» to the item's number gives its
// result (answered or not) by the call log, the employee may set it themselves or skip the client.

// MaxCallItems are the clients of a call list at most.
const MaxCallItems = 1000

// Results of a call list's item.
const (
	ItemWait     = "wait"
	ItemAnswered = "answered"
	ItemNoAnswer = "noanswer"
	ItemSkipped  = "skipped"
)

// ItemStatuses are the results an employee may set.
var ItemStatuses = []string{ItemWait, ItemAnswered, ItemNoAnswer, ItemSkipped}

// CallList is a call list with its progress.
type CallList struct {
	ID, Title, Entity, Owner, OwnerName string
	Created                             time.Time
	Closed                              *time.Time
	Total, Done                         int // the clients, the ones with a result
}

// Left: the clients not called yet.
func (l CallList) Left() int { return l.Total - l.Done }

// CallItem is a client of a call list.
type CallItem struct {
	ID, EntityID, Title, Name, Phone, Status string
	Pos                                      int
	Dialed                                   *time.Time // «Позвонить» pressed (nil — not)
	CallID                                   string     // the call of the log that gave the result ("" — none)
	Seconds                                  int        // its talk
}

// Dial is the number to dial ("" — none can be).
func (it CallItem) Dial() string { return Dialable(it.Phone) }

// CallLead is a client to put on a call list.
type CallLead struct {
	EntityID, Name, Phone string
}

const callListCols = `l.id::text, l.title, l.entity, l.owner::text, coalesce(CASE WHEN u.name <> '' THEN u.name ELSE u.email END, ''),
	l.created_at, l.closed_at, count(i.id)::int, count(i.id) FILTER (WHERE i.status <> 'wait')::int
	FROM telephony_calllists l LEFT JOIN users u ON u.id = l.owner LEFT JOIN telephony_calllist_items i ON i.list_id = l.id`

// MakeCallList makes the owner's call list of the cards of the entity (lead, deal) in their order; the title of
// each card is taken from it.
func (s *Store) MakeCallList(ctx context.Context, owner, title, entity string, items []CallLead) (string, error) {
	if entity != "lead" && entity != "deal" {
		return "", i18n.E("err.bad_form")
	}
	if len(items) == 0 {
		return "", i18n.E("tel.cl_err_empty")
	}
	if len(items) > MaxCallItems {
		return "", i18n.E("tel.cl_err_many", MaxCallItems)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO telephony_calllists (title, entity, owner) VALUES ($1, $2, $3) RETURNING id::text`,
		title, entity, owner).Scan(&id); err != nil {
		return "", err
	}
	for i, it := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO telephony_calllist_items (list_id, pos, entity_id, title, name, phone)
			VALUES ($1, $2, $3::uuid, coalesce(CASE $4 WHEN 'deal' THEN (SELECT title FROM crm_deals WHERE id = $3::uuid)
				ELSE (SELECT title FROM crm_leads WHERE id = $3::uuid) END, ''), $5, $6)`,
			id, i+1, it.EntityID, entity, it.Name, it.Phone); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

// CallLists are the owner's call lists ("" — everyone's), the open ones first, the newest first.
func (s *Store) CallLists(ctx context.Context, owner string) ([]CallList, error) {
	rows, err := s.db.Query(ctx, `SELECT `+callListCols+` WHERE $1 = '' OR l.owner::text = $1
		GROUP BY l.id, u.name, u.email ORDER BY l.closed_at IS NOT NULL, l.created_at DESC LIMIT 200`, owner)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CallList])
}

// CallList is a call list with its clients in order; the results of the calls made after «Позвонить» are taken from
// the call log first. ErrNotFound — none.
func (s *Store) CallList(ctx context.Context, id string) (CallList, []CallItem, error) {
	if err := s.resolveItems(ctx, id); err != nil {
		return CallList{}, nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT `+callListCols+` WHERE l.id::text = $1 GROUP BY l.id, u.name, u.email`, id)
	if err != nil {
		return CallList{}, nil, err
	}
	l, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[CallList])
	if errors.Is(err, pgx.ErrNoRows) {
		return l, nil, ErrNotFound
	}
	if err != nil {
		return l, nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT i.id::text, i.entity_id::text, i.title, i.name, i.phone, i.status, i.pos, i.dialed_at,
			coalesce(i.call_id::text, ''), coalesce(c.seconds, 0)
		FROM telephony_calllist_items i LEFT JOIN telephony_calls c ON c.id = i.call_id WHERE i.list_id::text = $1 ORDER BY i.pos`, id)
	if err != nil {
		return l, nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CallItem])
	return l, items, err
}

// resolveItems gives the clients dialed and waiting their call's result: the owner's first call out to the number
// since a minute before «Позвонить» (the last 10 digits match).
func (s *Store) resolveItems(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `WITH r AS (
			SELECT i.id, c.id AS call, c.status FROM telephony_calllist_items i JOIN telephony_calllists l ON l.id = i.list_id
			CROSS JOIN LATERAL (SELECT c.id, c.status FROM telephony_calls c
				WHERE c.from_user = l.owner AND c.to_user IS NULL AND c.started_at >= i.dialed_at - interval '1 minute'
				  AND right(regexp_replace(c.to_number, '\D', '', 'g'), 10) = right(regexp_replace(i.phone, '\D', '', 'g'), 10)
				ORDER BY c.started_at LIMIT 1) c
			WHERE i.list_id::text = $1 AND i.status = 'wait' AND i.dialed_at IS NOT NULL)
		UPDATE telephony_calllist_items i SET status = CASE WHEN r.status = 'answered' THEN 'answered' ELSE 'noanswer' END, call_id = r.call
		FROM r WHERE i.id = r.id`, id)
	return err
}

// Current is the client to call: the first waiting one; -1 — none is left.
func Current(items []CallItem) int {
	return slices.IndexFunc(items, func(it CallItem) bool { return it.Status == ItemWait })
}

// DialItem: «Позвонить» — the next call out to the client's number gives the result.
func (s *Store) DialItem(ctx context.Context, listID, itemID string) error {
	tag, err := s.db.Exec(ctx, `UPDATE telephony_calllist_items SET dialed_at = now(), status = 'wait', call_id = NULL
		WHERE list_id::text = $1 AND id::text = $2`, listID, itemID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetItemStatus sets the client's result by hand (wait — to call again).
func (s *Store) SetItemStatus(ctx context.Context, listID, itemID, status string) error {
	if !slices.Contains(ItemStatuses, status) {
		return i18n.E("err.bad_form")
	}
	tag, err := s.db.Exec(ctx, `UPDATE telephony_calllist_items SET status = $3,
			dialed_at = CASE WHEN $3 = 'wait' THEN NULL ELSE dialed_at END, call_id = CASE WHEN $3 = 'wait' THEN NULL ELSE call_id END
		WHERE list_id::text = $1 AND id::text = $2`, listID, itemID, status)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// CloseCallList: «Завершить обзвон» (open false — resume it).
func (s *Store) CloseCallList(ctx context.Context, id string, closed bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE telephony_calllists SET closed_at = CASE WHEN $2 THEN coalesce(closed_at, now()) END
		WHERE id::text = $1`, id, closed)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// DeleteCallList deletes the call list (the calls stay in the log).
func (s *Store) DeleteCallList(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_calllists WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
