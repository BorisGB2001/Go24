package calls

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Rings: a call from a private or group chat rings its people — each of them sees «Звонит …» on any
// page of go24 (the event reaches their browser through go24_notify, key "c<user>") and answers or declines.

// RingTime is how long a call rings before it counts as missed.
const RingTime = 45 * time.Second

var ErrNoRing = errors.New("calls: no such ring")

// Rings keeps the rings in the database.
type Rings struct{ DB *pgxpool.Pool }

// Ring is one call ringing.
type Ring struct {
	ID, CallerID, Room string
	Video              bool
	At                 time.Time
	People             []string // rung
}

// Event is what a browser of one of the people gets: a ring (to answer), an answer (to the caller), or the end
// of the ringing (stop the tune).
type Event struct {
	Ring   string `json:"ring"`
	Kind   string `json:"kind"` // ring | accepted | declined | stop
	From   string `json:"from,omitempty"`
	Photo  string `json:"photo,omitempty"`
	Chat   string `json:"chat,omitempty"`
	Video  bool   `json:"video,omitempty"`
	URL    string `json:"url,omitempty"`
	Left   int    `json:"left,omitempty"` // a ring: seconds it still rings
	Person string `json:"person,omitempty"`
}

// Start rings the people from the caller's chat.
func (rs *Rings) Start(ctx context.Context, callerID, room string, video bool, people []string) (Ring, error) {
	r := Ring{CallerID: callerID, Room: room, Video: video, People: people}
	tx, err := rs.DB.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx, `INSERT INTO call_rings (caller_id, chat_room, video) VALUES ($1, $2, $3) RETURNING id::text, created_at`,
		callerID, room, video).Scan(&r.ID, &r.At); err != nil {
		return r, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO call_ring_people (ring_id, user_id) SELECT $1, unnest($2::uuid[]) ON CONFLICT DO NOTHING`,
		r.ID, people); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// Get is a ring of the person (they call or are rung).
func (rs *Rings) Get(ctx context.Context, userID, id string) (Ring, error) {
	var r Ring
	var ended *time.Time
	err := rs.DB.QueryRow(ctx, `SELECT r.id::text, r.caller_id::text, r.chat_room, r.video, r.created_at, r.ended_at,
			coalesce(array_agg(p.user_id::text) FILTER (WHERE p.user_id IS NOT NULL), '{}')
		FROM call_rings r LEFT JOIN call_ring_people p ON p.ring_id = r.id
		WHERE r.id::text = $1 GROUP BY r.id`, id).Scan(&r.ID, &r.CallerID, &r.Room, &r.Video, &r.At, &ended, &r.People)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNoRing
	}
	if err != nil {
		return r, err
	}
	if r.CallerID != userID && !contains(r.People, userID) {
		return r, ErrNoRing
	}
	return r, nil
}

// Answer records the person's answer (accepted | declined) while the call still rings; false — it no longer does.
func (rs *Rings) Answer(ctx context.Context, userID, id, answer string) (bool, error) {
	tag, err := rs.DB.Exec(ctx, `UPDATE call_ring_people p SET answer = $3 FROM call_rings r
		WHERE p.ring_id = r.id AND r.id::text = $1 AND p.user_id = $2 AND p.answer = '' AND r.ended_at IS NULL
		AND r.created_at > now() - make_interval(secs => $4)`, id, userID, answer, RingTime.Seconds())
	return tag.RowsAffected() == 1, err
}

// End stops the ringing (the caller gave up or has left, or the time is out): who has not answered missed it.
// It gives those people.
func (rs *Rings) End(ctx context.Context, id string) ([]string, error) {
	tag, err := rs.DB.Exec(ctx, `UPDATE call_rings SET ended_at = now() WHERE id::text = $1 AND ended_at IS NULL`, id)
	if err != nil || tag.RowsAffected() == 0 {
		return nil, err
	}
	rows, err := rs.DB.Query(ctx, `UPDATE call_ring_people SET answer = 'missed' WHERE ring_id::text = $1 AND answer = '' RETURNING user_id::text`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// Ringing are the rings the person has not answered yet (a page opened while the call rings shows it too).
func (rs *Rings) Ringing(ctx context.Context, userID string) ([]Ring, error) {
	rows, err := rs.DB.Query(ctx, `SELECT r.id::text, r.caller_id::text, r.chat_room, r.video, r.created_at FROM call_rings r
		JOIN call_ring_people p ON p.ring_id = r.id AND p.user_id = $1 AND p.answer = ''
		WHERE r.ended_at IS NULL AND r.created_at > now() - make_interval(secs => $2) ORDER BY r.created_at`, userID, RingTime.Seconds())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Ring, error) {
		var r Ring
		err := row.Scan(&r.ID, &r.CallerID, &r.Room, &r.Video, &r.At)
		return r, err
	})
}

// Notify hands the event to the person's browsers (go24_notify → the stream of /notifications/live).
func (rs *Rings) Notify(ctx context.Context, userID string, ev Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	_, err = rs.DB.Exec(ctx, `SELECT pg_notify('go24_notify', 'c' || $1 || ':' || $2)`, userID, string(data))
	return err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
