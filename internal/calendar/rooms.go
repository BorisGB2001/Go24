package calendar

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
)

// Meeting rooms, as «Переговорные» of the portal calendar: the administrator keeps the list; a meeting books a room,
// and a room is never booked twice at the same time (every occurrence of a repeated meeting counts).

// Room is a meeting room.
type Room struct {
	ID, Name, Location string
	Capacity           int // seats (0 — not said)
}

// Rooms is the list of meeting rooms by name.
func (s *Store) Rooms(ctx context.Context) ([]Room, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, location, coalesce(capacity, 0) FROM meeting_rooms ORDER BY lower(name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Room, error) {
		var x Room
		err := r.Scan(&x.ID, &x.Name, &x.Location, &x.Capacity)
		return x, err
	})
}

// SaveRoom creates (empty ID) or changes a room; returns its ID. The caller checks that an administrator does it.
func (s *Store) SaveRoom(ctx context.Context, r Room) (string, error) {
	r.Name, r.Location = strings.TrimSpace(r.Name), strings.TrimSpace(r.Location)
	if r.Name == "" {
		return "", i18n.E("err.room_name")
	}
	var seats any
	if r.Capacity > 0 {
		seats = r.Capacity
	}
	if r.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO meeting_rooms (name, location, capacity) VALUES ($1, $2, $3) RETURNING id::text`,
			r.Name, r.Location, seats).Scan(&r.ID)
		return r.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE meeting_rooms SET name = $2, location = $3, capacity = $4 WHERE id::text = $1`, r.ID, r.Name, r.Location, seats)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return r.ID, err
}

// DeleteRoom removes a room; its meetings stay, without the room.
func (s *Store) DeleteRoom(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM meeting_rooms WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// roomEvents: the meetings (but except) that book the room between from and to, with their exceptions.
func (s *Store) roomEvents(ctx context.Context, q querier, room, except string, from, to time.Time) ([]Event, error) {
	rows, err := q.Query(ctx, `SELECT e.id::text, e.title, e.private, e.starts_at, e.ends_at, e.all_day, e.repeat, e.repeat_every, e.repeat_until,
			e.repeat_days, e.repeat_count, coalesce((SELECT array_agg(x.at) FROM event_exceptions x WHERE x.event_id = e.id), '{}')
		FROM events e WHERE e.room_id::text = $1 AND e.id::text <> $2 AND e.starts_at < $4
		  AND ((e.repeat = '' AND e.ends_at > $3) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $3::date)))`,
		room, except, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Event, error) {
		e := Event{Kind: "event"}
		err := r.Scan(&e.ID, &e.Title, &e.Private, &e.Start, &e.End, &e.AllDay, &e.Repeat, &e.Every, &e.Until, &e.Days, &e.Count, &e.Skip)
		return e, err
	})
}

// roomClash is the first occurrence of another meeting in e's room at the time of an occurrence of e (a repeated
// meeting is checked a year ahead, or until its end); nil — the room is free. The room is locked till the end of tx.
func (s *Store) roomClash(ctx context.Context, tx pgx.Tx, e Event) (*Event, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('room:' || $1))`, e.RoomID); err != nil {
		return nil, err
	}
	to := e.End
	if e.Repeat != "" {
		to = e.Start.AddDate(1, 0, 0)
		if e.Until != nil && e.Until.AddDate(0, 0, 1).Before(to) {
			to = e.Until.AddDate(0, 0, 1)
		}
	}
	if e.ID != "" && e.Repeat != "" {
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(at), '{}') FROM event_exceptions WHERE event_id::text = $1`, e.ID).Scan(&e.Skip); err != nil {
			return nil, err
		}
	}
	others, err := s.roomEvents(ctx, tx, e.RoomID, e.ID, e.Start, to)
	if err != nil {
		return nil, err
	}
	if len(others) == 0 {
		return nil, nil
	}
	var busy []Event
	for _, o := range others {
		busy = append(busy, o.expand(e.Start, to)...)
	}
	for _, mine := range e.expand(e.Start, to) {
		for _, b := range busy {
			if b.Start.Before(mine.End) && b.End.After(mine.Start) {
				return &b, nil
			}
		}
	}
	return nil, nil
}

// RoomBusy is when the room is booked between from and to; titles only of the meetings the viewer may see in full.
func (s *Store) RoomBusy(ctx context.Context, viewer, room string, from, to time.Time) (Room, []Slot, error) {
	var r Room
	err := s.db.QueryRow(ctx, `SELECT id::text, name, location, coalesce(capacity, 0) FROM meeting_rooms WHERE id::text = $1`, room).
		Scan(&r.ID, &r.Name, &r.Location, &r.Capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil, ErrNotFound
	}
	if err != nil {
		return r, nil, err
	}
	list, err := s.roomEvents(ctx, s.db, room, "", from, to)
	if err != nil {
		return r, nil, err
	}
	var slots []Slot
	for _, e := range list {
		seen := false
		if !e.Private {
			if _, err := s.Get(ctx, viewer, e.ID); err == nil {
				seen = true
			}
		}
		for _, o := range e.expand(from, to) {
			sl := Slot{Start: o.Start, End: o.End, Kind: "busy"}
			if seen {
				sl.Title, sl.EventID = o.Title, o.ID
			}
			slots = append(slots, sl)
		}
	}
	return r, slots, nil
}
