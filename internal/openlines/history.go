package openlines

import (
	"context"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// clientSQL: the other dialogues of the same client as the dialogue $1 — by the CRM contact and lead, the e-mail,
// the phone (its last 10 digits) and the Telegram user, of every line and channel.
const clientSQL = ` WHERE s.id::text <> $1 AND EXISTS (SELECT 1 FROM ol_messages m WHERE m.session_id = s.id)
	AND EXISTS (SELECT 1 FROM ol_sessions c WHERE c.id::text = $1 AND (s.contact_id = c.contact_id OR s.lead_id = c.lead_id
		OR (c.email <> '' AND lower(s.email) = lower(c.email))
		OR (length(regexp_replace(c.phone, '\D', '', 'g')) >= 7
			AND right(regexp_replace(s.phone, '\D', '', 'g'), 10) = right(regexp_replace(c.phone, '\D', '', 'g'), 10))
		OR (c.tg_user <> '' AND s.tg_user = c.tg_user)))`

// ClientDialogues are the client's other dialogues (the latest first, 20 at most), as «Другие обращения клиента»
// ; open — those of them the user may open.
func (s *Store) ClientDialogues(ctx context.Context, x Who, id string) ([]Session, map[string]bool, error) {
	rows, err := s.db.Query(ctx, sessionSQL+clientSQL+` ORDER BY s.last_at DESC LIMIT 20`, id)
	if err != nil {
		return nil, nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Session])
	if err != nil || len(list) == 0 {
		return list, nil, err
	}
	ids := make([]string, len(list))
	for i, l := range list {
		ids[i] = l.ID
	}
	args := []any{ids}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var seen []string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(array_agg(s.id::text), '{}') FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.id::text = ANY($1::text[]) AND `+x.cond(arg), args...).Scan(&seen); err != nil {
		return nil, nil, err
	}
	open := map[string]bool{}
	for _, id := range seen {
		open[id] = true
	}
	return list, open, nil
}

// TaskMade puts the service line «… создал(а) задачу GO-12» into the dialogue the task was made of.
func (s *Store) TaskMade(ctx context.Context, by, id, key, title string) error {
	return s.note(ctx, s.db, id, "ol.note_task", userName(ctx, s.db, by), key, title)
}

// Session is the dialogue as it is, without reading it.
func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	rows, err := s.db.Query(ctx, sessionSQL+` WHERE s.id::text = $1`, id)
	if err != nil {
		return Session{}, err
	}
	x, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Session])
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	return x, err
}

// VisitorConsent: the visitor accepts the line's agreement (once; ip — where from).
func (s *Store) VisitorConsent(ctx context.Context, widgetID, token, ip string) error {
	id, err := s.session(ctx, widgetID, token)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE ol_sessions SET consent_at = now(), consent_ip = $2 WHERE id = $1 AND consent_at IS NULL`, id, ip)
	return err
}

// Consented: has the visitor accepted the line's agreement (no dialogue yet — no)?
func (s *Store) Consented(ctx context.Context, widgetID, token string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT s.consent_at IS NOT NULL FROM ol_sessions s WHERE s.token_hash = $1 AND s.widget_id::text = $2`,
		hash(token), widgetID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}
