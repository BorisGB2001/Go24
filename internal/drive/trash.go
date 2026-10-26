package drive

import (
	"context"
	"fmt"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Trashed is a file or a folder in the trash of a drive: where it was, who deleted it and when.
type Trashed struct {
	ID, Name, Kind string // Kind: file | folder
	Where          string // the folder it was in ("" — the top)
	DeletedBy      string
	DeletedByID    string
	DeletedAt      time.Time
	Size           int64
	PurgeAt        *time.Time // when it goes for good (nil — kept until deleted by hand)
}

// Trash lists what was deleted on the drive (a folder's contents go with it and are not listed apart).
func (s *Store) Trash(ctx context.Context, d Drive) ([]Trashed, error) {
	if d.Root != "" { // the trash is the owner's
		return nil, ErrForbidden
	}
	rows, err := s.db.Query(ctx, `
		SELECT f.id::text, f.name, 'folder', coalesce(p.name, ''), coalesce(u.name, ''), coalesce(f.deleted_by::text, ''), f.deleted_at, 0::bigint
		FROM drive_folders f LEFT JOIN drive_folders p ON p.id = f.parent_id LEFT JOIN users u ON u.id = f.deleted_by
		WHERE f.drive = $1 AND f.owner_id IS NOT DISTINCT FROM $2 AND f.deleted_at IS NOT NULL AND `+fmt.Sprintf(visibleSQL, "f.id", "$3")+`
		UNION ALL
		SELECT f.id::text, f.name, 'file', coalesce(p.name, ''), coalesce(u.name, ''), coalesce(f.deleted_by::text, ''), f.deleted_at, f.size
		FROM files f LEFT JOIN drive_folders p ON p.id = f.folder_id LEFT JOIN users u ON u.id = f.deleted_by
		WHERE f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2 AND f.deleted_at IS NOT NULL AND `+fmt.Sprintf(visibleSQL, "f.folder_id", "$3")+`
		ORDER BY 7 DESC`, d.Kind, d.owner(), d.viewerArg())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Trashed, error) {
		var t Trashed
		err := row.Scan(&t.ID, &t.Name, &t.Kind, &t.Where, &t.DeletedBy, &t.DeletedByID, &t.DeletedAt, &t.Size)
		return t, err
	})
	if err != nil {
		return nil, err
	}
	if days, err := s.tasks.TrashDays(ctx); err == nil && days > 0 {
		for i := range list {
			at := list[i].DeletedAt.AddDate(0, 0, days)
			list[i].PurgeAt = &at
		}
	}
	return list, nil
}

// MayRestore: the user may restore or delete for good a thing of the trash deleted by `by`.
func (d Drive) MayRestore(userID, by string) bool { return d.mayChange(userID, by) }

// trashed finds a deleted file or folder of the drive and who deleted it.
func (s *Store) trashed(ctx context.Context, d Drive, kind, id string) (by string, err error) {
	q := `SELECT coalesce(deleted_by::text, '') FROM files WHERE id::text = $1 AND drive = $2 AND drive_owner IS NOT DISTINCT FROM $3
		AND deleted_at IS NOT NULL AND ` + fmt.Sprintf(visibleSQL, "folder_id", "$4")
	if kind == "folder" {
		q = `SELECT coalesce(deleted_by::text, '') FROM drive_folders WHERE id::text = $1 AND drive = $2 AND owner_id IS NOT DISTINCT FROM $3
			AND deleted_at IS NOT NULL AND ` + fmt.Sprintf(visibleSQL, "id", "$4")
	}
	err = s.db.QueryRow(ctx, q, id, d.Kind, d.owner(), d.viewerArg()).Scan(&by)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return by, err
}

// Restore takes files and folders out of the trash back to where they were; when that folder is gone (in the trash
// itself) — to the top of the drive. A folder that meets a namesake there is «name (2)».
func (s *Store) Restore(ctx context.Context, userID string, d Drive, sel Selection) error {
	if d.Root != "" { // the trash is the owner's
		return ErrForbidden
	}
	for _, id := range sel.Files {
		by, err := s.trashed(ctx, d, "file", id)
		if err != nil {
			return err
		}
		if !d.MayRestore(userID, by) {
			return ErrForbidden
		}
		var folder string
		if err := s.db.QueryRow(ctx, `SELECT coalesce(folder_id::text, '') FROM files WHERE id::text = $1`, id).Scan(&folder); err != nil {
			return err
		}
		if folder != "" {
			if _, err := s.path(ctx, d, folder); err != nil {
				folder = ""
			}
		}
		if _, err := s.db.Exec(ctx, `UPDATE files SET deleted_at = NULL, deleted_by = NULL, folder_id = $2 WHERE id::text = $1`,
			id, nullable(folder)); err != nil {
			return err
		}
	}
	for _, id := range sel.Folders {
		by, err := s.trashed(ctx, d, "folder", id)
		if err != nil {
			return err
		}
		if !d.MayRestore(userID, by) {
			return ErrForbidden
		}
		var parent, name string
		if err := s.db.QueryRow(ctx, `SELECT coalesce(parent_id::text, ''), name FROM drive_folders WHERE id = $1`, id).Scan(&parent, &name); err != nil {
			return err
		}
		if parent != "" {
			if _, err := s.path(ctx, d, parent); err != nil {
				parent = ""
			}
		}
		rows, err := s.db.Query(ctx, `SELECT lower(name) FROM drive_folders WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2
			AND parent_id IS NOT DISTINCT FROM $3 AND deleted_at IS NULL`, d.Kind, d.owner(), nullable(parent))
		if err != nil {
			return err
		}
		taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `UPDATE drive_folders SET deleted_at = NULL, deleted_by = NULL, parent_id = $2, name = $3 WHERE id = $1`,
			id, nullable(parent), freeName(name, taken, false)); err != nil {
			return folderErr(err)
		}
	}
	return nil
}

// Purge deletes files and folders of the trash for good (a folder with all it holds). The rows go; the bytes are
// left to the storage's own cleanup.
func (s *Store) Purge(ctx context.Context, userID string, d Drive, sel Selection) error {
	if d.Root != "" { // the trash is the owner's
		return ErrForbidden
	}
	for _, id := range sel.Files {
		by, err := s.trashed(ctx, d, "file", id)
		if err != nil {
			return err
		}
		if !d.MayRestore(userID, by) {
			return ErrForbidden
		}
		if _, err := s.db.Exec(ctx, `DELETE FROM files WHERE id::text = $1`, id); err != nil {
			return err
		}
	}
	for _, id := range sel.Folders {
		by, err := s.trashed(ctx, d, "folder", id)
		if err != nil {
			return err
		}
		if !d.MayRestore(userID, by) {
			return ErrForbidden
		}
		if err := purgeFolder(ctx, s.db, id); err != nil {
			return err
		}
	}
	return nil
}

type dbtx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// purgeFolder deletes a folder, the folders within it (deepest first) and their files.
func purgeFolder(ctx context.Context, db dbtx, id string) error {
	rows, err := db.Query(ctx, `WITH RECURSIVE down AS (
			SELECT id, 0 AS depth FROM drive_folders WHERE id::text = $1
			UNION ALL SELECT f.id, down.depth + 1 FROM drive_folders f JOIN down ON f.parent_id = down.id WHERE down.depth < 50)
		SELECT id::text FROM down ORDER BY depth DESC`, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `DELETE FROM files WHERE folder_id::text = ANY($1)`, ids); err != nil {
		return err
	}
	for _, f := range ids {
		if _, err := db.Exec(ctx, `DELETE FROM drive_folders WHERE id::text = $1`, f); err != nil {
			return err
		}
	}
	return nil
}

// PurgeExpired deletes for good what has been in the trashes of all drives longer than the company's trash
// setting (the one of deleted tasks); 0 — kept until deleted by hand.
func (s *Store) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	days, err := s.tasks.TrashDays(ctx)
	if err != nil || days == 0 {
		return 0, err
	}
	before := now.AddDate(0, 0, -days)
	tag, err := s.db.Exec(ctx, `DELETE FROM files WHERE deleted_at < $1`, before)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	rows, err := s.db.Query(ctx, `SELECT id::text FROM drive_folders WHERE deleted_at < $1`, before)
	if err != nil {
		return n, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return n, err
	}
	for _, id := range ids {
		if err := purgeFolder(ctx, s.db, id); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
