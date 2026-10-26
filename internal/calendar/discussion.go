package calendar

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// ---------- «Файлы» and «Комментарии» of a meeting, as on a meeting's page ----------

// File is a file of a meeting or of one of its comments.
type File struct {
	ID, Name, ContentType string
	Size                  int64
	Uploader, UploaderID  string
	CreatedAt             time.Time
}

// Comment is a comment of the meeting's discussion with its files.
type Comment struct {
	ID, Body, Author, AuthorID string
	CreatedAt                  time.Time
	Files                      []File
}

// Discussion is what the meeting's page shows below it.
type Discussion struct {
	Files    []File // attached to the meeting itself
	Comments []Comment
}

// maxComment is the longest comment, in characters.
const maxComment = 10000

// Seen is the meeting the user sees in full (not only as «Занят» of a private one): who may read its files and
// discuss it.
func (s *Store) Seen(ctx context.Context, viewer, id string) (Event, error) {
	e, err := s.Get(ctx, viewer, id)
	if err == nil && e.Hidden {
		err = ErrNotFound
	}
	return e, err
}

// Discussion is the meeting's files and comments; whether the user sees the meeting is the caller's to know.
func (s *Store) Discussion(ctx context.Context, id string) (Discussion, error) {
	var d Discussion
	rows, err := s.db.Query(ctx, `SELECT f.id::text, f.name, f.content_type, f.size, coalesce(u.name, ''), coalesce(f.uploaded_by::text, ''),
			f.created_at, coalesce(f.event_comment_id::text, '')
		FROM files f LEFT JOIN users u ON u.id = f.uploaded_by WHERE f.event_id::text = $1 ORDER BY f.created_at, f.name`, id)
	if err != nil {
		return d, err
	}
	byComment := map[string][]File{}
	for rows.Next() {
		var f File
		var comment string
		if err := rows.Scan(&f.ID, &f.Name, &f.ContentType, &f.Size, &f.Uploader, &f.UploaderID, &f.CreatedAt, &comment); err != nil {
			rows.Close()
			return d, err
		}
		if comment == "" {
			d.Files = append(d.Files, f)
		} else {
			byComment[comment] = append(byComment[comment], f)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return d, err
	}
	rows, err = s.db.Query(ctx, `SELECT c.id::text, c.body, coalesce(u.name, ''), coalesce(c.author_id::text, ''), c.created_at
		FROM event_comments c LEFT JOIN users u ON u.id = c.author_id WHERE c.event_id::text = $1 ORDER BY c.created_at, c.id`, id)
	if err != nil {
		return d, err
	}
	for rows.Next() {
		var c Comment
		if err := rows.Scan(&c.ID, &c.Body, &c.Author, &c.AuthorID, &c.CreatedAt); err != nil {
			rows.Close()
			return d, err
		}
		c.Files = byComment[c.ID]
		d.Comments = append(d.Comments, c)
	}
	rows.Close()
	return d, rows.Err()
}

// AddComment adds the user's comment to the meeting they see; files are the names of the files the caller attaches
// to it (the text may be empty then). The organizer, the invited who did not decline and those who commented before
// hear of it — those of them who still see the meeting.
func (s *Store) AddComment(ctx context.Context, viewer, id, body string, files []string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" && len(files) == 0 {
		return "", i18n.E("err.comment_empty")
	}
	if utf8.RuneCountInString(body) > maxComment {
		body = string([]rune(body)[:maxComment])
	}
	e, err := s.Seen(ctx, viewer, id)
	if err != nil {
		return "", err
	}
	note := body
	if note == "" {
		note = "📎 " + strings.Join(files, ", ")
	}
	var cid string
	if err := s.db.QueryRow(ctx, `INSERT INTO event_comments (event_id, author_id, body) VALUES ($1, $2, $3) RETURNING id::text`,
		e.ID, viewer, body).Scan(&cid); err != nil {
		return "", err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text FROM users u WHERE u.active AND u.id <> $2 AND (
			u.id = (SELECT owner_id FROM events WHERE id = $1)
			OR u.id IN (SELECT user_id FROM event_attendees WHERE event_id = $1 AND status <> 'declined')
			OR u.id IN (SELECT author_id FROM event_comments WHERE event_id = $1))`, e.ID, viewer)
	if err != nil {
		return cid, err
	}
	whom, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return cid, err
	}
	asThem := tasks.WithoutAdmin(ctx) // the commenter's admin mode is not theirs
	for _, u := range whom {
		if _, err := s.Seen(asThem, u, e.ID); err != nil {
			continue
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body, url)
			SELECT $1, $2, 'event_comment', $3, left($4, 300), $5 WHERE NOT go24_quiet()`,
			u, viewer, e.ID, note, "/calendar/"+e.ID+"#c-"+cid); err != nil {
			return cid, err
		}
	}
	return cid, nil
}

// DeleteComment deletes a comment of the meeting with its files: its author does, or an administrator.
func (s *Store) DeleteComment(ctx context.Context, viewer, id, commentID string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		mine := `SELECT id FROM event_comments WHERE id::text = $1 AND event_id::text = $2 AND (author_id = $3 OR $4)`
		// its files leave the meeting with it (they stay their uploaders')
		if _, err := tx.Exec(ctx, `UPDATE files SET event_id = NULL, event_comment_id = NULL WHERE event_comment_id IN (`+mine+`)`,
			commentID, id, viewer, tasks.IsAdmin(ctx)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM event_comments WHERE id IN (`+mine+`)`, commentID, id, viewer, tasks.IsAdmin(ctx))
		if err == nil && tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return err
	})
}

// RemoveFile takes a file off the meeting (it stays its uploader's): the uploader does, the organizer, or an
// administrator.
func (s *Store) RemoveFile(ctx context.Context, viewer, id, fileID string) error {
	tag, err := s.db.Exec(ctx, `UPDATE files f SET event_id = NULL, event_comment_id = NULL
		WHERE f.id::text = $1 AND f.event_id::text = $2
			AND (f.uploaded_by = $3 OR $4 OR EXISTS (SELECT 1 FROM events e WHERE e.id = f.event_id AND e.owner_id = $3))`,
		fileID, id, viewer, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}
