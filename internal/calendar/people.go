package calendar

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Calendars of colleagues, as «Календари сотрудников»: the calendar of a colleague is opened on its own
// (?user=) or added to one's panel and shown over one's own calendar in its colour. What colleagues see of one's
// meetings is one's choice (Shares): only the busy time, or the meetings in full; a private meeting is only «Занят»
// to them, a meeting the viewer is in is seen as usual. Guests from outside have none of it.

// Shares: what colleagues see in one's calendar — the busy time only (the default or the meetings.
var Shares = []string{"busy", "full"}

// shared: a meeting (not private) of someone (its organizer or one who has not declined it) who shows their meetings
// in full to colleagues; $1 — the viewer, an employee.
const shared = `(NOT e.private AND NOT EXISTS (SELECT 1 FROM users g WHERE g.id = $1 AND g.guest)
	AND EXISTS (SELECT 1 FROM users o WHERE o.calendar_share = 'full' AND o.active AND NOT o.guest AND (o.id = e.owner_id
		OR EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id = o.id AND a.status <> 'declined'))))`

// Colleague is a colleague whose calendar is in the viewer's panel.
type Colleague struct {
	ID, Name, Color string
}

// colleague: the viewer and the person are employees (not guests), the person is active and not the viewer.
func (s *Store) colleague(ctx context.Context, viewer, person string) error {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users v, users p WHERE v.id = $1 AND p.id::text = $2
		AND NOT v.guest AND NOT p.guest AND p.active AND p.id <> v.id)`, viewer, person).Scan(&ok)
	if err == nil && !ok {
		return ErrNotFound
	}
	return err
}

// People are the colleagues whose calendars the viewer has added, by name.
func (s *Store) People(ctx context.Context, viewer string) ([]Colleague, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, f.color FROM calendar_follows f JOIN users u ON u.id = f.target_id
		WHERE f.user_id = $1 AND u.active AND NOT u.guest ORDER BY lower(u.name)`, viewer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Colleague, error) {
		var x Colleague
		err := r.Scan(&x.ID, &x.Name, &x.Color)
		return x, err
	})
}

// AddPerson adds a colleague's calendar to the viewer's panel (again — changes its colour); without a colour it takes
// the next of the palette.
func (s *Store) AddPerson(ctx context.Context, viewer, person, color string) error {
	if err := s.colleague(ctx, viewer, person); err != nil {
		return err
	}
	if color != "" && !colorRe.MatchString(color) {
		return i18n.E("err.bad_form")
	}
	if color == "" {
		var n int
		if err := s.db.QueryRow(ctx, `SELECT count(*) FROM calendar_follows WHERE user_id = $1`, viewer).Scan(&n); err != nil {
			return err
		}
		color = Colors[(n+3)%len(Colors)]
	}
	_, err := s.db.Exec(ctx, `INSERT INTO calendar_follows (user_id, target_id, color) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, target_id) DO UPDATE SET color = excluded.color`, viewer, person, color)
	return err
}

// RemovePerson takes a colleague's calendar off the viewer's panel.
func (s *Store) RemovePerson(ctx context.Context, viewer, person string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM calendar_follows WHERE user_id = $1 AND target_id::text = $2`, viewer, person)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Share is what colleagues see in the viewer's calendar, one of Shares.
func (s *Store) Share(ctx context.Context, viewer string) (string, error) {
	var v string
	err := s.db.QueryRow(ctx, `SELECT calendar_share FROM users WHERE id = $1`, viewer).Scan(&v)
	return v, err
}

// SetShare sets what colleagues see in the viewer's calendar.
func (s *Store) SetShare(ctx context.Context, viewer, share string) error {
	if share != "busy" && share != "full" {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE users SET calendar_share = $2 WHERE id = $1`, viewer, share)
	return err
}

// PersonName is the name of a colleague whose calendar the viewer may open.
func (s *Store) PersonName(ctx context.Context, viewer, person string) (string, error) {
	if err := s.colleague(ctx, viewer, person); err != nil {
		return "", err
	}
	var name string
	err := s.db.QueryRow(ctx, `SELECT name FROM users WHERE id::text = $1`, person).Scan(&name)
	return name, err
}

// PersonRange is the calendar of a colleague between from and to as the viewer sees it: the meetings the colleague
// organizes or has not declined. Those the viewer may see anyway (they are in them, the company's, their project's)
// and, when the colleague shows their meetings, the ones not private are in full; the others are only the busy time.
func (s *Store) PersonRange(ctx context.Context, viewer, person string, from, to time.Time) ([]Event, error) {
	if err := s.colleague(ctx, viewer, person); err != nil {
		return nil, err
	}
	share, err := s.Share(ctx, person)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.starts_at < $2
		AND ((e.repeat = '' AND e.ends_at >= $1) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $1::date)))
		AND (e.owner_id::text = $3 OR EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id::text = $3 AND a.status <> 'declined'))
		ORDER BY e.starts_at`, from, to, person)
	if err != nil {
		return nil, err
	}
	series, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{} // the meetings the viewer sees anyway
	if len(series) > 0 {
		ids := make([]string, len(series))
		for i, e := range series {
			ids[i] = e.ID
		}
		vrows, err := s.db.Query(ctx, `SELECT e.id::text FROM events e WHERE e.id = ANY($3::uuid[]) AND `+visible, viewer, tasks.IsAdmin(ctx), ids)
		if err != nil {
			return nil, err
		}
		got, err := pgx.CollectRows(vrows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, id := range got {
			seen[id] = true
		}
	}
	var list []Event
	for _, e := range series {
		if !seen[e.ID] && (share != "full" || e.Private) {
			e.Hidden, e.Title, e.Description, e.Where, e.Attendees, e.RoomID, e.RoomName, e.Video = true, "", "", "", nil, "", "", false
		}
		e.Of = person
		list = append(list, e.expand(from, to)...)
	}
	sortEvents(list)
	return list, nil
}

// WithPeople adds to the viewer's calendar the meetings of the colleagues of their panel, each in the colour of its
// calendar; a meeting already there is not repeated.
func (s *Store) WithPeople(ctx context.Context, viewer string, list []Event, from, to time.Time) ([]Event, error) {
	people, err := s.People(ctx, viewer)
	if err != nil || len(people) == 0 {
		return list, err
	}
	seen := map[string]bool{}
	for _, e := range list {
		seen[e.ID+e.Start.String()] = true
	}
	for _, p := range people {
		theirs, err := s.PersonRange(ctx, viewer, p.ID, from, to)
		if err != nil {
			return nil, err
		}
		for _, e := range theirs {
			if k := e.ID + e.Start.String(); !seen[k] {
				seen[k], e.CalColor = true, p.Color
				list = append(list, e)
			}
		}
	}
	sortEvents(list)
	return list, nil
}
