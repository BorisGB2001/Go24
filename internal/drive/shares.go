package drive

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Accesses are the kinds of access to a shared folder: read — see and download; edit — also upload, make folders,
// change and delete one's own; full — change and delete anything in it.
var Accesses = []string{"read", "edit", "full"}

// FileAccesses are the kinds of access to a shared file: read — see and download; edit — also change it.
var FileAccesses = []string{"read", "edit"}

// Share is a folder or a file of «Мой диск» opened to an employee or a department.
type Share struct {
	ID, ItemID   string // ItemID: the folder or the file shared
	Kind         string // user | department
	WhomID, Whom string
	Access       string
}

// SharedFolder is a folder (or a file) someone shared with the user: the best access they have to it.
type SharedFolder struct {
	ID, Name, Owner, OwnerID, Access string
	SharedAt                         time.Time
	File                             bool
}

// mine: the shares of the user $1 — to them or to a department of theirs (or one above it)
const mine = `(sh.user_id = $1 OR sh.department_id IN (WITH RECURSIVE dep AS (
		SELECT department_id AS id FROM user_departments WHERE user_id = $1
		UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
	SELECT id FROM dep))`

// best: the strongest access of the shares grouped
const best = `(ARRAY['read', 'edit', 'full'])[max(array_position(ARRAY['read', 'edit', 'full'], sh.access))]`

// openShared is a folder shared with the user as a drive of its own (see Drive.Root).
func (s *Store) openShared(ctx context.Context, userID, folderID string) (Drive, error) {
	var d Drive
	err := s.db.QueryRow(ctx, `SELECT f.owner_id::text, f.name, coalesce(u.name, ''), `+best+`
		FROM drive_shares sh JOIN drive_folders f ON f.id = sh.folder_id LEFT JOIN users u ON u.id = f.owner_id
		WHERE sh.folder_id::text = $2 AND f.drive = 'user' AND f.owner_id <> $1 AND `+mine+`
		GROUP BY f.owner_id, f.name, u.name`, userID, folderID).Scan(&d.OwnerID, &d.Name, &d.Owner, &d.Access)
	if errors.Is(err, pgx.ErrNoRows) {
		return Drive{}, ErrNotFound
	}
	if err != nil {
		return Drive{}, err
	}
	d.Kind, d.Root = "user", folderID
	d.CanWrite, d.CanManage = d.Access != "read", d.Access == "full"
	if _, err := s.path(ctx, d, folderID); err != nil { // in the owner's trash
		return Drive{}, err
	}
	return d, nil
}

// openSharedFile is a file shared with the user as a drive of its own (see Drive.File).
func (s *Store) openSharedFile(ctx context.Context, userID, fileID string) (Drive, error) {
	var d Drive
	err := s.db.QueryRow(ctx, `SELECT x.drive_owner::text, x.name, coalesce(u.name, ''), `+best+`
		FROM drive_shares sh JOIN files x ON x.id = sh.file_id LEFT JOIN users u ON u.id = x.drive_owner
		WHERE sh.file_id::text = $2 AND x.drive = 'user' AND x.drive_owner <> $1 AND x.deleted_at IS NULL AND `+mine+` AND `+liveFile+`
		GROUP BY x.drive_owner, x.name, u.name`, userID, fileID).Scan(&d.OwnerID, &d.Name, &d.Owner, &d.Access)
	if errors.Is(err, pgx.ErrNoRows) {
		return Drive{}, ErrNotFound
	}
	if err != nil {
		return Drive{}, err
	}
	d.Kind, d.File = "user", fileID // only changing the file itself, with edit (see Drive.mayChange)
	return d, nil
}

// SharedWithMe is «Общие со мной»: the folders and the files others shared with the user (not those in their trash).
func (s *Store) SharedWithMe(ctx context.Context, userID string) ([]SharedFolder, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id::text, f.name, coalesce(u.name, ''), f.owner_id::text, `+best+`, max(sh.created_at), false
		FROM drive_shares sh JOIN drive_folders f ON f.id = sh.folder_id LEFT JOIN users u ON u.id = f.owner_id
		WHERE f.drive = 'user' AND f.owner_id <> $1 AND `+mine+` AND `+liveFolder+`
		GROUP BY f.id, f.name, u.name, f.owner_id
		UNION ALL
		SELECT x.id::text, x.name, coalesce(u.name, ''), x.drive_owner::text, `+best+`, max(sh.created_at), true
		FROM drive_shares sh JOIN files x ON x.id = sh.file_id LEFT JOIN users u ON u.id = x.drive_owner
		WHERE x.drive = 'user' AND x.drive_owner <> $1 AND x.deleted_at IS NULL AND `+mine+` AND `+liveFile+`
		GROUP BY x.id, x.name, u.name, x.drive_owner
		ORDER BY 6 DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[SharedFolder])
}

// liveFolder: the folder f and those above it are not in the trash
const liveFolder = `NOT EXISTS (WITH RECURSIVE up AS (
		SELECT id, parent_id, deleted_at, 1 AS depth FROM drive_folders WHERE id = f.id
		UNION ALL SELECT p.id, p.parent_id, p.deleted_at, up.depth + 1 FROM drive_folders p JOIN up ON p.id = up.parent_id WHERE up.depth < 50)
	SELECT 1 FROM up WHERE deleted_at IS NOT NULL)`

// liveFile: the folders above the file x are not in the trash
const liveFile = `NOT EXISTS (WITH RECURSIVE up AS (
		SELECT id, parent_id, deleted_at, 1 AS depth FROM drive_folders WHERE id = x.folder_id
		UNION ALL SELECT p.id, p.parent_id, p.deleted_at, up.depth + 1 FROM drive_folders p JOIN up ON p.id = up.parent_id WHERE up.depth < 50)
	SELECT 1 FROM up WHERE deleted_at IS NOT NULL)`

// SharedFileTo: the file of someone's «Мой диск» is shared with the user, or is within a folder shared with them.
func (s *Store) SharedFileTo(ctx context.Context, userID, fileID string) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `WITH RECURSIVE up AS (
			SELECT f.folder_id AS id, 1 AS depth FROM files f WHERE f.id::text = $2 AND f.drive = 'user' AND f.deleted_at IS NULL
			UNION ALL SELECT p.parent_id, up.depth + 1 FROM drive_folders p JOIN up ON p.id = up.id WHERE p.parent_id IS NOT NULL AND up.depth < 50)
		SELECT EXISTS (SELECT 1 FROM drive_shares sh WHERE (sh.folder_id IN (SELECT id FROM up)
				OR sh.file_id IN (SELECT id FROM files WHERE id::text = $2 AND drive = 'user' AND deleted_at IS NULL)) AND `+mine+`)
			AND NOT EXISTS (SELECT 1 FROM drive_folders WHERE id IN (SELECT id FROM up) AND deleted_at IS NOT NULL)`, userID, fileID).Scan(&ok)
	return err == nil && ok
}

// Shares are the shares of the folders and the files, by the folder or the file.
func (s *Store) Shares(ctx context.Context, ids []string) (map[string][]Share, error) {
	out := map[string][]Share{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT sh.id::text, coalesce(sh.folder_id, sh.file_id)::text, CASE WHEN sh.user_id IS NULL THEN 'department' ELSE 'user' END,
			coalesce(sh.user_id, sh.department_id)::text, coalesce(u.name, d.name, ''), sh.access
		FROM drive_shares sh LEFT JOIN users u ON u.id = sh.user_id LEFT JOIN departments d ON d.id = sh.department_id
		WHERE coalesce(sh.folder_id, sh.file_id)::text = ANY($1) ORDER BY sh.created_at`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Share])
	for _, sh := range list {
		out[sh.ItemID] = append(out[sh.ItemID], sh)
	}
	return out, err
}

// SetShare opens a folder (or, with fileID, a file) of the user's own «Мой диск» to an employee or a department
// (kind: user | department), or changes their access.
func (s *Store) SetShare(ctx context.Context, userID string, d Drive, folderID, fileID, kind, whomID, access string) error {
	if d.Kind != "user" || d.Root != "" || d.File != "" || d.OwnerID != userID {
		return ErrForbidden
	}
	accesses, item, itemID, url, from := Accesses, "folder_id", folderID, "/disk/sh/", "drive_folders"
	if fileID != "" {
		accesses, item, itemID, url, from = FileAccesses, "file_id", fileID, "/disk/sf/", "files"
	}
	if !slices.Contains(accesses, access) || (kind != "user" && kind != "department") || whomID == "" || (kind == "user" && whomID == userID) {
		return ErrNotFound
	}
	if fileID != "" {
		if _, _, err := s.fileOf(ctx, s.db, d, fileID); err != nil {
			return err
		}
	} else if _, err := s.path(ctx, d, folderID); err != nil {
		return err
	}
	col := "user_id"
	if kind == "department" {
		col = "department_id"
	}
	notifyKind, cond := "disk_share", col+` IS NOT NULL`
	if fileID != "" {
		notifyKind, cond = "disk_file_share", `file_id IS NOT NULL AND `+col+` IS NOT NULL`
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var added bool
	err = tx.QueryRow(ctx, `INSERT INTO drive_shares (`+item+`, `+col+`, access, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (`+item+`, `+col+`) WHERE `+cond+` DO UPDATE SET access = EXCLUDED.access
		RETURNING xmax = 0`, itemID, whomID, access, userID).Scan(&added)
	if err != nil && isFKOrUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if added { // «Вам открыли папку» (файл): the employee, or everyone in the department and the ones below it
		whom := `SELECT $1::uuid`
		if kind == "department" {
			whom = `WITH RECURSIVE dep AS (SELECT $1::uuid AS id UNION SELECT d.id FROM departments d JOIN dep ON d.parent_id = dep.id)
				SELECT DISTINCT ud.user_id FROM user_departments ud JOIN dep ON dep.id = ud.department_id`
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			SELECT u.id, $2, $4, f.name, $5 || f.id FROM users u, `+from+` f
			WHERE u.id IN (`+whom+`) AND u.id <> $2 AND u.active AND f.id = $3`, whomID, userID, itemID, notifyKind, url); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SharesOf are the shares of a folder (or, with fileID, a file) of the user's own «Мой диск».
func (s *Store) SharesOf(ctx context.Context, userID string, d Drive, folderID, fileID string) ([]Share, error) {
	if d.Kind != "user" || d.Root != "" || d.File != "" || d.OwnerID != userID {
		return nil, ErrForbidden
	}
	itemID := folderID
	if fileID != "" {
		itemID = fileID
		if _, _, err := s.fileOf(ctx, s.db, d, fileID); err != nil {
			return nil, err
		}
	} else if _, err := s.path(ctx, d, folderID); err != nil {
		return nil, err
	}
	shares, err := s.Shares(ctx, []string{itemID})
	return shares[itemID], err
}

// DeleteShare closes a folder or a file of the user's own «Мой диск» to the one it was shared with.
func (s *Store) DeleteShare(ctx context.Context, userID string, d Drive, shareID string) error {
	if d.Kind != "user" || d.Root != "" || d.File != "" || d.OwnerID != userID {
		return ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM drive_shares sh WHERE sh.id::text = $1
		AND (EXISTS (SELECT 1 FROM drive_folders f WHERE f.id = sh.folder_id AND f.drive = 'user' AND f.owner_id = $2)
			OR EXISTS (SELECT 1 FROM files x WHERE x.id = sh.file_id AND x.drive = 'user' AND x.drive_owner = $2))`, shareID, userID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// isFKOrUUID: no such employee or department (or not even an id).
func isFKOrUUID(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "22P02")
}

// Department is a department to share a folder with, its name with the departments above it.
type Department struct {
	ID, Name string
}

// Departments are the company's departments (without the company itself), as a tree in order.
func (s *Store) Departments(ctx context.Context) ([]Department, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE t AS (
			SELECT id, name, ARRAY[lpad(sort::text, 9, '0') || name] AS path, 0 AS depth FROM departments WHERE parent_id IS NULL
			UNION ALL SELECT d.id, d.name, t.path || (lpad(d.sort::text, 9, '0') || d.name), t.depth + 1 FROM departments d JOIN t ON d.parent_id = t.id WHERE t.depth < 20)
		SELECT id::text, repeat('· ', depth - 1) || name FROM t WHERE depth > 0 ORDER BY path`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Department])
}
