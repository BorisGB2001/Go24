package crm

import (
	"context"
	"time"
)

// «Перезвонить»: a client's missed call is an open call deed on the card, due at once, for the card's
// responsible; talking with the client afterwards (calling back, or the client getting through) closes it.

// MissedCall plans «Перезвонить» (subject) for a missed call from number on the card; the responsible is the card's
// (else called). One per card: while one is open, another missed call adds nothing ("" returned).
func (s *Store) MissedCall(ctx context.Context, entity, id, number, called, subject string, at time.Time) (string, error) {
	var open bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM crm_activities WHERE entity = $1 AND entity_id::text = $2
		AND callback_number IS NOT NULL AND done_at IS NULL)`, entity, id).Scan(&open); err != nil || open {
		return "", err
	}
	owner, err := s.Responsible(ctx, entity, id)
	if err != nil {
		return "", err
	}
	if owner == "" {
		owner = called
	}
	if owner == "" {
		return "", nil
	}
	aid, err := s.AddActivity(ctx, called, Activity{Entity: entity, EntityID: id, Kind: "call", Subject: subject,
		Deadline: at, Responsible: Ref{ID: owner}})
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(ctx, `UPDATE crm_activities SET callback_number = $2 WHERE id = $1`, aid, number)
	return aid, err
}

// CalledBack closes the card's open «Перезвонить» deeds: userID has talked with the client. It tells how many.
func (s *Store) CalledBack(ctx context.Context, entity, id, userID string) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM crm_activities WHERE entity = $1 AND entity_id::text = $2
		AND callback_number IS NOT NULL AND done_at IS NULL`, entity, id)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, x := range ids {
		if _, err := s.CompleteActivity(ctx, userID, x); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}
