package drive

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Card is what the card of a file of a drive («Подробнее») tells of it.
type Card struct {
	File
	Drive, DriveOwner, ProjectKey, ProjectName string
	OwnerName                                  string // whose «Мой диск» it lies on
	Changer                                    string // who uploaded its current version, when not its first
	Comments                                   []Comment
}

// Comment is a comment of «Обсуждение» of a file.
type Comment struct {
	ID, Body, Author, AuthorID string
	CreatedAt                  time.Time
}

// FileCard is the file of a drive with its comments; whether the user may read it is the caller's to know.
func (s *Store) FileCard(ctx context.Context, fileID string) (Card, error) {
	var c Card
	f := &c.File
	err := s.db.QueryRow(ctx, `SELECT f.id, f.name, f.content_type, coalesce(st.name, ''), coalesce(u.name, ''),
			coalesce(f.uploaded_by::text, ''), f.size, f.created_at, coalesce(f.folder_id::text, ''), coalesce(p.name, ''),
			`+lockCols+`,
			f.drive, coalesce(f.drive_owner::text, ''), coalesce(pr.key, ''), coalesce(pr.name, ''),
			CASE WHEN f.changed_by IS DISTINCT FROM f.uploaded_by THEN coalesce(ch.name, '') ELSE '' END, coalesce(ow.name, '')
		FROM files f LEFT JOIN storages st ON st.id = f.storage_id LEFT JOIN users u ON u.id = f.uploaded_by
			LEFT JOIN users ch ON ch.id = f.changed_by `+lockJoin+`
			LEFT JOIN drive_folders p ON p.id = f.folder_id LEFT JOIN projects pr ON f.drive = 'project' AND pr.id = f.drive_owner
			LEFT JOIN users ow ON f.drive = 'user' AND ow.id = f.drive_owner
		WHERE f.id::text = $1 AND f.drive IS NOT NULL AND f.deleted_at IS NULL`, fileID).
		Scan(&f.ID, &f.Name, &f.ContentType, &f.StorageName, &f.Uploader, &f.UploaderID, &f.Size, &f.CreatedAt, &f.FolderID,
			&f.Where, &f.Locker, &f.LockerID, &f.LockedAt, &c.Drive, &c.DriveOwner, &c.ProjectKey, &c.ProjectName, &c.Changer, &c.OwnerName)
	if errors.Is(err, pgx.ErrNoRows) || isFKOrUUID(err) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.body, coalesce(u.name, ''), coalesce(c.author_id::text, ''), c.created_at
		FROM drive_file_comments c LEFT JOIN users u ON u.id = c.author_id WHERE c.file_id = $1 ORDER BY c.created_at, c.id`, f.ID)
	if err != nil {
		return c, err
	}
	c.Comments, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Comment])
	return c, err
}

// maxComment is the longest comment, in characters.
const maxComment = 10000

// AddComment adds the user's comment to the file, which they may read. The file's uploader and those who commented
// on it before hear of it — those of them who may still read the file.
func (s *Store) AddComment(ctx context.Context, userID, fileID, body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", i18n.E("err.comment_empty")
	}
	if utf8.RuneCountInString(body) > maxComment {
		body = string([]rune(body)[:maxComment])
	}
	var name, kind, owner string
	err := s.db.QueryRow(ctx, `SELECT name, drive, coalesce(drive_owner::text, '') FROM files
		WHERE id::text = $1 AND drive IS NOT NULL AND deleted_at IS NULL`, fileID).Scan(&name, &kind, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	var id string
	if err := s.db.QueryRow(ctx, `INSERT INTO drive_file_comments (file_id, author_id, body) VALUES ($1, $2, $3) RETURNING id::text`,
		fileID, userID, body).Scan(&id); err != nil {
		return "", err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id::text FROM users u WHERE u.active AND u.id <> $2 AND (
			u.id = (SELECT uploaded_by FROM files WHERE id::text = $1)
			OR u.id IN (SELECT author_id FROM drive_file_comments WHERE file_id::text = $1))`, fileID, userID)
	if err != nil {
		return id, err
	}
	whom, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return id, err
	}
	asThem := tasks.WithoutAdmin(ctx) // the commenter's admin mode is not theirs
	for _, u := range whom {
		if !s.CanReadFile(asThem, u, fileID, kind, owner) && !(kind == "user" && (u == owner || s.SharedFileTo(asThem, u, fileID))) {
			continue
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'disk_comment', $3, $4)`,
			u, userID, name, "/files/"+fileID+"/about#c-"+id); err != nil {
			return id, err
		}
	}
	return id, nil
}

// DeleteComment deletes a comment of the file: its author does, or an administrator.
func (s *Store) DeleteComment(ctx context.Context, userID, fileID, commentID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM drive_file_comments WHERE id::text = $1 AND file_id::text = $2 AND (author_id = $3 OR $4)`,
		commentID, fileID, userID, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// CommentCounts are the numbers of comments of the files that have them, by the file.
func (s *Store) CommentCounts(ctx context.Context, ids []string) (map[string]int, error) {
	out := map[string]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT file_id::text, count(*) FROM drive_file_comments WHERE file_id::text = ANY($1) GROUP BY file_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
