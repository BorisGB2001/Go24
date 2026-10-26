package drive

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// Right is a line of «Права доступа» of a folder of the company's drive or of a project's: to whom (Kind: all — every
// employee of the drive, user, department) and the access (read | edit | full).
type Right struct {
	ID, FolderID string
	Kind         string
	WhomID, Whom string
	Access       string
}

// viewerArg is the viewer the folders' rights are checked for: nil — none to check (one's own «Мой диск», a folder
// shared with them, or one who has every folder of the drive: an administrator, a project's manager).
func (d Drive) viewerArg() *string {
	if d.viewer == "" {
		return nil
	}
	return &d.viewer
}

// visibleSQL: the folder col is open to the viewer arg (an SQL parameter, see viewerArg) by the rights of the folders
const visibleSQL = `(%[1]s IS NULL OR %[2]s::uuid IS NULL OR drive_folder_access(%[1]s, %[2]s::uuid) IS DISTINCT FROM 'none')`

// Within is the drive as the viewer may use it in the folder ("" — the top), by the rights of the folders: a folder
// not open to them is not found; with read they only see and download, with edit they also upload and change their
// own, with full — everything. The drive's own limits stay (one who may not write in it writes in no folder of it).
func (s *Store) Within(ctx context.Context, d Drive, folderID string) (Drive, error) {
	if d.viewer == "" || folderID == "" {
		return d, nil
	}
	var access *string
	err := s.db.QueryRow(ctx, `SELECT drive_folder_access($1::uuid, $2::uuid)`, folderID, d.viewer).Scan(&access)
	if err != nil && isFKOrUUID(err) {
		return Drive{}, ErrNotFound
	}
	if err != nil {
		return Drive{}, err
	}
	switch {
	case access == nil:
		return d, nil
	case *access == "none":
		return Drive{}, ErrNotFound
	}
	d.Access = *access
	d.CanWrite = d.CanWrite && *access != "read"
	d.CanManage = d.CanWrite && *access == "full"
	return d, nil
}

// hiddenInside: the folder holds a folder (at any depth) not open to the viewer — what is moved or deleted with all
// it holds is moved or deleted only by one who sees all of it.
func (s *Store) hiddenInside(ctx context.Context, q querier, d Drive, folderID string) (bool, error) {
	if d.viewer == "" {
		return false, nil
	}
	var hidden bool
	err := q.QueryRow(ctx, `WITH RECURSIVE down AS (
			SELECT id, 0 AS depth FROM drive_folders WHERE id = $1
			UNION ALL SELECT f.id, down.depth + 1 FROM drive_folders f JOIN down ON f.parent_id = down.id WHERE down.depth < 50)
		SELECT EXISTS (SELECT 1 FROM down WHERE depth > 0 AND drive_folder_access(id, $2::uuid) = 'none')`, folderID, d.viewer).Scan(&hidden)
	return hidden, err
}

// FolderRights are «Права доступа» of the folders, by the folder (only those with rights of their own are there).
func (s *Store) FolderRights(ctx context.Context, ids []string) (map[string][]Right, error) {
	out := map[string][]Right{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.id::text, r.folder_id::text,
			CASE WHEN r.user_id IS NOT NULL THEN 'user' WHEN r.department_id IS NOT NULL THEN 'department' ELSE 'all' END,
			coalesce(r.user_id::text, r.department_id::text, ''), coalesce(u.name, d.name, ''), r.access
		FROM drive_folder_rights r LEFT JOIN users u ON u.id = r.user_id LEFT JOIN departments d ON d.id = r.department_id
		WHERE r.folder_id::text = ANY($1) ORDER BY r.user_id IS NOT NULL, r.department_id IS NOT NULL, r.created_at`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Right])
	for _, r := range list {
		out[r.FolderID] = append(out[r.FolderID], r)
	}
	return out, err
}

// MayRight: the viewer may change «Права доступа» of the folder — a folder of the company's drive or of a project's,
// with full access to it.
func (s *Store) MayRight(ctx context.Context, d Drive, folderID string) bool {
	if (d.Kind != "company" && d.Kind != "project") || folderID == "" {
		return false
	}
	if _, err := s.path(ctx, d, folderID); err != nil {
		return false
	}
	in, err := s.Within(ctx, d, folderID)
	return err == nil && in.CanManage
}

// SetRight opens the folder to everyone of the drive (kind all), an employee or a department (with its
// sub-departments) with the access, or changes it. The first right of a folder closes it to all the others — so the
// one who sets it keeps full access to it.
func (s *Store) SetRight(ctx context.Context, userID string, d Drive, folderID, kind, whomID, access string) error {
	if !slices.Contains(Accesses, access) || (kind != "all" && kind != "user" && kind != "department") || (kind != "all" && whomID == "") {
		return ErrNotFound
	}
	if !s.MayRight(ctx, d, folderID) {
		return ErrForbidden
	}
	var user, dept *string
	switch kind {
	case "user":
		user = &whomID
	case "department":
		dept = &whomID
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if d.viewer != "" { // the first right: the one who sets it keeps the folder (an administrator has it anyway)
		if _, err := tx.Exec(ctx, `INSERT INTO drive_folder_rights (folder_id, user_id, access, created_by)
			SELECT $1, $2, 'full', $2 WHERE NOT EXISTS (SELECT 1 FROM drive_folder_rights WHERE folder_id = $1)`, folderID, userID); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO drive_folder_rights (folder_id, user_id, department_id, access, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (folder_id, coalesce(user_id, department_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET access = EXCLUDED.access`,
		folderID, user, dept, access, userID)
	if err != nil && isFKOrUUID(err) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteRight takes a line out of «Права доступа» of the folder; with none left the folder has the rights of the one
// above it again. One may not take away their own full access while they need it (see SetRight).
func (s *Store) DeleteRight(ctx context.Context, userID string, d Drive, folderID, rightID string) error {
	if !s.MayRight(ctx, d, folderID) {
		return ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM drive_folder_rights WHERE id::text = $1 AND folder_id = $2`, rightID, folderID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if d.viewer != "" {
		var access *string
		if err := tx.QueryRow(ctx, `SELECT drive_folder_access($1::uuid, $2::uuid)`, folderID, d.viewer).Scan(&access); err != nil {
			return err
		}
		if access != nil && *access != "full" {
			return ErrForbidden
		}
	}
	return tx.Commit(ctx)
}

// ClearRights takes all «Права доступа» of the folder away: it has the rights of the one above it again.
func (s *Store) ClearRights(ctx context.Context, d Drive, folderID string) error {
	if !s.MayRight(ctx, d, folderID) {
		return ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM drive_folder_rights WHERE folder_id = $1`, folderID); err != nil {
		return err
	}
	if d.viewer != "" { // the rights above it must still give them the folder
		var access *string
		if err := tx.QueryRow(ctx, `SELECT drive_folder_access($1::uuid, $2::uuid)`, folderID, d.viewer).Scan(&access); err != nil {
			return err
		}
		if access != nil && *access != "full" {
			return ErrForbidden
		}
	}
	return tx.Commit(ctx)
}

// CanReadFile: may the user download the file (its drive's kind and owner as its row has them) — they see its drive,
// and the rights of the folders let them see its folder.
func (s *Store) CanReadFile(ctx context.Context, userID, fileID, kind, owner string) bool {
	if !s.CanRead(ctx, userID, kind, owner) {
		return false
	}
	if kind != "company" && kind != "project" || tasks.IsAdmin(ctx) {
		return true
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT `+fmt.Sprintf(visibleSQL, "f.folder_id", "$2")+` FROM files f WHERE f.id::text = $1`, fileID, userID).Scan(&ok)
	return err == nil && ok
}

// orForbidden is the error, or ErrForbidden when there is none.
func orForbidden(err error) error {
	if err != nil {
		return err
	}
	return ErrForbidden
}
