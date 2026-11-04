package openlines

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Invite brings a colleague into an open dialogue, as «Пригласить в диалог» of the open lines: they see it among
// their own, answer the client and write hidden messages along with its operator, who stays the same; a service line
// and their notification.
func (s *Store) Invite(ctx context.Context, by, id, to string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var operator *string
	var status string
	var invited []string
	if err := tx.QueryRow(ctx, `SELECT operator_id::text, status, invited::text[] FROM ol_sessions WHERE id::text = $1 FOR UPDATE`, id).
		Scan(&operator, &status, &invited); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, to).Scan(&ok); err != nil {
		return err
	}
	if !ok || status != "open" || operator == nil || *operator == to || slices.Contains(invited, to) {
		return i18n.E("err.ol_invite")
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET invited = array_append(invited, $2::uuid) WHERE id::text = $1`, id, to); err != nil {
		return err
	}
	if err := s.note(ctx, tx, id, "ol.note_invited", userName(ctx, tx, by), userName(ctx, tx, to)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) SELECT u.id, 'openline', left($3, 300), '/openlines?s=' || $1
		FROM users u WHERE u.id::text = $2 AND u.active AND u.id::text <> $4`, id, to, s.tr("ol.notify_invited", userName(ctx, tx, by)), by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Uninvite takes a colleague out of the dialogue: they leave it themselves («Покинуть диалог»), or its operator, a
// line's head, an administrator (x.All) removes them. A service line.
func (s *Store) Uninvite(ctx context.Context, x Who, id, who string) error {
	by := x.UserID
	tag, err := s.db.Exec(ctx, `UPDATE ol_sessions SET invited = array_remove(invited, $2::uuid) WHERE id::text = $1 AND $2 = ANY(invited::text[])
		AND ($3 OR $4 = $2 OR operator_id::text = $4)`, id, who, x.All, by)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return i18n.E("err.ol_uninvite")
	}
	// their notifications of the dialogue are read: it is not theirs any more
	if _, err := s.db.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL
		AND user_id::text = $2`, id, who); err != nil {
		return err
	}
	if by == who {
		return s.note(ctx, s.db, id, "ol.note_left", userName(ctx, s.db, who))
	}
	return s.note(ctx, s.db, id, "ol.note_uninvited", userName(ctx, s.db, by), userName(ctx, s.db, who))
}

// notifyInvited tells the colleagues invited into the dialogue of the client's message (one unread notification each).
func notifyInvited(ctx context.Context, q dbtx, sessionID, body string) error {
	_, err := q.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT u.id, 'openline', left($2, 300), '/openlines?s=' || $1 FROM ol_sessions s JOIN users u ON u.id = ANY(s.invited)
		WHERE s.id::text = $1 AND u.active
		AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = u.id AND n.kind = 'openline' AND n.url = '/openlines?s=' || $1 AND n.read_at IS NULL)`,
		sessionID, body)
	return err
}
