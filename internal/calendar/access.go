package calendar

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Access to one's calendar for colleagues by name, as «Доступ» of a calendar: over what all colleagues
// see (Shares) one gives a person «Просмотр» — the meetings in full — or «Управление»: they keep the calendar for one,
// as a secretary does — make meetings in it (one is their organizer), change and delete one's meetings, answer one's
// invitations. Private meetings stay only the busy time to them. The one who keeps the calendar works with the store
// as its owner, with the context of AsDelegate.

// Levels of access to one's calendar.
var Levels = []string{"view", "edit"}

// Grant is a person's access to a calendar: in Grants — the colleague given access, in Granted — the owner of the
// calendar.
type Grant struct {
	UserID, Name, Level string
}

// Grants are the colleagues the owner gave access to their calendar, by name.
func (s *Store) Grants(ctx context.Context, owner string) ([]Grant, error) {
	return s.grants(ctx, `SELECT u.id::text, u.name, a.level FROM calendar_access a JOIN users u ON u.id = a.user_id
		WHERE a.owner_id = $1 AND u.active AND NOT u.guest ORDER BY lower(u.name)`, owner)
}

// Granted are the calendars of colleagues the viewer was given access to, by the owner's name.
func (s *Store) Granted(ctx context.Context, viewer string) ([]Grant, error) {
	return s.grants(ctx, `SELECT u.id::text, u.name, a.level FROM calendar_access a JOIN users u ON u.id = a.owner_id
		WHERE a.user_id = $1 AND u.active AND NOT u.guest ORDER BY lower(u.name)`, viewer)
}

func (s *Store) grants(ctx context.Context, q, id string) ([]Grant, error) {
	rows, err := s.db.Query(ctx, q, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Grant, error) {
		var g Grant
		err := r.Scan(&g.UserID, &g.Name, &g.Level)
		return g, err
	})
}

// SetGrant gives a colleague access to the owner's calendar (one of Levels) or takes it away (level "").
func (s *Store) SetGrant(ctx context.Context, owner, user, level string) error {
	if level == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM calendar_access WHERE owner_id = $1 AND user_id::text = $2`, owner, user)
		return err
	}
	if level != "view" && level != "edit" {
		return i18n.E("err.bad_form")
	}
	if err := s.colleague(ctx, owner, user); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO calendar_access (owner_id, user_id, level) VALUES ($1, $2, $3)
		ON CONFLICT (owner_id, user_id) DO UPDATE SET level = excluded.level`, owner, user, level)
	return err
}

// Level is the viewer's access to the owner's calendar: "", "view" or "edit".
func (s *Store) Level(ctx context.Context, viewer, owner string) (string, error) {
	var level string
	err := s.db.QueryRow(ctx, `SELECT a.level FROM calendar_access a JOIN users u ON u.id = a.owner_id
		WHERE a.user_id = $1 AND a.owner_id::text = $2 AND u.active`, viewer, owner).Scan(&level)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return level, err
}

// Manages: the viewer keeps the owner's calendar («Управление»).
func (s *Store) Manages(ctx context.Context, viewer, owner string) (bool, error) {
	level, err := s.Level(ctx, viewer, owner)
	return level == "edit", err
}

type delegateKey struct{}

// AsDelegate: the store is worked with as the owner of a calendar by delegate, who keeps it for them; the private
// meetings of the owner are hidden from the delegate and stay out of their reach.
func AsDelegate(ctx context.Context, delegate string) context.Context {
	return context.WithValue(ctx, delegateKey{}, delegate)
}

// delegateOf is the one who keeps the calendar for its owner ("" — the owner themselves).
func delegateOf(ctx context.Context) string {
	d, _ := ctx.Value(delegateKey{}).(string)
	return d
}

// attends: the person is invited to the meeting.
func (e Event) attends(person string) bool {
	for _, a := range e.Attendees {
		if a.ID == person {
			return true
		}
	}
	return false
}

// hiddenFrom: a private meeting the delegate is not in (its organizer or invited) is only the busy time to them.
func (e Event) hiddenFrom(delegate string) bool {
	return e.Private && delegate != "" && e.OwnerID != delegate && !e.attends(delegate)
}
