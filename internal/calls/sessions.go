package calls

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sessions: a call of a chat from its first person to its last — a line in the chat «Звонок начат»
// with «Присоединиться» while it goes on, then «Звонок завершён · 12 мин · Анна, Борис» (or «Пропущенный звонок»
// when nobody came). go24 learns the call is over by asking LiveKit who is still in its room.
type Sessions struct{ DB *pgxpool.Pool }

// Session is one call of a chat.
type Session struct {
	ID, Room, Chat, Event, StartedBy string
	Video                            bool
	People                           []string // who took part, the one who started first
	Started, Ended                   time.Time
}

// Join counts the person into the chat's call, starting it when none goes on; isNew — the call has just begun
// (its line is to be posted).
func (ss *Sessions) Join(ctx context.Context, room, chat, userID string, video bool) (Session, bool, error) {
	var s Session
	var isNew bool
	err := ss.DB.QueryRow(ctx, `INSERT INTO call_sessions (lk_room, chat_room, started_by, video, people) VALUES ($1, $2, $3, $4, ARRAY[$3::uuid])
		ON CONFLICT (lk_room) WHERE ended_at IS NULL DO UPDATE
			SET people = CASE WHEN $3::uuid = ANY(call_sessions.people) THEN call_sessions.people ELSE call_sessions.people || $3::uuid END
		RETURNING id::text, lk_room, chat_room, event_id, started_by::text, video, people::text[], started_at, xmax = 0`,
		room, chat, userID, video).Scan(&s.ID, &s.Room, &s.Chat, &s.Event, &s.StartedBy, &s.Video, &s.People, &s.Started, &isNew)
	return s, isNew, err
}

// SetLine remembers the call's line in the chat.
func (ss *Sessions) SetLine(ctx context.Context, id, event string) error {
	_, err := ss.DB.Exec(ctx, `UPDATE call_sessions SET event_id = $2 WHERE id::text = $1`, id, event)
	return err
}

// Starter is who started the call of the LiveKit room that goes on now; "" — none goes on.
func (ss *Sessions) Starter(ctx context.Context, room string) (string, error) {
	var id string
	err := ss.DB.QueryRow(ctx, `SELECT started_by::text FROM call_sessions WHERE lk_room = $1 AND ended_at IS NULL`, room).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return id, err
}

// Going are the calls not over yet.
func (ss *Sessions) Going(ctx context.Context) ([]Session, error) {
	rows, err := ss.DB.Query(ctx, `SELECT id::text, lk_room, chat_room, event_id, started_by::text, video, people::text[], started_at
		FROM call_sessions WHERE ended_at IS NULL ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Session, error) {
		var s Session
		err := row.Scan(&s.ID, &s.Room, &s.Chat, &s.Event, &s.StartedBy, &s.Video, &s.People, &s.Started)
		return s, err
	})
}

// Finish ends the call (once): false — it was over already.
func (ss *Sessions) Finish(ctx context.Context, id string) (Session, bool, error) {
	var s Session
	err := ss.DB.QueryRow(ctx, `UPDATE call_sessions SET ended_at = now() WHERE id::text = $1 AND ended_at IS NULL
		RETURNING id::text, lk_room, chat_room, event_id, started_by::text, video, people::text[], started_at, ended_at`, id).
		Scan(&s.ID, &s.Room, &s.Chat, &s.Event, &s.StartedBy, &s.Video, &s.People, &s.Started, &s.Ended)
	if err == pgx.ErrNoRows {
		return s, false, nil
	}
	return s, err == nil, err
}
