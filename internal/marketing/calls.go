package marketing

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Обзвон» as in «Маркетинг»: a list of calls to the cards of a CRM segment that have a phone. Started, it
// fixes its cards and plans a «Звонок» deed in each — with the call's script, by its deadline — for the card's
// responsible or the chosen employees in turn; each of them is told once. Its progress is the deeds done.

// MaxCalls are the cards of a call list at most.
const MaxCalls = 5000

// CallList is a list of calls with its progress.
type CallList struct {
	ID, Title, SegmentID, SegmentName, Script string
	Deadline                                  *time.Time
	Assign                                    string   // owner — the cards' responsible; staff — Staff in turn
	Staff                                     []string // the employees' ids
	Status                                    string   // draft, started
	CreatedBy, AuthorName                     string
	StartedAt                                 *time.Time
	UpdatedAt                                 time.Time
	Total, Done                               int
}

// Editable: a draft, not started yet.
func (c CallList) Editable() bool { return c.Status == "draft" }

// Left: the calls not made yet.
func (c CallList) Left() int { return c.Total - c.Done }

const callCols = `c.id::text, c.title, coalesce(c.segment_id::text, ''), coalesce(g.name, ''), c.script, c.deadline, c.assign,
	coalesce((SELECT array_agg(x::text) FROM unnest(c.staff) x), '{}'), c.status, coalesce(c.created_by::text, ''), coalesce(u.name, ''),
	c.started_at, c.updated_at, count(i.id)::int, count(a.done_at)::int
	FROM mkt_calls c LEFT JOIN mkt_segments g ON g.id = c.segment_id LEFT JOIN users u ON u.id = c.created_by
	LEFT JOIN mkt_call_items i ON i.call_id = c.id LEFT JOIN crm_activities a ON a.id = i.activity_id`

// CallLists are the company's call lists, the latest first.
func (s *Store) CallLists(ctx context.Context) ([]CallList, error) {
	rows, err := s.db.Query(ctx, `SELECT `+callCols+` GROUP BY c.id, g.name, u.name ORDER BY c.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[CallList])
}

// CallList is one of them.
func (s *Store) CallList(ctx context.Context, id string) (CallList, error) {
	rows, err := s.db.Query(ctx, `SELECT `+callCols+` WHERE c.id::text = $1 GROUP BY c.id, g.name, u.name`, id)
	if err != nil {
		return CallList{}, err
	}
	c, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[CallList])
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	return c, err
}

func (s *Store) mayChangeCalls(ctx context.Context, userID string, admin bool, id string) (CallList, error) {
	c, err := s.CallList(ctx, id)
	if err == nil && !admin && c.CreatedBy != userID {
		err = ErrForbidden
	}
	return c, err
}

// SaveCallList adds a call list (c.ID "") or changes a draft one: the author's or, by an administrator, any.
func (s *Store) SaveCallList(ctx context.Context, userID string, admin bool, c CallList) (string, error) {
	c.Title, c.Script = strings.TrimSpace(c.Title), strings.TrimSpace(c.Script)
	if c.Assign != "staff" {
		c.Assign = "owner"
	}
	if c.Title == "" || len([]rune(c.Title)) > maxName || len([]rune(c.Script)) > 10000 || c.Assign == "staff" && len(c.Staff) == 0 {
		return "", i18n.E("err.mkt_calls")
	}
	if c.Staff == nil {
		c.Staff = []string{}
	}
	var seg any
	if c.SegmentID != "" {
		seg = c.SegmentID
	}
	if c.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO mkt_calls (title, segment_id, script, deadline, assign, staff, created_by)
			VALUES ($1, $2, $3, $4, $5, $6::uuid[], $7) RETURNING id::text`, c.Title, seg, c.Script, c.Deadline, c.Assign, c.Staff, userID).Scan(&c.ID)
		return c.ID, err
	}
	old, err := s.mayChangeCalls(ctx, userID, admin, c.ID)
	if err != nil {
		return "", err
	}
	if !old.Editable() {
		return "", i18n.E("err.mkt_started")
	}
	_, err = s.db.Exec(ctx, `UPDATE mkt_calls SET title = $2, segment_id = $3, script = $4, deadline = $5, assign = $6, staff = $7::uuid[],
		updated_at = now() WHERE id::text = $1`, c.ID, c.Title, seg, c.Script, c.Deadline, c.Assign, c.Staff)
	return c.ID, err
}

// DeleteCallList removes a call list; the deeds it planned stay in the cards.
func (s *Store) DeleteCallList(ctx context.Context, userID string, admin bool, id string) error {
	if _, err := s.mayChangeCalls(ctx, userID, admin, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM mkt_calls WHERE id::text = $1`, id)
	return err
}

// Callee is a card of a call list.
type Callee struct {
	Entity, EntityID, Name, Phone, Responsible string
}

// phoneSQL is the query of a segment's cards with a phone: entity, id, name, phone, responsible.
var phoneSQL = map[string]string{
	"contact": `SELECT 'contact', x.id::text, x.name, x.phone, coalesce(x.responsible_id::text, '') FROM crm_contacts x WHERE trim(x.phone) <> ''`,
	"company": `SELECT 'company', x.id::text, x.title, x.phone, coalesce(x.responsible_id::text, '') FROM crm_companies x WHERE trim(x.phone) <> ''`,
	"lead": `SELECT 'lead', x.id::text, coalesce(nullif(x.name, ''), x.title), x.phone, coalesce(x.responsible_id::text, '') FROM crm_leads x
		JOIN crm_stages st ON st.id = x.stage_id WHERE trim(x.phone) <> ''`,
}

// Callees are the cards of the call list's segment with a phone the viewer may see (CRM rights).
func (s *Store) Callees(ctx context.Context, viewer string, c CallList) ([]Callee, error) {
	if c.SegmentID == "" {
		return nil, nil
	}
	g, err := s.Segment(ctx, c.SegmentID)
	if err != nil {
		return nil, err
	}
	q, ok := phoneSQL[g.Entity]
	if !ok {
		return nil, i18n.E("err.mkt_calls_list")
	}
	q, args, err := s.segmentQuery(ctx, viewer, g, q)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Callee])
	if err != nil {
		return nil, err
	}
	if len(list) > MaxCalls {
		return nil, i18n.E("err.mkt_too_many", MaxCalls)
	}
	return list, nil
}

// StartCallList fixes the cards of a draft (as its starter sees them) and plans their calls: a deed «Звонок» by the
// deadline for the card's responsible (the starter when it has none) or the chosen employees in turn.
func (s *Store) StartCallList(ctx context.Context, userID string, admin bool, id string) (int, error) {
	c, err := s.mayChangeCalls(ctx, userID, admin, id)
	if err != nil {
		return 0, err
	}
	if !c.Editable() {
		return 0, i18n.E("err.mkt_started")
	}
	if c.Deadline == nil {
		return 0, i18n.E("err.crm_activity_deadline")
	}
	list, err := s.Callees(ctx, userID, c)
	if err != nil {
		return 0, err
	}
	if len(list) == 0 {
		return 0, i18n.E("err.mkt_nobody")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mkt_calls SET status = 'started', started_at = now(), updated_at = now() WHERE id::text = $1 AND status = 'draft'`, id)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 0 {
		return 0, i18n.E("err.mkt_started")
	}
	calls := map[string]int{} // by the responsible
	for i, x := range list {
		who := x.Responsible
		if c.Assign == "staff" {
			who = c.Staff[i%len(c.Staff)]
		}
		if who == "" {
			who = userID
		}
		var act string
		if err := tx.QueryRow(ctx, `INSERT INTO crm_activities (entity, entity_id, kind, subject, description, deadline, responsible_id, created_by)
			VALUES ($1, $2, 'call', $3, $4, $5, $6, $7) RETURNING id::text`, x.Entity, x.EntityID, c.Title, c.Script, c.Deadline, who, userID).Scan(&act); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mkt_call_items (call_id, entity, entity_id, name, phone, responsible_id, activity_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, c.ID, x.Entity, x.EntityID, x.Name, strings.TrimSpace(x.Phone), who, act); err != nil {
			return 0, err
		}
		calls[who]++
	}
	for who, n := range calls {
		if who == userID {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_activity', $3, '/crm/activities')`,
			who, userID, c.Title+" ("+strconv.Itoa(n)+")"); err != nil {
			return 0, err
		}
	}
	return len(list), tx.Commit(ctx)
}

// CallItem is a card of a started call list and its call.
type CallItem struct {
	ID                             int64
	Entity, EntityID, Name, Phone  string
	ResponsibleID, ResponsibleName string
	ActivityID                     string // "" — the deed was removed
	DoneAt                         *time.Time
}

// CallItems are the cards of the call list (done: "1" — called, "0" — not yet, "" — all), a page of them, and how
// many there are.
func (s *Store) CallItems(ctx context.Context, id, done string, limit, offset int) ([]CallItem, int, error) {
	cond := map[string]string{"1": "a.done_at IS NOT NULL", "0": "a.id IS NOT NULL AND a.done_at IS NULL"}[done]
	if cond == "" {
		cond = "true"
	}
	from := ` FROM mkt_call_items i LEFT JOIN crm_activities a ON a.id = i.activity_id LEFT JOIN users u ON u.id = i.responsible_id
		WHERE i.call_id::text = $1 AND ` + cond
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*)`+from, id).Scan(&n); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(ctx, `SELECT i.id, i.entity, i.entity_id::text, i.name, i.phone, coalesce(i.responsible_id::text, ''), coalesce(u.name, ''),
		coalesce(i.activity_id::text, ''), a.done_at`+from+` ORDER BY i.id LIMIT $2 OFFSET $3`, id, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[CallItem])
	return list, n, err
}
