package calls

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// The person's call history, as «Звонки» of the portal messenger: the calls they took part in (started or joined)
// and the rings they missed or declined without coming to that call later.

// Call is one line of the history.
type Call struct {
	ID, Chat string
	Kind     string // out | in (the person took part) | missed | declined (rung, did not come)
	Video    bool
	By       string   // who called: who started the call or rang
	People   []string // who took part (missed and declined: the caller)
	At       time.Time
	Secs     int  // how long it went on (0 while it goes on or when nobody came)
	Live     bool // it goes on
}

// Alone: an outgoing call nobody else came to.
func (c Call) Alone() bool { return c.Kind == "out" && len(c.People) < 2 && !c.Live }

// lost is the SQL of the person's rings they missed or declined and did not come to its call afterwards; a ring
// nobody ended counts as missed once its time is out.
const lost = `FROM call_ring_people p JOIN call_rings r ON r.id = p.ring_id
	WHERE p.user_id = $1 AND (p.answer IN ('missed', 'declined') OR (p.answer = '' AND (r.ended_at IS NOT NULL OR r.created_at < now() - make_interval(secs => $2))))
	AND NOT EXISTS (SELECT 1 FROM call_sessions s WHERE s.chat_room = r.chat_room AND $1 = ANY(s.people)
		AND s.started_at <= coalesce(r.ended_at, r.created_at + make_interval(secs => $2)) AND coalesce(s.ended_at, now()) >= r.created_at)`

// History is the person's calls, the latest first; missed — only the missed and declined ones.
func (rs *Rings) History(ctx context.Context, userID string, missed bool, limit int) ([]Call, error) {
	took := `SELECT id::text, chat_room, CASE WHEN started_by = $1 THEN 'out' ELSE 'in' END, video, started_by::text, people::text[],
			started_at, coalesce(extract(epoch FROM ended_at - started_at)::int, 0), ended_at IS NULL
		FROM call_sessions WHERE $1 = ANY(people)`
	if missed {
		took = `SELECT id::text, chat_room, '', video, started_by::text, people::text[], started_at, 0, false FROM call_sessions WHERE false`
	}
	rows, err := rs.DB.Query(ctx, took+` UNION ALL
		SELECT r.id::text, r.chat_room, CASE WHEN p.answer = 'declined' THEN 'declined' ELSE 'missed' END, r.video, r.caller_id::text,
			ARRAY[r.caller_id::text], r.created_at, 0, false `+lost+`
		ORDER BY 7 DESC LIMIT $3`, userID, RingTime.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Call, error) {
		var c Call
		err := row.Scan(&c.ID, &c.Chat, &c.Kind, &c.Video, &c.By, &c.People, &c.At, &c.Secs, &c.Live)
		if c.Live || c.Alone() {
			c.Secs = 0
		}
		return c, err
	})
}

// Unseen is how many missed and declined calls the person has not looked at yet (beside the tab «Звонки»).
func (rs *Rings) Unseen(ctx context.Context, userID string) (int, error) {
	var n int
	err := rs.DB.QueryRow(ctx, `SELECT count(*) `+lost+` AND NOT p.seen`, userID, RingTime.Seconds()).Scan(&n)
	return n, err
}

// SeeAll: the person has looked at their call history.
func (rs *Rings) SeeAll(ctx context.Context, userID string) error {
	_, err := rs.DB.Exec(ctx, `UPDATE call_ring_people SET seen = true WHERE user_id = $1 AND NOT seen AND ring_id IN (SELECT r.id `+lost+`)`,
		userID, RingTime.Seconds())
	return err
}
