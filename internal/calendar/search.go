package calendar

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"go24/internal/tasks"
)

// Presets of the calendar's search, those: the invitations not answered yet, the meetings one organizes,
// the invitations one declined.
const (
	PresetInvites   = "invites"
	PresetOrganizer = "organizer"
	PresetDeclined  = "declined"
)

var Presets = []string{PresetInvites, PresetOrganizer, PresetDeclined}

// searchLimit: the most meetings a search finds.
const searchLimit = 200

// Search is the calendar's search: the meetings the viewer sees whose title, description or place has the text, of the
// preset ("" — any; no text and no preset find nothing), in order of their start. An invitation is one not finished
// yet; a repeated meeting is found once, at its next occurrence (its first, when all are past). Someone else's private
// meeting is never found by its text.
func (s *Store) Search(ctx context.Context, viewer, text, preset string) ([]Event, error) {
	q := selectSQL + ` WHERE ` + visible
	args := []any{viewer, tasks.IsAdmin(ctx)}
	mine := `EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id = $1 AND a.status = `
	switch preset {
	case PresetInvites:
		q += ` AND e.owner_id <> $1 AND ` + mine + `'invited')
			AND ((e.repeat = '' AND e.ends_at >= now()) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= current_date)))`
	case PresetOrganizer:
		q += ` AND e.owner_id = $1`
	case PresetDeclined:
		q += ` AND ` + mine + `'declined')`
	case "":
	default:
		return nil, ErrNotFound
	}
	if text = strings.TrimSpace(text); text != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(text)+"%")
		q += ` AND (e.title ILIKE $3 OR e.description ILIKE $3 OR e.location ILIKE $3)
			AND (NOT e.private OR e.owner_id = $1 OR $2 OR EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id = $1))`
	} else if preset == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, q+` ORDER BY e.starts_at DESC LIMIT `+strconv.Itoa(searchLimit), args...)
	if err != nil {
		return nil, err
	}
	list, err := s.scan(ctx, viewer, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for i, e := range list {
		if e.Repeat == "" {
			continue
		}
		if next := e.expand(now, now.AddDate(2, 0, 0)); len(next) > 0 {
			list[i] = next[0]
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Start.Before(list[j].Start) })
	return list, nil
}

// Invitations is how many invitations the viewer has not answered yet (the counter of «Приглашения»).
func (s *Store) Invitations(ctx context.Context, viewer string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM events e JOIN event_attendees a ON a.event_id = e.id
		WHERE a.user_id = $1 AND a.status = 'invited' AND e.owner_id <> $1
		  AND ((e.repeat = '' AND e.ends_at >= now()) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= current_date)))`,
		viewer).Scan(&n)
	return n, err
}
