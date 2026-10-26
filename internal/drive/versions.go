package drive

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Version is one state of a file: the current one (ID "") or one it had before.
type Version struct {
	ID          string
	No          int
	Size        int64
	ContentType string
	Author      string
	AuthorID    string
	CreatedAt   time.Time
}

// NewVersion is the stored bytes of a file uploaded again.
type NewVersion struct {
	Key, ContentType, StorageID string
	Size                        int64
}

// Same is the file of the folder ("" — the top) with the name that the user may upload again: its id, "" if none.
func (s *Store) Same(ctx context.Context, userID string, d Drive, folderID, name string) (string, error) {
	folderID = d.At(folderID)
	var id, by string
	err := s.db.QueryRow(ctx, `SELECT id::text, coalesce(uploaded_by::text, '') FROM files
		WHERE drive = $1 AND drive_owner IS NOT DISTINCT FROM $2 AND folder_id IS NOT DISTINCT FROM $3 AND name = $4
			AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1`, d.Kind, d.owner(), nullable(folderID), name).Scan(&id, &by)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !d.mayChange(userID, by)) {
		return "", nil
	}
	return id, err
}

// AddVersion makes the bytes the file's current version; what it was becomes a version.
func (s *Store) AddVersion(ctx context.Context, userID string, d Drive, id string, v NewVersion) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.changeFile(ctx, tx, userID, d, id); err != nil {
		return err
	}
	if err := s.keepVersion(ctx, tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE files SET object_key = $2, storage_id = $3, size = $4, content_type = $5,
		changed_by = $6, created_at = now() WHERE id::text = $1`, id, v.Key, nullable(v.StorageID), v.Size, v.ContentType, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// keepVersion saves the file's current bytes as a version; beyond the versions kept the oldest go.
func (s *Store) keepVersion(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO drive_file_versions (file_id, object_key, storage_id, size, content_type, uploaded_by, created_at)
		SELECT id, object_key, storage_id, size, content_type, coalesce(changed_by, uploaded_by), created_at FROM files WHERE id::text = $1`, id); err != nil {
		return err
	}
	l, err := s.Limits(ctx)
	if err != nil {
		return err
	}
	return trimVersions(ctx, tx, id, l.Versions)
}

// Versions is the file's history, the current version first.
func (s *Store) Versions(ctx context.Context, fileID string) ([]Version, error) {
	rows, err := s.db.Query(ctx, `SELECT * FROM (
		SELECT '' AS id, f.size, f.content_type, coalesce(u.name, ''), coalesce(u.id::text, ''), f.created_at
		FROM files f LEFT JOIN users u ON u.id = coalesce(f.changed_by, f.uploaded_by) WHERE f.id::text = $1
		UNION ALL
		SELECT v.id::text, v.size, v.content_type, coalesce(u.name, ''), coalesce(u.id::text, ''), v.created_at
		FROM drive_file_versions v LEFT JOIN users u ON u.id = v.uploaded_by WHERE v.file_id::text = $1
		) x ORDER BY id = '' DESC, created_at DESC`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Version
	for rows.Next() {
		var v Version
		if err := rows.Scan(&v.ID, &v.Size, &v.ContentType, &v.Author, &v.AuthorID, &v.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, v)
	}
	for i := range list {
		list[i].No = len(list) - i
	}
	return list, rows.Err()
}

// VersionObject is where the bytes of a version of the file are.
func (s *Store) VersionObject(ctx context.Context, fileID, versionID string) (Entry, error) {
	var e Entry
	err := s.db.QueryRow(ctx, `SELECT object_key, storage_id, size, content_type, created_at FROM drive_file_versions
		WHERE id::text = $1 AND file_id::text = $2`, versionID, fileID).Scan(&e.Key, &e.StorageID, &e.Size, &e.ContentType, &e.ModTime)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// RestoreVersion brings a version back as the file's current one; the current one becomes a version too.
func (s *Store) RestoreVersion(ctx context.Context, userID string, d Drive, id, versionID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.changeFile(ctx, tx, userID, d, id); err != nil {
		return err
	}
	var v NewVersion
	var storageID *string
	err = tx.QueryRow(ctx, `SELECT object_key, storage_id, size, content_type FROM drive_file_versions WHERE id::text = $1 AND file_id::text = $2`,
		versionID, id).Scan(&v.Key, &storageID, &v.Size, &v.ContentType)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := s.keepVersion(ctx, tx, id); err != nil {
		return err
	}
	// the object stays shared with the version it came from: stored objects are never deleted one by one
	if _, err := tx.Exec(ctx, `UPDATE files SET object_key = $2, storage_id = $3, size = $4, content_type = $5,
		changed_by = $6, created_at = now() WHERE id::text = $1`, id, v.Key, storageID, v.Size, v.ContentType, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MayChange: the user may change the file of the drive (upload a version of it, edit it online).
func (s *Store) MayChange(ctx context.Context, userID string, d Drive, id string) (bool, error) {
	err := s.changeFile(ctx, s.db, userID, d, id)
	if errors.Is(err, ErrForbidden) || errors.Is(err, ErrLocked) {
		return false, nil
	}
	return err == nil, err
}
