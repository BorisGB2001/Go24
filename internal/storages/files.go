package storages

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// File is a stored file: a personal upload or a task attachment.
type File struct {
	ID          string
	Name        string
	Size        int64
	ContentType string
	CreatedAt   time.Time
	StorageName string // "" = built-in storage
	TaskKey     string // "" = not attached to a task
	Uploader    string
}

const fileSelect = `
	SELECT f.id, f.name, f.size, f.content_type, f.created_at, coalesce(s.name, ''),
	       coalesce(p.key || '-' || t.number, ''), coalesce(u.name, '')
	FROM files f
	LEFT JOIN storages s ON s.id = f.storage_id
	LEFT JOIN tasks t ON t.id = f.task_id
	LEFT JOIN projects p ON p.id = t.project_id
	LEFT JOIN users u ON u.id = f.uploaded_by`

// NewFile describes a file that has just been stored.
type NewFile struct {
	Key, Name, ContentType string
	Size                   int64
	StorageID              string // "" = built-in
	TaskID                 string // "" = personal upload
	PostID                 string // a feed post's attachment
	CommentID              string // of a comment of that post
	Drive, DriveOwner      string // «Диск»: user | company | project and whose ("" — not on a drive)
	FolderID               string
	MailID                 string // an attachment of a letter
	ContentID              string // a picture of a letter's text (cid:), not an attachment
	KBSpaceID              string // a picture or a file of a knowledge base's page
	SiteID                 string // a picture of a site's page («Сайты»): open to everyone
	EventID                string // a file of a meeting of the calendar
	EventCommentID         string // of a comment of that meeting
	UploadedBy             string
	LegacyID               int // 0 = not imported
}

func (s *Store) AddFile(ctx context.Context, f NewFile) (string, error) {
	var storageID, taskID, postID *string
	if f.StorageID != "" {
		storageID = &f.StorageID
	}
	if f.TaskID != "" {
		taskID = &f.TaskID
	}
	if f.PostID != "" {
		postID = &f.PostID
	}
	opt := func(v string) *string {
		if v == "" {
			return nil
		}
		return &v
	}
	var legacyID *int
	if f.LegacyID != 0 {
		legacyID = &f.LegacyID
	}
	var id string
	err := s.db.QueryRow(ctx, `
		INSERT INTO files (object_key, name, size, content_type, uploaded_by, storage_id, task_id, legacy_id, post_id, drive, drive_owner, folder_id, mail_id, content_id, kb_space_id, site_id, feed_comment_id, event_id, event_comment_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19) RETURNING id`,
		f.Key, f.Name, f.Size, f.ContentType, f.UploadedBy, storageID, taskID, legacyID, postID, opt(f.Drive), opt(f.DriveOwner), opt(f.FolderID), opt(f.MailID), f.ContentID, opt(f.KBSpaceID), opt(f.SiteID), opt(f.CommentID), opt(f.EventID), opt(f.EventCommentID)).Scan(&id)
	return id, err
}

// ListFiles: the user's own uploads, or every file in admin mode.
func (s *Store) ListFiles(ctx context.Context, userID string, admin bool) ([]File, error) {
	rows, err := s.db.Query(ctx, fileSelect+` WHERE f.uploaded_by = $1 OR $2 ORDER BY f.created_at DESC LIMIT 200`, userID, admin)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[File])
}

// TaskFiles lists a task's attachments; the caller has already checked that the task is visible.
func (s *Store) TaskFiles(ctx context.Context, taskID string) ([]File, error) {
	rows, err := s.db.Query(ctx, fileSelect+` WHERE f.task_id = $1 ORDER BY f.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[File])
}

// FileMeta is what a download needs to decide access and find the bytes.
type FileMeta struct {
	Key, Name, ContentType string
	Size                   int64
	StorageID              *string
	TaskID                 *string
	PostID                 *string
	Drive, DriveOwner      string // "" — not on a drive
	MailID                 string // "" — not a letter's attachment
	KBSpaceID              string // "" — not of a knowledge base
	SiteID                 string // "" — not of a site
	EventID                string // "" — not of a meeting
	UploadedBy             *string
}

func (s *Store) FileMeta(ctx context.Context, id string) (FileMeta, error) {
	var m FileMeta
	err := s.db.QueryRow(ctx, `SELECT object_key, name, content_type, size, storage_id, task_id, post_id, coalesce(drive, ''),
		coalesce(drive_owner::text, ''), coalesce(mail_id::text, ''), coalesce(kb_space_id::text, ''), coalesce(site_id::text, ''), coalesce(event_id::text, ''), uploaded_by FROM files WHERE id = $1`, id).
		Scan(&m.Key, &m.Name, &m.ContentType, &m.Size, &m.StorageID, &m.TaskID, &m.PostID, &m.Drive, &m.DriveOwner, &m.MailID, &m.KBSpaceID, &m.SiteID, &m.EventID, &m.UploadedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}
