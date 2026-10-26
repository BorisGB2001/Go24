package crm

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Дела»: a call, a meeting or a to-do on a card, with a deadline and a responsible person. Open ones are
// planned on the card, shown on its kanban tile and counted in «Мои дела» (overdue and today's); done ones go to the
// card's timeline. The responsible is reminded RemindBefore the deadline.

// RemindBefore is how long before the deadline the responsible is reminded.
const RemindBefore = 15 * time.Minute

// ActivityKinds in the order of the form.
var ActivityKinds = []string{"call", "meeting", "todo"}

// Activity is a planned deed on a card.
type Activity struct {
	ID, Entity, EntityID, Kind string
	Subject, Description       string
	Deadline                   time.Time
	Responsible                Ref
	DoneAt                     *time.Time
	CardTitle                  string // the card's title or name (in «Мои дела»)
	CreatedBy                  string
}

// Overdue: open and past its deadline.
func (a Activity) Overdue() bool { return a.DoneAt == nil && a.Deadline.Before(time.Now()) }

// Path is the card's page.
func (a Activity) Path() string { return "/crm/" + plural[a.Entity] + "/" + a.EntityID }

var plural = map[string]string{"deal": "deals", "lead": "leads", "contact": "contacts", "company": "companies"}

const activitySQL = `SELECT a.id, a.entity, a.entity_id::text, a.kind, a.subject, a.description, a.deadline, a.responsible_id::text, u.name,
		a.done_at, coalesce(d.title, l.title, x.name, c.title, ''), a.created_by::text
	FROM crm_activities a JOIN users u ON u.id = a.responsible_id
	LEFT JOIN crm_deals d ON a.entity = 'deal' AND d.id = a.entity_id LEFT JOIN crm_leads l ON a.entity = 'lead' AND l.id = a.entity_id
	LEFT JOIN crm_contacts x ON a.entity = 'contact' AND x.id = a.entity_id LEFT JOIN crm_companies c ON a.entity = 'company' AND c.id = a.entity_id`

func scanActivities(rows pgx.Rows, err error) ([]Activity, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Activity, error) {
		var a Activity
		err := r.Scan(&a.ID, &a.Entity, &a.EntityID, &a.Kind, &a.Subject, &a.Description, &a.Deadline, &a.Responsible.ID,
			&a.Responsible.Name, &a.DoneAt, &a.CardTitle, &a.CreatedBy)
		return a, err
	})
}

// AddActivity plans a deed on a card; another responsible is notified.
func (s *Store) AddActivity(ctx context.Context, userID string, a Activity) (string, error) {
	if err := clean(&a.Subject, 300); err != nil || a.Subject == "" {
		return "", i18n.E("err.crm_activity_subject")
	}
	if err := clean(&a.Description, 10000); err != nil {
		return "", err
	}
	if a.Deadline.IsZero() {
		return "", i18n.E("err.crm_activity_deadline")
	}
	known := false
	for _, k := range ActivityKinds {
		known = known || k == a.Kind
	}
	if !known {
		a.Kind = "todo"
	}
	if a.Responsible.ID == "" {
		a.Responsible.ID = userID
	}
	if plural[a.Entity] == "" {
		return "", ErrNotFound
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO crm_activities (entity, entity_id, kind, subject, description, deadline, responsible_id, created_by)
		SELECT $1, id, $3, $4, $5, $6, $7, $8 FROM crm_`+plural[a.Entity]+` WHERE id::text = $2 RETURNING id`,
		a.Entity, a.EntityID, a.Kind, a.Subject, a.Description, a.Deadline, a.Responsible.ID, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if a.Responsible.ID != userID {
		_, err = s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_activity', $3, $4)`,
			a.Responsible.ID, userID, a.Subject, a.Path())
	}
	return id, err
}

// Activity is one deed.
func (s *Store) Activity(ctx context.Context, id string) (Activity, error) {
	list, err := scanActivities(s.db.Query(ctx, activitySQL+` WHERE a.id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Activity{}, err
	}
	return list[0], nil
}

// Activities of a card: the open ones by deadline.
func (s *Store) Activities(ctx context.Context, entity, id string) ([]Activity, error) {
	return scanActivities(s.db.Query(ctx, activitySQL+` WHERE a.entity = $1 AND a.entity_id::text = $2 AND a.done_at IS NULL
		ORDER BY a.deadline`, entity, id))
}

// MyActivities are the person's open deeds by deadline: all, or only "overdue", or "today" (until the end of the
// day, overdue included), or "planned" (after today).
func (s *Store) MyActivities(ctx context.Context, userID, filter string, now time.Time) ([]Activity, error) {
	end := endOfDay(now)
	cond := map[string]string{"overdue": ` AND a.deadline < $2`, "today": ` AND a.deadline < $3`, "planned": ` AND a.deadline >= $3`}[filter]
	return scanActivities(s.db.Query(ctx, activitySQL+` WHERE a.responsible_id::text = $1 AND a.done_at IS NULL
		AND ($2::timestamptz IS NOT NULL AND $3::timestamptz IS NOT NULL)`+cond+` ORDER BY a.deadline LIMIT 500`, userID, now, end))
}

func endOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, t.Location())
}

// ActivityCounter is the person's open deeds due by the end of the day (overdue included) — the «Дела» counter.
func (s *Store) ActivityCounter(ctx context.Context, userID string, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM crm_activities WHERE responsible_id::text = $1 AND done_at IS NULL AND deadline < $2`,
		userID, endOfDay(now)).Scan(&n)
	return n, err
}

// NextActivities are the nearest open deed of each of the cards (the kanban tiles).
func (s *Store) NextActivities(ctx context.Context, entity string, ids []string) (map[string]*Activity, error) {
	list, err := scanActivities(s.db.Query(ctx, `SELECT DISTINCT ON (a.entity_id) * FROM (`+activitySQL+`
		WHERE a.entity = $1 AND a.entity_id::text = ANY($2) AND a.done_at IS NULL) a ORDER BY a.entity_id, a.deadline`, entity, ids))
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Activity, len(list))
	for i := range list {
		out[list[i].EntityID] = &list[i]
	}
	return out, nil
}

// CompleteActivity marks a deed done; the card's timeline tells.
func (s *Store) CompleteActivity(ctx context.Context, userID, id string) (Activity, error) {
	a, err := s.Activity(ctx, id)
	if err != nil || a.DoneAt != nil {
		return a, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE crm_activities SET done_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return s.event(ctx, tx, a.Entity, a.EntityID, userID, "activity", a.Kind+":"+a.Subject)
	})
	if err == nil {
		err = s.Fire(ctx, a.Entity, a.EntityID, "activity")
	}
	return a, err
}

// DeleteActivity removes a deed.
func (s *Store) DeleteActivity(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM crm_activities WHERE id::text = $1`, id)
	return err
}

// RemindDue reminds the responsible of the open deeds whose deadline is RemindBefore away (or past, if not yet
// reminded); each deed once.
func (s *Store) RemindDue(ctx context.Context, now time.Time) (int, error) {
	rows, err := s.db.Query(ctx, `UPDATE crm_activities a SET reminded_at = $1 WHERE done_at IS NULL AND reminded_at IS NULL
		AND deadline <= $2 AND deadline > $1::timestamptz - interval '1 day' RETURNING id::text`, now, now.Add(RemindBefore))
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		a, err := s.Activity(ctx, id)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'crm_activity_remind', $2, $3)`,
			a.Responsible.ID, a.Subject+" · "+a.CardTitle, a.Path()); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
