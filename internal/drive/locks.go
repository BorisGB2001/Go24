package drive

import (
	"context"

	"go24/internal/i18n"
)

// ErrLocked: the file is locked by someone else.
var ErrLocked = i18n.E("err.file_locked")

// ErrLocksOff: the company has turned the locking of documents off.
var ErrLocksOff = i18n.E("err.disk_locks_off")

// locksOn: the locking of documents is on («Настройки → Диск»); while it is off, the locks set are kept but do not hold.
const locksOn = `NOT EXISTS (SELECT 1 FROM drive_limits WHERE kind = 'no_locks' AND bytes = 1)`

// lockCols are the name and the id of the one who locked a file f and when (none while locking is off), from the
// user lu of lockJoin.
const (
	lockCols = `coalesce(lu.name, ''), coalesce(lu.id::text, ''), CASE WHEN lu.id IS NOT NULL THEN f.locked_at END`
	lockJoin = `LEFT JOIN users lu ON lu.id = f.locked_by AND ` + locksOn
)

// changeFile checks that the user may change the file of the drive (upload a version of it, rename, move or delete
// it): its uploader or those who manage the drive, and not while someone else has it locked.
func (s *Store) changeFile(ctx context.Context, q querier, userID string, d Drive, id string) error {
	by, folder, err := s.fileOf(ctx, q, d, id)
	if err != nil {
		return err
	}
	if d, err = s.Within(ctx, d, folder); err != nil {
		return err
	}
	if !d.mayChange(userID, by) {
		return ErrForbidden
	}
	var locker string
	if err := q.QueryRow(ctx, `SELECT CASE WHEN `+locksOn+` THEN coalesce(locked_by::text, '') ELSE '' END FROM files WHERE id::text = $1`, id).Scan(&locker); err != nil {
		return err
	}
	if locker != "" && locker != userID {
		return ErrLocked
	}
	return nil
}

// Lock locks the file for the others («Заблокировать»): one who may change it does it.
func (s *Store) Lock(ctx context.Context, userID string, d Drive, id string) error {
	if off, err := s.off(ctx, offLocks); err != nil {
		return err
	} else if off {
		return ErrLocksOff
	}
	if err := s.changeFile(ctx, s.db, userID, d, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE files SET locked_by = $2, locked_at = now() WHERE id::text = $1 AND locked_by IS NULL`, id, userID)
	return err
}

// Unlock lifts the file's lock: the one who locked it, or one who manages the drive.
func (s *Store) Unlock(ctx context.Context, userID string, d Drive, id string) error {
	if _, _, err := s.fileOf(ctx, s.db, d, id); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE files SET locked_by = NULL, locked_at = NULL WHERE id::text = $1 AND (locked_by = $2 OR $3)`,
		id, userID, d.CanManage)
	if err == nil && tag.RowsAffected() == 0 {
		var locked bool
		if err := s.db.QueryRow(ctx, `SELECT locked_by IS NOT NULL FROM files WHERE id::text = $1`, id).Scan(&locked); err != nil {
			return err
		}
		if locked {
			return ErrForbidden
		}
	}
	return err
}

// lockedInside: a file within the folder (at any depth) is locked by someone other than the user.
func lockedInside(ctx context.Context, q querier, userID, folderID string) error {
	var locked bool
	if err := q.QueryRow(ctx, `WITH RECURSIVE down AS (
			SELECT id FROM drive_folders WHERE id::text = $1
			UNION ALL SELECT f.id FROM drive_folders f JOIN down ON f.parent_id = down.id WHERE f.deleted_at IS NULL)
		SELECT EXISTS (SELECT 1 FROM files WHERE folder_id IN (SELECT id FROM down) AND deleted_at IS NULL
			AND locked_by IS NOT NULL AND locked_by <> $2::uuid AND `+locksOn+`)`, folderID, userID).Scan(&locked); err != nil {
		return err
	}
	if locked {
		return ErrLocked
	}
	return nil
}

// Editing keeps who has the file open in the online editor now (none — everyone closed it).
func (s *Store) Editing(ctx context.Context, fileID string, users []string) error {
	var ids []string
	for _, u := range users {
		if len(u) == 36 && isUUID(u) {
			ids = append(ids, u)
		}
	}
	if len(ids) == 0 {
		_, err := s.db.Exec(ctx, `UPDATE files SET editing = NULL, editing_at = NULL WHERE id::text = $1`, fileID)
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE files SET editing = $2::uuid[], editing_at = now() WHERE id::text = $1`, fileID, ids)
	return err
}

func isUUID(s string) bool {
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}
