package openlines

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

func contains(list []string, v string) bool { return slices.Contains(list, v) }

// queue is the line's operators in their order (active users only); an empty queue is the widget's creator.
func (s *Store) queue(ctx context.Context, q dbtx, widgetID string) ([]string, error) {
	var ids []string
	err := q.QueryRow(ctx, `SELECT coalesce(array_agg(u.id::text ORDER BY o.n), '{}') FROM ol_widgets w,
		unnest(CASE WHEN cardinality(w.operators) > 0 THEN w.operators ELSE ARRAY[w.created_by] END) WITH ORDINALITY o(id, n)
		JOIN users u ON u.id = o.id WHERE w.id::text = $1 AND u.active`, widgetID).Scan(&ids)
	return ids, err
}

// assign hands the dialogue to an operator of the line: keep (when given), else by the line's mode among the queue
// without skip. "" — nobody is left.
func (s *Store) assign(ctx context.Context, q dbtx, w Widget, sessionID, keep string, skip []string) (string, error) {
	ops, err := s.queue(ctx, q, w.ID)
	if err != nil {
		return "", err
	}
	next := keep
	if next == "" {
		var free []string
		for _, o := range ops {
			if !contains(skip, o) {
				free = append(free, o)
			}
		}
		if len(free) == 0 {
			return "", nil
		}
		next = free[0]
		switch w.QueueMode {
		case "evenly": // the fewest open dialogues; equal — in the queue's order
			var busy []int
			if err := q.QueryRow(ctx, `SELECT array_agg((SELECT count(*) FROM ol_sessions s WHERE s.status = 'open' AND s.operator_id = o.id::uuid
				AND s.id::text <> $2) ORDER BY o.n) FROM unnest($1::text[]) WITH ORDINALITY o(id, n)`, free, sessionID).Scan(&busy); err != nil {
				return "", err
			}
			for i := range free {
				if busy[i] < busy[slices.Index(free, next)] {
					next = free[i]
				}
			}
		case "strict": // the one after who had the last dialogue
			var last *string
			if err := q.QueryRow(ctx, `SELECT last_operator::text FROM ol_widgets WHERE id::text = $1`, w.ID).Scan(&last); err != nil {
				return "", err
			}
			if last != nil {
				from := slices.Index(ops, *last)
				for i := 1; i <= len(ops); i++ {
					if o := ops[(from+i+len(ops))%len(ops)]; contains(free, o) {
						next = o
						break
					}
				}
			}
		}
	}
	if _, err := q.Exec(ctx, `UPDATE ol_sessions SET operator_id = $2, assigned_at = now() WHERE id::text = $1`, sessionID, next); err != nil {
		return "", err
	}
	_, err = q.Exec(ctx, `UPDATE ol_widgets SET last_operator = $2 WHERE id::text = $1`, w.ID, next)
	return next, err
}

// notifyOperators: the dialogue's operator, while nobody has it — the line's queue (or its creator); one unread
// notification per dialogue and person.
func notifyOperators(ctx context.Context, q dbtx, sessionID, body string) error {
	_, err := q.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT u.id, 'openline', left($2, 300), '/openlines?s=' || $1 FROM users u, ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.id::text = $1 AND u.active AND CASE WHEN s.operator_id IS NOT NULL THEN u.id = s.operator_id
			WHEN cardinality(w.operators) > 0 THEN u.id = ANY(w.operators) ELSE u.id = w.created_by END
		AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = u.id AND n.kind = 'openline' AND n.url = '/openlines?s=' || $1 AND n.read_at IS NULL)`,
		sessionID, body)
	return err
}

// note is a service line of the dialogue (operators only).
func (s *Store) note(ctx context.Context, q dbtx, sessionID, key string, args ...any) error {
	_, err := q.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind) VALUES ($1::uuid, $2, 'note')`, sessionID, s.tr(key, args...))
	return err
}

func userName(ctx context.Context, q dbtx, id string) string {
	var n string
	_ = q.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, id).Scan(&n)
	return n
}

// closeSession closes an open dialogue with the line's closing message and a service line (by — who closed it, or ""
// when the visitor left it).
func (s *Store) closeSession(ctx context.Context, id, by string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var text string
	err = tx.QueryRow(ctx, `UPDATE ol_sessions s SET status = 'closed', closed_at = now(), waiting_since = NULL FROM ol_widgets w
		WHERE w.id = s.widget_id AND s.id::text = $1 AND s.status = 'open' RETURNING w.close_text`, id).Scan(&text)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if text != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind) VALUES ($1::uuid, $2, 'close')`, id, text); err != nil {
			return err
		}
	}
	if by != "" {
		err = s.note(ctx, tx, id, "ol.note_closed", userName(ctx, tx, by))
	} else {
		err = s.note(ctx, tx, id, "ol.note_autoclosed")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Due is the line's clock (go24-worker, every half a minute): a dialogue its operator has not answered in time goes
// to the next one; the visitor waiting too long gets «all operators are busy»; a dialogue the visitor left after an
// answer is closed. It returns how many dialogues changed.
func (s *Store) Due(ctx context.Context, now time.Time) (int, error) {
	n := 0
	widgets := map[string]Widget{}
	line := func(id string) (Widget, error) {
		if w, ok := widgets[id]; ok {
			return w, nil
		}
		w, err := s.Widget(ctx, id)
		widgets[id] = w
		return w, err
	}
	type due struct{ ID, Widget, Operator string }
	list := func(sql string) ([]due, error) {
		rows, err := s.db.Query(ctx, sql, now)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByPos[due])
	}

	// not answered in time — the next operator
	late, err := list(`SELECT s.id::text, s.widget_id::text, s.operator_id::text FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.status = 'open' AND w.active AND w.queue_mode <> 'all' AND s.operator_id IS NOT NULL AND s.answered_at IS NULL
		AND s.waiting_since IS NOT NULL AND s.assigned_at < $1::timestamptz - make_interval(mins => w.answer_wait)`)
	if err != nil {
		return n, err
	}
	for _, d := range late {
		w, err := line(d.Widget)
		if err != nil {
			return n, err
		}
		if !w.InHours(now) {
			continue
		}
		if err := s.passOn(ctx, w, d.ID, d.Operator); err != nil {
			return n, err
		}
		n++
	}

	// nobody has answered for a while — «all operators are busy», once per wait
	waiting, err := list(`SELECT s.id::text, s.widget_id::text, '' FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.status = 'open' AND w.noanswer_text <> '' AND NOT s.noanswer_sent AND s.waiting_since < $1::timestamptz - make_interval(mins => w.noanswer_after)`)
	if err != nil {
		return n, err
	}
	for _, d := range waiting {
		w, err := line(d.Widget)
		if err != nil {
			return n, err
		}
		if !w.InHours(now) {
			continue
		}
		if _, err := s.db.Exec(ctx, `WITH m AS (INSERT INTO ol_messages (session_id, body, kind) VALUES ($1::uuid, $2, 'noanswer'))
			UPDATE ol_sessions SET noanswer_sent = true WHERE id = $1::uuid`, d.ID, w.NoanswerText); err != nil {
			return n, err
		}
		n++
	}

	// answered, and the visitor is silent — closed
	left, err := list(`SELECT s.id::text, s.widget_id::text, '' FROM ol_sessions s JOIN ol_widgets w ON w.id = s.widget_id
		WHERE s.status = 'open' AND w.autoclose > 0 AND s.waiting_since IS NULL AND s.answered_at IS NOT NULL
		AND s.last_at < $1::timestamptz - make_interval(hours => w.autoclose)`)
	if err != nil {
		return n, err
	}
	for _, d := range left {
		if err := s.closeSession(ctx, d.ID, ""); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// passOn gives the dialogue its operator has not answered to the next one of the queue; when the whole queue has
// passed it by, the round starts again. The only operator keeps it (the clock starts anew).
func (s *Store) passOn(ctx context.Context, w Widget, id, from string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var tried []string
	if err := tx.QueryRow(ctx, `SELECT tried::text[] FROM ol_sessions WHERE id::text = $1 FOR UPDATE`, id).Scan(&tried); err != nil {
		return err
	}
	skip := append(tried, from)
	next, err := s.assign(ctx, tx, w, id, "", skip)
	if err == nil && next == "" { // everyone has had it: a new round
		skip = []string{from}
		next, err = s.assign(ctx, tx, w, id, "", skip)
	}
	if err != nil {
		return err
	}
	if next == "" {
		if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET assigned_at = now() WHERE id::text = $1`, id); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE ol_sessions SET tried = $2::text[]::uuid[] WHERE id::text = $1`, id, skip); err != nil {
		return err
	}
	if err := s.note(ctx, tx, id, "ol.note_passed", userName(ctx, tx, from), userName(ctx, tx, next)); err != nil {
		return err
	}
	// the dialogue is not the previous operator's any more
	if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL
		AND user_id::text = $2`, id, from); err != nil {
		return err
	}
	var last string
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT body FROM ol_messages WHERE session_id::text = $1 AND author_id IS NULL AND kind = ''
		ORDER BY id DESC LIMIT 1), '')`, id).Scan(&last); err != nil {
		return err
	}
	if err := notifyOperators(ctx, tx, id, last); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.follow(ctx, id)
}
