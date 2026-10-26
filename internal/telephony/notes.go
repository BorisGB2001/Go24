package telephony

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------- «Комментарий к звонку», as in the call card ----------

// SetComment is the employee's comment on their call with the number that began at since or later: the call of the
// log gets it (and its «Звонок» in the CRM card's timeline); while the call is not in the log yet — it is still going,
// or Asterisk has not reported it — the comment waits for it. "" clears it. The id of the call; "" — waiting.
func (s *Store) SetComment(ctx context.Context, userID, number string, since time.Time, text string) (string, error) {
	key := BlackKey(number)
	if key == "" {
		return "", errors.New("telephony: a comment without a number")
	}
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM telephony_calls
		WHERE (from_user::text = $1 OR to_user::text = $1) AND started_at >= $3
		  AND (right(regexp_replace(from_number, '\D', '', 'g'), 10) = $2 OR right(regexp_replace(to_number, '\D', '', 'g'), 10) = $2)
		ORDER BY started_at DESC LIMIT 1`, userID, key, since.Add(-time.Minute)).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if text == "" {
			_, err = s.db.Exec(ctx, `DELETE FROM telephony_notes WHERE user_id::text = $1 AND number = $2`, userID, key)
		} else {
			_, err = s.db.Exec(ctx, `INSERT INTO telephony_notes (user_id, number, comment) VALUES ($1, $2, $3)
				ON CONFLICT (user_id, number) DO UPDATE SET comment = EXCLUDED.comment, created_at = now()`, userID, key, text)
		}
		return "", err
	case err != nil:
		return "", err
	}
	return id, s.comment(ctx, id, text)
}

// SetCallComment sets the comment of a call of the log ("" clears it).
func (s *Store) SetCallComment(ctx context.Context, id, text string) error {
	return s.comment(ctx, id, text)
}

// comment sets the comment of a call of the log and of its «Звонок» in the CRM card.
func (s *Store) comment(ctx context.Context, id, text string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE telephony_calls SET comment = $2 WHERE id::text = $1`, id, text); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE crm_timeline SET body = (CASE WHEN $2 = '' THEN body::jsonb - 'note'
			ELSE jsonb_set(body::jsonb, '{note}', to_jsonb($2::text)) END)::text
		WHERE kind = 'call' AND body LIKE '{%' AND body::jsonb->>'id' = $1`, id, text); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Pending is the comment the employee wrote on the call with the number still going ("" — none).
func (s *Store) Pending(ctx context.Context, userID, number string) (string, error) {
	var text string
	err := s.db.QueryRow(ctx, `SELECT comment FROM telephony_notes WHERE user_id::text = $1 AND number = $2
		AND created_at > now() - interval '3 hours'`, userID, BlackKey(number)).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return text, err
}

// TakeNote gives a call just reported by Asterisk the comment its employee wrote while talking (within 3 hours).
func (s *Store) TakeNote(ctx context.Context, callID string) error {
	if callID == "" {
		return nil
	}
	var text string
	err := s.db.QueryRow(ctx, `DELETE FROM telephony_notes n USING telephony_calls c
		WHERE c.id::text = $1 AND n.user_id IN (c.from_user, c.to_user) AND n.created_at > now() - interval '3 hours'
		  AND n.number IN (right(regexp_replace(c.from_number, '\D', '', 'g'), 10), right(regexp_replace(c.to_number, '\D', '', 'g'), 10))
		RETURNING n.comment`, callID).Scan(&text)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.comment(ctx, callID, text)
}
