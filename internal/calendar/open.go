package calendar

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Открытые события» of the calendar: events of a category every employee (not a guest from outside) sees
// and joins without an invitation; the new ones of a category come to everyone as a notification, unless they muted it.

// openEvent: the viewer ($1) is an employee and the meeting is an open event.
const openEvent = `(e.category_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM users g WHERE g.id = $1 AND g.guest))`

// Category is a category of open events.
type Category struct {
	ID, Name, Description string
	CreatedBy, Creator    string
	Closed                bool // only its creator (and admins) add events
	Muted                 bool // the viewer does not hear of its new events
	Upcoming              int  // its events that have not ended yet
	Mine                  bool // the viewer may change it
}

// Coming is how many come to the meeting: its organizer and those invited who have not declined.
func (e Event) Coming() int {
	n := 1
	for _, a := range e.Attendees {
		if a.Status != "declined" {
			n++
		}
	}
	return n
}

// Categories lists the categories of open events, by name.
func (s *Store) Categories(ctx context.Context, viewer string) ([]Category, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.name, c.description, coalesce(c.created_by::text, ''), coalesce(u.name, ''), c.closed,
		EXISTS (SELECT 1 FROM event_category_mutes m WHERE m.category_id = c.id AND m.user_id::text = $1),
		(SELECT count(*) FROM events e WHERE e.category_id = c.id AND (e.ends_at > now() OR e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= now()::date)))
		FROM event_categories c LEFT JOIN users u ON u.id = c.created_by ORDER BY lower(c.name), c.created_at`, viewer)
	if err != nil {
		return nil, err
	}
	admin := tasks.IsAdmin(ctx)
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Category, error) {
		var c Category
		err := r.Scan(&c.ID, &c.Name, &c.Description, &c.CreatedBy, &c.Creator, &c.Closed, &c.Muted, &c.Upcoming)
		c.Mine = c.CreatedBy == viewer || admin
		return c, err
	})
}

// guest: the person is a guest from outside (no open events for them).
func (s *Store) guest(ctx context.Context, viewer string) (bool, error) {
	var g bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1 AND guest)`, viewer).Scan(&g)
	return g, err
}

// SaveCategory adds a category (any employee) or changes one (its creator, an admin); returns its ID.
func (s *Store) SaveCategory(ctx context.Context, viewer string, c Category) (string, error) {
	c.Name, c.Description = strings.TrimSpace(c.Name), strings.TrimSpace(c.Description)
	if c.Name == "" || len([]rune(c.Name)) > 100 || len([]rune(c.Description)) > 2000 {
		return "", i18n.E("err.bad_form")
	}
	if g, err := s.guest(ctx, viewer); err != nil || g {
		return "", errors.Join(err, ErrForbidden)
	}
	if c.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO event_categories (name, description, created_by, closed) VALUES ($1, $2, $3, $4) RETURNING id::text`,
			c.Name, c.Description, viewer, c.Closed).Scan(&c.ID)
		return c.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE event_categories SET name = $2, description = $3, closed = $4
		WHERE id::text = $1 AND (created_by::text = $5 OR $6)`, c.ID, c.Name, c.Description, c.Closed, viewer, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrForbidden
	}
	return c.ID, err
}

// DeleteCategory removes a category (its creator, an admin); its events stay as ordinary meetings of their organizers.
func (s *Store) DeleteCategory(ctx context.Context, viewer, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM event_categories WHERE id::text = $1 AND (created_by::text = $2 OR $3)`, id, viewer, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrForbidden
	}
	return err
}

// MuteCategory: the viewer does not hear of the new events of the category (on — false: hears again).
func (s *Store) MuteCategory(ctx context.Context, viewer, id string, on bool) error {
	if !uuidRe.MatchString(id) {
		return ErrNotFound
	}
	q := `DELETE FROM event_category_mutes WHERE category_id = $1 AND user_id = $2`
	if on {
		q = `INSERT INTO event_category_mutes (category_id, user_id) SELECT id, $2 FROM event_categories WHERE id = $1 ON CONFLICT DO NOTHING`
	}
	_, err := s.db.Exec(ctx, q, id, viewer)
	return err
}

// mayOpen: the viewer may put the meeting e into its category — an employee, and the category is not a closed one of
// someone else (a meeting already there stays).
func (s *Store) mayOpen(ctx context.Context, viewer string, e Event) error {
	if !uuidRe.MatchString(e.CategoryID) {
		return i18n.E("err.bad_form")
	}
	var found, may bool
	err := s.db.QueryRow(ctx, `SELECT true, NOT c.closed OR c.created_by::text = $2 OR $3
			OR EXISTS (SELECT 1 FROM events e WHERE e.id::text = $4 AND e.category_id = c.id)
		FROM event_categories c WHERE c.id = $1`, e.CategoryID, viewer, tasks.IsAdmin(ctx), e.ID).Scan(&found, &may)
	if errors.Is(err, pgx.ErrNoRows) {
		return i18n.E("err.bad_form")
	}
	if err != nil {
		return err
	}
	if g, err := s.guest(ctx, viewer); err != nil || g || !may {
		return errors.Join(err, ErrForbidden)
	}
	return nil
}

// announceOpen tells everyone of the new open event id: the employees but its organizer, those invited (they have
// the invitation) and those who muted its category.
func (s *Store) announceOpen(ctx context.Context, tx pgx.Tx, id, viewer, category, title string) error {
	_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body)
		SELECT u.id, $2, 'event_open', $1, $4 FROM users u
		WHERE u.active AND NOT u.guest AND u.id <> $2 AND NOT go24_quiet()
			AND NOT EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = $1 AND a.user_id = u.id)
			AND NOT EXISTS (SELECT 1 FROM event_category_mutes m WHERE m.category_id::text = $3 AND m.user_id = u.id)`,
		id, viewer, category, title)
	return err
}

// OpenEvents are the occurrences of the open events (of one category, "" — of all) that touch [from, to), by time.
func (s *Store) OpenEvents(ctx context.Context, viewer, category string, from, to time.Time) ([]Event, error) {
	if g, err := s.guest(ctx, viewer); err != nil || g {
		return nil, err
	}
	rows, err := s.db.Query(ctx, selectSQL+` WHERE e.category_id IS NOT NULL AND ($1 = '' OR e.category_id::text = $1)
		AND (e.repeat = '' AND e.starts_at < $3 AND e.ends_at >= $2 OR e.repeat <> '' AND e.starts_at < $3
			AND (e.repeat_until IS NULL OR e.repeat_until >= $2::date)) ORDER BY e.starts_at`, category, from, to)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, e := range list {
		out = append(out, e.expand(from, to)...)
	}
	sortEvents(out)
	return out, nil
}

// Join: the viewer comes to the open event id (join — false: does not come any more); its organizer hears of it.
func (s *Store) Join(ctx context.Context, viewer, id string, join bool) error {
	if g, err := s.guest(ctx, viewer); err != nil || g {
		return errors.Join(err, ErrForbidden)
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var owner string
		err := tx.QueryRow(ctx, `SELECT owner_id::text FROM events WHERE id::text = $1 AND category_id IS NOT NULL`, id).Scan(&owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || owner == viewer {
			return err
		}
		status := "declined"
		if join {
			_, err = tx.Exec(ctx, `INSERT INTO event_attendees (event_id, user_id, status) VALUES ($1, $2, 'accepted')
				ON CONFLICT (event_id, user_id) DO UPDATE SET status = 'accepted'`, id, viewer)
			status = "accepted"
		} else {
			_, err = tx.Exec(ctx, `DELETE FROM event_attendees WHERE event_id::text = $1 AND user_id = $2`, id, viewer)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body)
			SELECT $3, $2, 'event_reply', $1, $4 WHERE NOT go24_quiet()`, id, viewer, owner, status)
		return err
	})
}
