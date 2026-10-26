package calendar

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// A meeting in a calendar file (.ics) downloads one and imports a file into one's calendar.

// EventICS is the calendar file of one meeting as Get loads it (a repeated one — the whole series with its exceptions).
func EventICS(e Event, now time.Time) string {
	var b strings.Builder
	line := func(l string) { foldLine(&b, l) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//go24//calendar//RU")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	writeEvent(line, e, now.UTC().Format("20060102T150405Z"))
	line("END:VCALENDAR")
	return b.String()
}

// Imported is a meeting of a calendar file and its UID there.
type Imported struct {
	UID   string
	Event Event
}

// Import puts the meetings of a file into the viewer's calendar (calendarID — one of their own, "" — the main one):
// personal meetings without the people of the file; a meeting imported before (the same UID) is changed. It tells
// how many were added and changed.
func (s *Store) Import(ctx context.Context, viewer, calendarID string, list []Imported) (added, changed int, err error) {
	for _, im := range list {
		e := im.Event
		e.ProjectID, e.Company, e.RoomID, e.Video = "", false, "", false
		e.CalendarID, e.OwnerID = calendarID, viewer
		var id string
		err = s.db.QueryRow(ctx, `SELECT id::text FROM events WHERE owner_id::text = $1 AND import_uid = $2`, viewer, im.UID).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return added, changed, err
		}
		e.ID = id
		if e.ID, err = s.Save(ctx, viewer, e, nil); err != nil {
			return added, changed, err
		}
		if err = s.keepSkips(ctx, e.ID, e.Repeat, e.Skip, `UPDATE events SET import_uid = $2 WHERE id::text = $1`, im.UID); err != nil {
			return added, changed, err
		}
		if id == "" {
			added++
		} else {
			changed++
		}
	}
	return added, changed, nil
}

// keepSkips makes the deleted occurrences of an imported or synced series those of its file (the occurrences changed
// in go24 stay out of the series) and runs mark (with its args after the meeting's ID) in the same transaction.
func (s *Store) keepSkips(ctx context.Context, id, repeat string, list []time.Time, mark string, args ...any) error {
	var skip []time.Time
	for _, t := range list {
		if repeat != "" && !slices.ContainsFunc(skip, t.Equal) {
			skip = append(skip, t)
		}
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, mark, append([]any{id}, args...)...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM event_exceptions x WHERE x.event_id::text = $1
			AND NOT EXISTS (SELECT 1 FROM events c WHERE c.series_id = x.event_id AND c.series_at = x.at)`, id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO event_exceptions (event_id, at) SELECT $1::uuid, unnest($2::timestamptz[]) ON CONFLICT DO NOTHING`, id, skip)
		return err
	})
}
