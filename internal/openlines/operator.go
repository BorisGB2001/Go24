package openlines

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Filter of the dialogue list: the tab — «mine» (the operator's open ones), «noanswer» (the visitor waits), «all»
// (open), «closed»; Q — the visitor's name, phone, e-mail or a word of the conversation; ID — the one dialogue,
// whatever its tab (REST).
type Filter struct {
	Tab, Q, UserID, ID string
	Who                Who // whose dialogues may be seen
}

// Sessions are the dialogues with at least one message, newest first (spam only among the closed ones).
func (s *Store) Sessions(ctx context.Context, f Filter) ([]Session, error) {
	where := []string{`EXISTS (SELECT 1 FROM ol_messages m WHERE m.session_id = s.id)`}
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	where = append(where, f.Who.cond(arg))
	switch {
	case f.ID != "":
		where = append(where, `s.id::text = `+arg(f.ID))
	case f.Tab == "closed":
		where = append(where, `s.status = 'closed'`)
	case f.Tab == "mine":
		where = append(where, `s.status = 'open'`, `s.operator_id::text = `+arg(f.UserID))
	case f.Tab == "noanswer":
		where = append(where, `s.status = 'open'`, `s.waiting_since IS NOT NULL`)
	default:
		where = append(where, `s.status = 'open'`)
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		p := arg("%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%")
		where = append(where, `(s.name ILIKE `+p+` OR s.email ILIKE `+p+` OR s.phone ILIKE `+p+
			` OR EXISTS (SELECT 1 FROM ol_messages m WHERE m.session_id = s.id AND m.kind <> 'note' AND m.body ILIKE `+p+`))`)
	}
	rows, err := s.db.Query(ctx, sessionSQL+` WHERE `+strings.Join(where, " AND ")+` ORDER BY s.last_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Session])
}

// Counts are the open dialogues of the tabs «mine», «noanswer», «all» (the counters on them).
func (s *Store) Counts(ctx context.Context, x Who) (map[string]int, error) {
	var mine, noanswer, all int
	args := []any{x.UserID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	err := s.db.QueryRow(ctx, `SELECT count(*) FILTER (WHERE s.operator_id::text = $1), count(*) FILTER (WHERE s.waiting_since IS NOT NULL), count(*)
		FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id) WHERE s.status = 'open' AND EXISTS (SELECT 1 FROM ol_messages m WHERE m.session_id = s.id)
		AND `+x.cond(arg), args...).Scan(&mine, &noanswer, &all)
	return map[string]int{"mine": mine, "noanswer": noanswer, "all": all}, err
}

// Transfer hands an open dialogue to another employee: a service line and their notification. The new operator has the line's time to answer, as one the queue gave.
func (s *Store) Transfer(ctx context.Context, by, id, to string) error {
	return s.transfer(ctx, by, id, to)
}

// Intercept: the user takes someone else's open dialogue (or an unassigned one) to themselves, as «Перехватить
// диалог» of the open lines; the former operator is told.
func (s *Store) Intercept(ctx context.Context, by, id string) error {
	return s.transfer(ctx, by, id, by)
}

// transfer gives the dialogue to the operator to: another one's — a transfer, by's own — an interception.
func (s *Store) transfer(ctx context.Context, by, id, to string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var from *string
	var status string
	if err := tx.QueryRow(ctx, `SELECT operator_id::text, status FROM ol_sessions WHERE id::text = $1 FOR UPDATE`, id).Scan(&from, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND active)`, to).Scan(&ok); err != nil {
		return err
	}
	if !ok || status != "open" || (from != nil && *from == to) {
		return i18n.E("err.ol_transfer")
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET operator_id = $2::uuid, assigned_at = now(), tried = '{}' WHERE id::text = $1`, id, to); err != nil {
		return err
	}
	// the earlier notifications of the dialogue are read: the new operator gets one of the transfer, the former one —
	// of the interception
	if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL`, id); err != nil {
		return err
	}
	if to != by {
		if err := s.note(ctx, tx, id, "ol.note_transferred", userName(ctx, tx, by), userName(ctx, tx, to)); err != nil {
			return err
		}
		if err := notifyOperators(ctx, tx, id, s.tr("ol.notify_transferred", userName(ctx, tx, by))); err != nil {
			return err
		}
	} else if from == nil {
		if err := s.note(ctx, tx, id, "ol.note_taken", userName(ctx, tx, by)); err != nil {
			return err
		}
	} else {
		if err := s.note(ctx, tx, id, "ol.note_intercepted", userName(ctx, tx, by), userName(ctx, tx, *from)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) SELECT u.id, 'openline', left($3, 300), '/openlines?s=' || $1
			FROM users u WHERE u.id::text = $2 AND u.active`, id, *from, s.tr("ol.notify_intercepted", userName(ctx, tx, by))); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.follow(ctx, id)
}

// Spam closes the dialogue (without the closing message) and stops taking the visitor's messages; «Открыть снова»
// takes it back.
func (s *Store) Spam(ctx context.Context, by, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE ol_sessions SET status = 'closed', closed_at = now(), spam = true, waiting_since = NULL, unread = 0 WHERE id::text = $1 AND NOT spam`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := s.note(ctx, tx, id, "ol.note_spam", userName(ctx, tx, by)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Messages are the dialogue's messages after the given id, read without marking the dialogue read (REST).
func (s *Store) Messages(ctx context.Context, id string, after int64) ([]Message, error) {
	var sid string
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM ol_sessions WHERE id::text = $1`, id).Scan(&sid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return s.messages(ctx, sid, after)
}

func (s *Store) tr(key string, args ...any) string {
	if s.Tr == nil {
		return key
	}
	return s.Tr(key, args...)
}

// ---------- «Быстрые ответы» ----------

// Quick is a prepared answer: the operator's own or the company's (Common; administrators keep them).
type Quick struct {
	ID, Title, Body string
	Common          bool
	CreatedAt       time.Time
}

// Quick are the answers the operator may insert: the company's and their own, by title.
func (s *Store) Quick(ctx context.Context, userID string) ([]Quick, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, title, body, owner_id IS NULL, created_at FROM ol_quick
		WHERE owner_id IS NULL OR owner_id::text = $1 ORDER BY lower(title), created_at`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Quick])
}

// SaveQuick creates (ID "") or changes an answer: one's own, or the company's by an administrator.
func (s *Store) SaveQuick(ctx context.Context, userID string, admin bool, q Quick) (string, error) {
	q.Title, q.Body = strings.TrimSpace(q.Title), strings.TrimSpace(q.Body)
	if q.Title == "" || len([]rune(q.Title)) > 100 || q.Body == "" || len([]rune(q.Body)) > 5000 {
		return "", i18n.E("err.ol_quick")
	}
	if q.Common && !admin {
		return "", i18n.E("err.forbidden")
	}
	var owner *string
	if !q.Common {
		owner = &userID
	}
	if q.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO ol_quick (owner_id, title, body) VALUES ($1, $2, $3) RETURNING id`, owner, q.Title, q.Body).Scan(&q.ID)
		return q.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE ol_quick SET owner_id = $2, title = $3, body = $4
		WHERE id::text = $1 AND (owner_id::text = $5 OR (owner_id IS NULL AND $6))`, q.ID, owner, q.Title, q.Body, userID, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return q.ID, err
}

// DeleteQuick removes one's own answer, or the company's by an administrator.
func (s *Store) DeleteQuick(ctx context.Context, userID string, admin bool, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM ol_quick WHERE id::text = $1 AND (owner_id::text = $2 OR (owner_id IS NULL AND $3))`, id, userID, admin)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// Pause: the operator does not take new dialogues (on) or takes them again; the queue passes them by meanwhile.
func (s *Store) Pause(ctx context.Context, userID string, on bool) error {
	sql := `DELETE FROM ol_paused WHERE user_id::text = $1`
	if on {
		sql = `INSERT INTO ol_paused (user_id) VALUES ($1::uuid) ON CONFLICT DO NOTHING`
	}
	_, err := s.db.Exec(ctx, sql, userID)
	return err
}

// Paused: is the operator on a pause?
func (s *Store) Paused(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ol_paused WHERE user_id::text = $1)`, userID).Scan(&ok)
	return ok, err
}

// Pin pins the open dialogue to its operator (on; an unassigned one is taken by the user) or unpins it: a pinned one
// is not passed on when its operator does not answer in time. Someone else's dialogue — err.ol_pin.
func (s *Store) Pin(ctx context.Context, by, id string, on bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE ol_sessions SET pinned = $3, operator_id = coalesce(operator_id, $2::uuid),
		assigned_at = CASE WHEN operator_id IS NULL THEN now() ELSE assigned_at END
		WHERE id::text = $1 AND status = 'open' AND pinned <> $3 AND (operator_id IS NULL OR operator_id::text = $2)`, id, by, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return i18n.E("err.ol_pin")
	}
	key := "ol.note_unpinned"
	if on {
		key = "ol.note_pinned"
	}
	if err := s.note(ctx, tx, id, key, userName(ctx, tx, by)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// TransferLine passes an open dialogue to another line's queue, as «Передать в открытую линию»: nobody
// has it there yet but by the line's mode; the client stays in their channel. Back to the dialogue's own line — its own queue.
func (s *Store) TransferLine(ctx context.Context, by, id, lineID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var own, now, status string
	if err := tx.QueryRow(ctx, `SELECT widget_id::text, coalesce(queue_id, widget_id)::text, status FROM ol_sessions WHERE id::text = $1 FOR UPDATE`,
		id).Scan(&own, &now, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	w, err := s.Widget(ctx, lineID)
	if errors.Is(err, ErrNotFound) || (err == nil && (!w.Active || status != "open" || w.ID == now)) {
		return i18n.E("err.ol_transfer")
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET queue_id = CASE WHEN $2 = widget_id::text THEN NULL ELSE $2::uuid END,
		operator_id = NULL, assigned_at = now(), tried = '{}', pinned = false WHERE id::text = $1`, id, w.ID); err != nil {
		return err
	}
	if err := s.note(ctx, tx, id, "ol.note_transferred_line", userName(ctx, tx, by), w.Name); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL`, id); err != nil {
		return err
	}
	if w.QueueMode != "all" {
		if _, err := s.assign(ctx, tx, w, id, "", nil); err != nil {
			return err
		}
	}
	if err := notifyOperators(ctx, tx, id, s.tr("ol.notify_transferred", userName(ctx, tx, by))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
