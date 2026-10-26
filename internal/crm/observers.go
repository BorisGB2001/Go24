package crm

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// «Наблюдатели» of a deal (a smart process's card too) and a lead: people who see the card
// whatever their rights to it, may comment it and are told of its new comments and stages.

// watched: the kinds of card that have observers.
func watched(entity string) bool { return entity == "deal" || entity == "lead" }

// observerSQL is the condition "the person (argument n) observes alias.id".
func observerSQL(entity, alias, n string) string {
	return alias + `.id IN (SELECT item_id FROM crm_` + entity + `_observers WHERE user_id::text = ` + n + `)`
}

// ObserverIDs are the observers' IDs (the people picker's choice).
func ObserverIDs(list []Ref) []string {
	ids := make([]string, len(list))
	for i, r := range list {
		ids[i] = r.ID
	}
	return ids
}

// Observers of a card, by name.
func (s *Store) Observers(ctx context.Context, entity, id string) ([]Ref, error) {
	if !watched(entity) {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name FROM crm_`+entity+`_observers o JOIN users u ON u.id = o.user_id
		WHERE o.item_id::text = $1 ORDER BY u.name`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ref, error) {
		var x Ref
		return x, r.Scan(&x.ID, &x.Name)
	})
}

// setObservers makes the people the card's observers (nil — keeps them); the new ones are notified by userID.
func (s *Store) setObservers(ctx context.Context, userID, entity, id, title string, list []Ref) error {
	if list == nil {
		return nil
	}
	ids := []string{}
	for _, r := range list {
		if r.ID != "" {
			ids = append(ids, r.ID)
		}
	}
	rows, err := s.db.Query(ctx, `WITH del AS (DELETE FROM crm_`+entity+`_observers WHERE item_id::text = $1 AND NOT (user_id::text = ANY($2)))
		INSERT INTO crm_`+entity+`_observers (item_id, user_id) SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2)
		ON CONFLICT DO NOTHING RETURNING user_id::text`, id, ids)
	if err != nil {
		return err
	}
	added, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, u := range added {
		if u == userID {
			continue
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'crm_observer', $3, $4)`,
			u, userID, title, "/crm/"+plural[entity]+"/"+id); err != nil {
			return err
		}
	}
	return nil
}

// SetObservers makes the people (IDs) the observers of a deal or a lead, with the change in its history.
func (s *Store) SetObservers(ctx context.Context, userID, entity, id string, ids []string) error {
	if !watched(entity) {
		return ErrNotFound
	}
	var title string
	if err := s.db.QueryRow(ctx, `SELECT title FROM crm_`+plural[entity]+` WHERE id::text = $1`, id).Scan(&title); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	old, err := s.Observers(ctx, entity, id)
	if err != nil {
		return err
	}
	list := []Ref{}
	for _, u := range ids {
		list = append(list, Ref{ID: u})
	}
	if err := s.setObservers(ctx, userID, entity, id, title, list); err != nil {
		return err
	}
	next, err := s.Observers(ctx, entity, id)
	if err != nil {
		return err
	}
	var d differ
	d.add("observers", refNames(old), refNames(next))
	return s.LogChanges(ctx, userID, entity, id, d)
}

// IsObserver: does the person observe the card?
func (s *Store) IsObserver(ctx context.Context, userID, entity, id string) (bool, error) {
	if !watched(entity) || userID == "" {
		return false, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_`+entity+`_observers WHERE item_id::text = $1 AND user_id::text = $2)`,
		id, userID).Scan(&ok)
	return ok, err
}

// CheckCard is Check of an action on a card: reading it is allowed to its observers too.
func (s *Store) CheckCard(ctx context.Context, userID, entity, action, id, responsibleID string) error {
	err := s.Check(ctx, userID, entity, action, responsibleID)
	if !errors.Is(err, ErrForbidden) || action != "read" {
		return err
	}
	if ok, oerr := s.IsObserver(ctx, userID, entity, id); oerr != nil || ok {
		return oerr
	}
	return err
}

// tellWatchers notifies the card's observers (and its responsible, withResponsible) but the actor of a comment or a
// stage: kind crm_comment or crm_stage, body — the card's title (and the stage).
func (s *Store) tellWatchers(ctx context.Context, actor, entity, id, kind, body string, withResponsible bool) error {
	if !watched(entity) {
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
		SELECT DISTINCT w.u, $3::uuid, $4, $5, $6 FROM (
			SELECT user_id AS u FROM crm_`+entity+`_observers WHERE item_id::text = $1
			UNION SELECT responsible_id FROM crm_`+plural[entity]+` WHERE $2 AND id::text = $1) w
		WHERE w.u <> $3::uuid`, id, withResponsible, actor, kind, body, "/crm/"+plural[entity]+"/"+id)
	return err
}
