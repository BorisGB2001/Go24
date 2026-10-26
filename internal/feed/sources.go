package feed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Events of other sections in the feed: «Приветствуем нового сотрудника», a meeting of the calendar
// (its people see it), a new page of the knowledge base. Each is a post — with comments, reactions, «Следить» — of the
// one it is about (the new employee, the organizer, the author of the page); the rights of the feed do not apply.
const (
	SourceWelcome = "welcome"
	SourceMeeting = "meeting"
	SourceKB      = "kb"
)

// Announcement is the event a post is made of.
type Announcement struct {
	Source, SourceID string
	AuthorID         string
	ProjectID        string   // a project's (its members see it)
	To               []string // only these people and the author see it; none and no project — all employees
	Title            string
	Data             map[string]string // what the post shows of it (Post.SourceData)
}

// Announce makes the post of an event, or brings the one made before up to date (its title, data and people: those
// added see it, no one is told — the section itself tells of its things). The time of the post stays.
func (s *Store) Announce(ctx context.Context, a Announcement) (string, error) {
	data, err := json.Marshal(a.Data)
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(a.Title)
	if r := []rune(title); len(r) > 300 {
		title = string(r[:300])
	}
	body := title // for the search; the post shows the event itself
	if body == "" {
		body = a.Source
	}
	var project any
	if a.ProjectID != "" {
		project = a.ProjectID
	}
	limited := len(a.To) > 0 && a.ProjectID == ""
	var id string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO feed_posts (author_id, project_id, title, body, limited, source, source_id, source_data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (source, source_id) WHERE source <> '' DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body,
				source_data = EXCLUDED.source_data, updated_at = now()
			RETURNING id`, a.AuthorID, project, title, body, limited, a.Source, a.SourceID, data).Scan(&id); err != nil {
			return err
		}
		if !limited {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, user_id)
			SELECT $1, u.id FROM users u WHERE u.id::text = ANY($2::text[]) AND u.active AND NOT u.guest
			  AND NOT EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = $1 AND r.user_id = u.id)`, id, a.To)
		return err
	})
	return id, err
}

// Withdraw removes the post of an event (the meeting or the page was deleted).
func (s *Store) Withdraw(ctx context.Context, source, sourceID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM feed_posts WHERE source = $1 AND source_id = $2 AND source <> ''`, source, sourceID)
	return err
}

// Announced is the post of an event ("" — none).
func (s *Store) Announced(ctx context.Context, source, sourceID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM feed_posts WHERE source = $1 AND source_id = $2 AND source <> ''`, source, sourceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SourceTime is a time of the event's data (RFC 3339; zero — none).
func (p *Post) SourceTime(key string) time.Time {
	t, _ := time.Parse(time.RFC3339, p.SourceData[key])
	return t
}
