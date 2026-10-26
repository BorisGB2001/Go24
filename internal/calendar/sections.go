package calendar

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// One's own calendars, as «Добавить календарь» (calendar.section.*): besides the main personal calendar a
// person keeps others, each with its colour, and puts their meetings in them. Nobody else sees them: an invited person
// has the meeting in their main calendar.

// Section is one of the person's own calendars.
type Section struct {
	ID, Name, Color string
	// a calendar connected by its iCal link (see SyncLink): the link, when it was loaded last and how that went
	URL       string
	SyncedAt  *time.Time
	SyncError string
	OwnerID   string
	// a calendar of a CalDAV server connected two ways (see DavResources): the server's collection and the login
	DAV, Login string
}

// Sections are the viewer's own calendars by name.
func (s *Store) Sections(ctx context.Context, viewer string) ([]Section, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, color, source_url, synced_at, sync_error, owner_id::text, dav_url, dav_login FROM user_calendars
		WHERE owner_id = $1 ORDER BY lower(name), created_at`, viewer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Section])
}

// SaveSection creates (empty ID) or changes one of the viewer's calendars; returns its ID. A new one without a colour
// takes the next of the palette.
func (s *Store) SaveSection(ctx context.Context, viewer string, x Section) (string, error) {
	x.Name = strings.TrimSpace(x.Name)
	if x.Name == "" || len([]rune(x.Name)) > 100 {
		return "", i18n.E("err.calendar_name")
	}
	if x.Color = strings.ToLower(strings.TrimSpace(x.Color)); x.Color != "" && !colorRe.MatchString(x.Color) {
		return "", i18n.E("err.bad_form")
	}
	if x.ID == "" {
		if x.Color == "" {
			var n int
			if err := s.db.QueryRow(ctx, `SELECT count(*) FROM user_calendars WHERE owner_id = $1`, viewer).Scan(&n); err != nil {
				return "", err
			}
			x.Color = Colors[(n+1)%len(Colors)]
		}
		err := s.db.QueryRow(ctx, `INSERT INTO user_calendars (owner_id, name, color) VALUES ($1, $2, $3) RETURNING id::text`,
			viewer, x.Name, x.Color).Scan(&x.ID)
		return x.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE user_calendars SET name = $3, color = $4 WHERE id::text = $1 AND owner_id = $2`, x.ID, viewer, x.Name, x.Color)
	if err == nil && tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return x.ID, err
}

// DeleteSection removes one of the viewer's calendars with its meetings.
func (s *Store) DeleteSection(ctx context.Context, viewer, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM user_calendars WHERE id::text = $1 AND owner_id = $2`, id, viewer)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// MainColor is the colour of the viewer's main calendar ("" — that of the theme).
func (s *Store) MainColor(ctx context.Context, viewer string) (string, error) {
	var c string
	err := s.db.QueryRow(ctx, `SELECT calendar_color FROM users WHERE id = $1`, viewer).Scan(&c)
	return c, err
}

// SetMainColor sets the colour of the viewer's main calendar.
func (s *Store) SetMainColor(ctx context.Context, viewer, color string) error {
	if color = strings.ToLower(strings.TrimSpace(color)); color != "" && !colorRe.MatchString(color) {
		return i18n.E("err.bad_form")
	}
	_, err := s.db.Exec(ctx, `UPDATE users SET calendar_color = $2 WHERE id = $1`, viewer, color)
	return err
}
