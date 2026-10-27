package openlines

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

func contains(list []string, v string) bool { return slices.Contains(list, v) }

// queue is the line's operators in their order (active users not on a pause only); an empty queue is the widget's creator.
func (s *Store) queue(ctx context.Context, q dbtx, widgetID string) ([]string, error) {
	var ids []string
	err := q.QueryRow(ctx, `SELECT coalesce(array_agg(u.id::text ORDER BY o.n), '{}') FROM ol_widgets w,
		unnest(CASE WHEN cardinality(w.operators) > 0 THEN w.operators ELSE ARRAY[w.created_by] END) WITH ORDINALITY o(id, n)
		JOIN users u ON u.id = o.id WHERE w.id::text = $1 AND u.active AND NOT EXISTS (SELECT 1 FROM ol_paused p WHERE p.user_id = u.id)`, widgetID).Scan(&ids)
	return ids, err
}

// assign hands the dialogue to an operator of the line: keep (when given and not at the line's limit), else by the
// line's mode among the queue without skip and those at the limit. "" — nobody is left.
func (s *Store) assign(ctx context.Context, q dbtx, w Widget, sessionID, keep string, skip []string) (string, error) {
	ops, err := s.queue(ctx, q, w.ID)
	if err != nil {
		return "", err
	}
	full, err := s.atLimit(ctx, q, w, sessionID, ops)
	if err != nil {
		return "", err
	}
	next := keep
	if contains(full, keep) {
		next = ""
	}
	if next == "" {
		var free []string
		for _, o := range ops {
			if !contains(skip, o) && !contains(full, o) {
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

// atLimit are the operators among ops having the line's «Максимальное количество диалогов» of other open dialogues
// (till the answer — those the client waits in; till closing — all of them).
func (s *Store) atLimit(ctx context.Context, q dbtx, w Widget, sessionID string, ops []string) ([]string, error) {
	if w.MaxChats == 0 || len(ops) == 0 {
		return nil, nil
	}
	var full []string
	err := q.QueryRow(ctx, `SELECT coalesce(array_agg(o.id), '{}') FROM unnest($1::text[]) o(id)
		WHERE (SELECT count(*) FROM ol_sessions s WHERE s.status = 'open' AND s.operator_id = o.id::uuid AND s.id::text <> $2
			AND ($4 = 'close' OR s.waiting_since IS NOT NULL)) >= $3`, ops, sessionID, w.MaxChats, w.MaxChatsType).Scan(&full)
	return full, err
}

// notifyOperators: the dialogue's operator, while nobody has it — the line's queue (or its creator) but those on a pause
// and at the line's limit;
// one unread notification per dialogue and person.
func notifyOperators(ctx context.Context, q dbtx, sessionID, body string) error {
	_, err := q.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT u.id, 'openline', left($2, 300), '/openlines?s=' || $1 FROM users u, ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.id::text = $1 AND u.active AND CASE WHEN s.operator_id IS NOT NULL THEN u.id = s.operator_id
			WHEN EXISTS (SELECT 1 FROM ol_paused p WHERE p.user_id = u.id) THEN false
			WHEN cardinality(w.operators) > 0 THEN u.id = ANY(w.operators) ELSE u.id = w.created_by END
		AND (s.operator_id IS NOT NULL OR w.queue_mode = 'all' OR w.max_chats = 0 OR (SELECT count(*) FROM ol_sessions x WHERE x.status = 'open'
			AND x.operator_id = u.id AND x.id <> s.id AND (w.max_chats_type = 'close' OR x.waiting_since IS NOT NULL)) < w.max_chats)
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
	if by != "" {
		return s.closeAs(ctx, id, "ol.note_closed", userName(ctx, s.db, by))
	}
	return s.closeAs(ctx, id, "ol.note_autoclosed")
}

// closeAs closes an open dialogue with the line's closing message and the service line of the key.
func (s *Store) closeAs(ctx context.Context, id, key string, args ...any) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var text string
	err = tx.QueryRow(ctx, `UPDATE ol_sessions s SET status = 'closed', closed_at = now(), waiting_since = NULL, bot_node = NULL FROM ol_widgets w
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
	if err := s.note(ctx, tx, id, key, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Due is the line's clock (go24-worker, every half a minute): a dialogue its operator has not answered in time goes
// to the next one; the visitor waiting too long gets «all operators are busy»; a dialogue the visitor left after an
// answer is closed; a dialogue waiting longer than the line's KPI is a violation told to its operator and the heads.
// It returns how many dialogues changed.
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
	late, err := list(`SELECT s.id::text, w.id::text, s.operator_id::text FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.status = 'open' AND w.active AND w.queue_mode <> 'all' AND s.operator_id IS NOT NULL AND s.answered_at IS NULL AND NOT s.pinned
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

	// waits longer than the line's KPI — a violation, once per wait
	kpi, err := list(`SELECT s.id::text, w.id::text, '' FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.status = 'open' AND NOT s.spam AND s.waiting_since IS NOT NULL AND s.kpi_wait IS DISTINCT FROM s.waiting_since
		AND CASE WHEN s.answered_at IS NULL THEN w.kpi_first ELSE w.kpi_further END > 0
		AND s.waiting_since < $1::timestamptz - make_interval(mins => CASE WHEN s.answered_at IS NULL THEN w.kpi_first ELSE w.kpi_further END)`)
	if err != nil {
		return n, err
	}
	for _, d := range kpi {
		w, err := line(d.Widget)
		if err != nil {
			return n, err
		}
		if !w.InHours(now) {
			continue
		}
		if err := s.kpiLate(ctx, d.ID); err != nil {
			return n, err
		}
		n++
	}

	// waiting in the queue — to the operator free now, the longest waiting first
	queued, err := list(`SELECT s.id::text, w.id::text, '' FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.status = 'open' AND w.active AND w.queue_mode <> 'all' AND s.operator_id IS NULL AND s.waiting_since IS NOT NULL
		AND s.bot_node IS NULL AND NOT s.spam AND $1::timestamptz IS NOT NULL ORDER BY s.waiting_since`)
	if err != nil {
		return n, err
	}
	for _, d := range queued {
		w, err := line(d.Widget)
		if err != nil {
			return n, err
		}
		if !w.InHours(now) {
			continue
		}
		ok, err := s.dequeue(ctx, w, d.ID)
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
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
	if err := notifyOperators(ctx, tx, id, lastWords(ctx, tx, id)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.follow(ctx, id)
}

// lastWords is the client's last message of the dialogue (for a notification).
func lastWords(ctx context.Context, q dbtx, id string) string {
	var last string
	_ = q.QueryRow(ctx, `SELECT coalesce((SELECT body FROM ol_messages WHERE session_id::text = $1 AND author_id IS NULL AND kind = ''
		ORDER BY id DESC LIMIT 1), '')`, id).Scan(&last)
	return last
}

// dequeue gives a dialogue waiting in the queue (nobody was free: at the limit, on a pause) to an operator free now.
func (s *Store) dequeue(ctx context.Context, w Widget, id string) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var free bool
	if err := tx.QueryRow(ctx, `SELECT operator_id IS NULL FROM ol_sessions WHERE id::text = $1 FOR UPDATE`, id).Scan(&free); err != nil || !free {
		return false, err
	}
	next, err := s.assign(ctx, tx, w, id, "", nil)
	if err != nil || next == "" {
		return false, err
	}
	if err := notifyOperators(ctx, tx, id, lastWords(ctx, tx, id)); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, s.follow(ctx, id)
}

// forward gives a dialogue nobody has answered yet to the responsible of the client's CRM card, when the line says
// so («Направлять обращение ответственному сотруднику») and they are its free operator — once the client is known.
func (s *Store) forward(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var line string
	var operator, owner *string
	err = tx.QueryRow(ctx, `SELECT w.id::text, s.operator_id::text, coalesce((SELECT responsible_id FROM crm_contacts WHERE id = s.contact_id),
		(SELECT responsible_id FROM crm_leads WHERE id = s.lead_id))::text FROM ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.id::text = $1 AND s.status = 'open' AND s.answered_at IS NULL AND NOT s.pinned AND s.bot_node IS NULL AND w.crm_forward FOR UPDATE OF s`,
		id).Scan(&line, &operator, &owner)
	if err == pgx.ErrNoRows || (err == nil && (owner == nil || (operator != nil && *operator == *owner))) {
		return nil
	}
	if err != nil {
		return err
	}
	w, err := s.Widget(ctx, line)
	if err != nil {
		return err
	}
	ops, err := s.queue(ctx, tx, w.ID)
	if err != nil || !contains(ops, *owner) {
		return err
	}
	if next, err := s.assign(ctx, tx, w, id, *owner, ops); err != nil || next != *owner {
		return err // at the limit: the dialogue stays where it is
	}
	if err := s.note(ctx, tx, id, "ol.note_forwarded", userName(ctx, tx, *owner)); err != nil {
		return err
	}
	if operator != nil {
		if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE kind = 'openline' AND url = '/openlines?s=' || $1 AND read_at IS NULL
			AND user_id::text = $2`, id, *operator); err != nil {
			return err
		}
	}
	if err := notifyOperators(ctx, tx, id, lastWords(ctx, tx, id)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return s.follow(ctx, id)
}

// kpiLate marks the dialogue's wait as a violation of the line's KPI: a service line and a notification to its
// operator (while nobody has it — to the line's queue) and to the heads of the lines, as the line says.
func (s *Store) kpiLate(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var first bool
	var mins int
	var name string
	err = tx.QueryRow(ctx, `UPDATE ol_sessions s SET kpi_wait = s.waiting_since, kpi_late = s.kpi_late + 1,
		kpi_first_late = s.kpi_first_late OR s.answered_at IS NULL FROM ol_widgets w
		WHERE w.id = coalesce(s.queue_id, s.widget_id) AND s.id::text = $1 AND s.waiting_since IS NOT NULL
		RETURNING s.answered_at IS NULL, CASE WHEN s.answered_at IS NULL THEN w.kpi_first ELSE w.kpi_further END, s.name`, id).Scan(&first, &mins, &name)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	key := "ol.kpi_further"
	if first {
		key = "ol.kpi_first"
	}
	if err := s.note(ctx, tx, id, key, mins); err != nil {
		return err
	}
	if name == "" {
		name = "#" + id[:6]
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url)
		SELECT u.id, 'openline', left($2, 300), '/openlines?s=' || $1 FROM users u, ol_sessions s JOIN ol_widgets w ON w.id = coalesce(s.queue_id, s.widget_id)
		WHERE s.id::text = $1 AND u.active AND ((w.kpi_operator AND CASE WHEN s.operator_id IS NOT NULL THEN u.id = s.operator_id
			WHEN cardinality(w.operators) > 0 THEN u.id = ANY(w.operators) ELSE u.id = w.created_by END)
			OR (w.kpi_heads AND u.id IN (SELECT user_id FROM ol_heads)))`, id, s.tr("ol.kpi_notify", name, s.tr(key, mins))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
