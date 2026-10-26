// Package drive is «Диск»: «Мой диск» of each employee, the company's «Общий диск» and the drive of each project,
// with folders. The files themselves are those of package storages (their bytes in the storage chosen on upload).
package drive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct {
	db    *pgxpool.Pool
	tasks *tasks.Store
}

func NewStore(db *pgxpool.Pool, t *tasks.Store) *Store { return &Store{db: db, tasks: t} }

// Drive is one drive as the viewer may use it.
type Drive struct {
	Kind       string // user | company | project
	OwnerID    string // the user or the project ("" — the company)
	ProjectKey string
	Name       string // a project's name
	Collab     bool   // the drive of a collab
	CanWrite   bool   // upload, make folders
	CanManage  bool   // delete anyone's files and folders
	Root       string // a folder of someone's «Мой диск» shared with the viewer: the drive is only what it holds
	File       string // a file of someone's «Мой диск» shared with the viewer: the drive is only that file
	Owner      string // whose «Мой диск» that is
	Access     string // the viewer's access to it: read | edit | full
	Employee   bool   // an administrator in another employee's «Мой диск» (Owner — whose, Dismissed — no longer working)
	Dismissed  bool
	viewer     string // whose access the rights of its folders limit ("" — nobody's, see viewerArg)
}

// Path is the drive's address in go24: /disk/my, /disk/company, /disk/p/KEY.
func (d Drive) Path() string {
	if d.Root != "" {
		return "/disk/sh/" + d.Root
	}
	if d.File != "" {
		return "/disk/sf/" + d.File
	}
	if d.Employee {
		return "/disk/u/" + d.OwnerID
	}
	switch d.Kind {
	case "company":
		return "/disk/company"
	case "project":
		return "/disk/p/" + d.ProjectKey
	}
	return "/disk/my"
}

// At is the folder meant by folder: "" is the drive's top — for a shared folder, the folder itself.
func (d Drive) At(folder string) string {
	if folder == "" {
		return d.Root
	}
	return folder
}

// notRoot: a shared folder itself is not renamed, moved or deleted by those it is shared with, nor a shared file.
func (d Drive) notRoot(id string) error {
	if (d.Root != "" && id == d.Root) || d.File != "" {
		return ErrForbidden
	}
	return nil
}

func (d Drive) owner() *string {
	if d.OwnerID == "" {
		return nil
	}
	return &d.OwnerID
}

// Open finds a drive for the user: "my", "company", or a project key ("u:ID" — an employee's «Мой диск», for an
// administrator the administrator reaches the drive of anyone, a dismissed employee's too). Everyone writes in the company drive and
// administrators manage it; in a project those who work in it write and its managers manage.
func (s *Store) Open(ctx context.Context, userID, which string) (Drive, error) {
	if id, ok := strings.CutPrefix(which, "sh:"); ok {
		return s.openShared(ctx, userID, id)
	}
	if id, ok := strings.CutPrefix(which, "sf:"); ok {
		return s.openSharedFile(ctx, userID, id)
	}
	if id, ok := strings.CutPrefix(which, "u:"); ok {
		return s.openEmployee(ctx, userID, id)
	}
	switch which {
	case "my":
		return Drive{Kind: "user", OwnerID: userID, CanWrite: true, CanManage: true}, nil
	case "company":
		if !s.CanRead(ctx, userID, "company", "") {
			return Drive{}, ErrNotFound
		}
		d := Drive{Kind: "company", CanWrite: true, CanManage: tasks.IsAdmin(ctx)}
		if !d.CanManage {
			d.viewer = userID
		}
		return d, nil
	}
	p, err := s.tasks.ProjectByKey(ctx, userID, which)
	if err != nil {
		return Drive{}, err
	}
	// a guest uploads into a collab's drive only when the collab lets guests upload; nobody writes into its archive
	write := p.CanWork() && s.tasks.GuestMay(ctx, p.ID, userID, "files") == nil
	d := Drive{Kind: "project", OwnerID: p.ID, ProjectKey: p.Key, Name: p.Name, Collab: p.Collab(), CanWrite: write,
		CanManage: p.CanManage() && !(p.Collab() && p.Archived)}
	if !p.CanManage() && !tasks.IsAdmin(ctx) {
		d.viewer = userID
	}
	return d, nil
}

// openEmployee is an employee's «Мой диск» as an administrator manages it; one's own is just «Мой диск».
func (s *Store) openEmployee(ctx context.Context, userID, id string) (Drive, error) {
	if !tasks.IsAdmin(ctx) {
		return Drive{}, ErrNotFound
	}
	if id == userID {
		return s.Open(ctx, userID, "my")
	}
	d := Drive{Kind: "user", OwnerID: id, CanWrite: true, CanManage: true, Employee: true}
	var active bool
	err := s.db.QueryRow(ctx, `SELECT name, active FROM users WHERE id::text = $1`, id).Scan(&d.Owner, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return Drive{}, ErrNotFound
	}
	d.Dismissed = !active
	return d, err
}

// Folder of a drive.
type Folder struct {
	ID, ParentID, Name string
}

// File of a drive.
type File struct {
	ID, Name, ContentType, StorageName, Uploader, UploaderID string
	Size                                                     int64
	CreatedAt                                                time.Time
	FolderID, Where                                          string // found by Search: the folder it is in ("" — the top)
	Locker, LockerID                                         string // «Заблокировать»: who has it locked, since when
	LockedAt                                                 *time.Time
	Editors                                                  string // who has it open in the online editor now
}

// List is a folder of the drive ("" — its top): the folders, the files, and the path from the top.
func (s *Store) List(ctx context.Context, d Drive, folderID string) (folders []Folder, files []File, path []Folder, err error) {
	folderID = d.At(folderID)
	if folderID != "" {
		if path, err = s.path(ctx, d, folderID); err != nil {
			return nil, nil, nil, err
		}
		for i, f := range path { // a shared folder's drive begins below it
			if f.ID == d.Root {
				path = path[i+1:]
				break
			}
		}
	}
	var parent *string
	if folderID != "" {
		parent = &folderID
	}
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(parent_id::text, ''), name FROM drive_folders
		WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2 AND parent_id IS NOT DISTINCT FROM $3 AND deleted_at IS NULL
			AND `+fmt.Sprintf(visibleSQL, "id", "$4")+` ORDER BY lower(name)`,
		d.Kind, d.owner(), parent, d.viewerArg())
	if err != nil {
		return nil, nil, nil, err
	}
	if folders, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Folder]); err != nil {
		return nil, nil, nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, coalesce(st.name, ''), coalesce(u.name, ''),
			coalesce(f.uploaded_by::text, ''), f.size, f.created_at, coalesce(f.folder_id::text, ''), '',
			`+lockCols+`,
			coalesce((SELECT string_agg(eu.name, ', ' ORDER BY eu.name) FROM users eu WHERE eu.id = ANY(f.editing)
				AND f.editing_at > now() - interval '12 hours'), '')
		FROM files f LEFT JOIN storages st ON st.id = f.storage_id LEFT JOIN users u ON u.id = f.uploaded_by
			`+lockJoin+`
		WHERE f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2 AND f.folder_id IS NOT DISTINCT FROM $3
			AND f.deleted_at IS NULL
		ORDER BY lower(f.name)`, d.Kind, d.owner(), parent)
	if err != nil {
		return nil, nil, nil, err
	}
	files, err = pgx.CollectRows(rows, pgx.RowToStructByPos[File])
	return folders, files, path, err
}

// path is the folders from the drive's top down to the folder (which must be in the drive and not in its trash).
func (s *Store) path(ctx context.Context, d Drive, folderID string) ([]Folder, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT id, parent_id, name, drive, owner_id, deleted_at, 1 AS depth FROM drive_folders WHERE id::text = $1
			UNION ALL
			SELECT f.id, f.parent_id, f.name, f.drive, f.owner_id, f.deleted_at, up.depth + 1 FROM drive_folders f JOIN up ON f.id = up.parent_id
			WHERE up.depth < 50)
		SELECT id, coalesce(parent_id::text, ''), name FROM up WHERE drive = $2 AND owner_id IS NOT DISTINCT FROM $3
			AND NOT EXISTS (SELECT 1 FROM up WHERE deleted_at IS NOT NULL)
		ORDER BY depth DESC`, folderID, d.Kind, d.owner())
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Folder])
	if err != nil {
		return nil, err
	}
	if len(list) == 0 || list[len(list)-1].ID != folderID {
		return nil, ErrNotFound
	}
	if d.Root != "" && !slices.ContainsFunc(list, func(f Folder) bool { return f.ID == d.Root }) {
		return nil, ErrNotFound // not within the shared folder
	}
	if _, err := s.Within(ctx, d, folderID); err != nil { // not open to the viewer
		return nil, err
	}
	return list, nil
}

// CheckFolder: the folder ("" — the top) is in the drive and the user may put things into it.
func (s *Store) CheckFolder(ctx context.Context, d Drive, folderID string) error {
	if !d.CanWrite {
		return ErrForbidden
	}
	if folderID = d.At(folderID); folderID == "" {
		return nil
	}
	if _, err := s.path(ctx, d, folderID); err != nil {
		return err
	}
	if in, err := s.Within(ctx, d, folderID); err != nil || !in.CanWrite {
		return orForbidden(err)
	}
	return nil
}

// CreateFolder makes a folder in a folder of the drive.
func (s *Store) CreateFolder(ctx context.Context, userID string, d Drive, parentID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 || strings.ContainsAny(name, `/\`) {
		return "", i18n.E("err.folder_name")
	}
	parentID = d.At(parentID)
	if err := s.CheckFolder(ctx, d, parentID); err != nil {
		return "", err
	}
	var parent *string
	if parentID != "" {
		parent = &parentID
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO drive_folders (drive, owner_id, parent_id, name, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		d.Kind, d.owner(), parent, name, userID).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return "", i18n.E("err.folder_exists")
	}
	return id, err
}

// EnsurePath finds the folders of dir ("a/b/c", from an uploaded folder) under the parent folder, making those that
// are not there yet when a folder is uploaded: a folder of the same name takes the files in. The deepest
// folder's ID is returned; an empty dir is the parent itself.
func (s *Store) EnsurePath(ctx context.Context, userID string, d Drive, parentID, dir string) (string, error) {
	if d.File != "" {
		return "", ErrForbidden
	}
	parentID = d.At(parentID)
	if err := s.CheckFolder(ctx, d, parentID); err != nil {
		return "", err
	}
	parts := strings.Split(strings.ReplaceAll(dir, `\`, "/"), "/")
	if len(parts) > 32 {
		return "", i18n.E("err.folder_name")
	}
	for _, name := range parts {
		name = strings.TrimSpace(name)
		if name == "" || name == "." {
			continue
		}
		if name == ".." || len([]rune(name)) > 200 {
			return "", i18n.E("err.folder_name")
		}
		id, err := s.childFolder(ctx, d, parentID, name)
		if errors.Is(err, pgx.ErrNoRows) {
			id, err = s.CreateFolder(ctx, userID, d, parentID, name)
			var ie *i18n.Error
			if errors.As(err, &ie) && ie.Key == "err.folder_exists" { // made by a parallel upload meanwhile
				id, err = s.childFolder(ctx, d, parentID, name)
			}
		}
		if err != nil {
			return "", err
		}
		parentID = id
	}
	return parentID, nil
}

// childFolder is the folder named name (in any case) right in the parent folder.
func (s *Store) childFolder(ctx context.Context, d Drive, parentID, name string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM drive_folders WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2
		AND parent_id IS NOT DISTINCT FROM $3 AND lower(name) = lower($4) AND deleted_at IS NULL`,
		d.Kind, d.owner(), nullable(parentID), name).Scan(&id)
	return id, err
}

// DeleteFolder puts a folder with all it holds into the drive's trash: its maker, when all it holds is theirs too,
// or those who manage the drive.
func (s *Store) DeleteFolder(ctx context.Context, userID string, d Drive, id string) error {
	if err := d.notRoot(id); err != nil {
		return err
	}
	if _, err := s.path(ctx, d, id); err != nil {
		return err
	}
	var by string
	var others bool // things of other people inside
	if err := s.db.QueryRow(ctx, `WITH RECURSIVE down AS (
			SELECT id FROM drive_folders WHERE id = $1
			UNION ALL SELECT f.id FROM drive_folders f JOIN down ON f.parent_id = down.id WHERE f.deleted_at IS NULL)
		SELECT created_by::text,
			EXISTS (SELECT 1 FROM drive_folders WHERE id IN (SELECT id FROM down) AND created_by <> $2::uuid)
			OR EXISTS (SELECT 1 FROM files WHERE folder_id IN (SELECT id FROM down) AND deleted_at IS NULL AND uploaded_by IS DISTINCT FROM $2::uuid)
		FROM drive_folders WHERE id = $1`, id, userID).Scan(&by, &others); err != nil {
		return err
	}
	if d, err := s.Within(ctx, d, id); err != nil || (!d.CanManage && !(d.CanWrite && by == userID && !others)) {
		return orForbidden(err)
	}
	if hidden, err := s.hiddenInside(ctx, s.db, d, id); err != nil || hidden {
		return orForbidden(err)
	}
	if err := lockedInside(ctx, s.db, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE drive_folders SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, id, userID)
	return err
}

// DeleteFile puts a file of the drive into its trash (its uploader or those who manage the drive).
func (s *Store) DeleteFile(ctx context.Context, userID string, d Drive, fileID string) error {
	if err := d.notRoot(fileID); err != nil {
		return err
	}
	if err := s.changeFile(ctx, s.db, userID, d, fileID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE files SET deleted_at = now(), deleted_by = $2 WHERE id::text = $1`, fileID, userID)
	return err
}

// CanRead: may the user download a file of this drive (kind and owner as the file row has them)?
func (s *Store) CanRead(ctx context.Context, userID, kind, owner string) bool {
	switch kind {
	case "company": // the company's drive is for employees: a guest from outside does not see it
		var guest bool
		_ = s.db.QueryRow(ctx, `SELECT guest FROM users WHERE id::text = $1`, userID).Scan(&guest)
		return !guest
	case "user":
		return owner == userID || tasks.IsAdmin(ctx)
	case "project":
		var ok bool
		_ = s.db.QueryRow(ctx, `SELECT $3 OR EXISTS (SELECT 1 FROM project_members WHERE project_id::text = $1 AND user_id = $2)`,
			owner, userID, tasks.IsAdmin(ctx)).Scan(&ok)
		return ok
	}
	return false
}

// checkName is a name a file or a folder may have.
func checkName(name, errKey string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 || strings.ContainsAny(name, `/\`) {
		return "", i18n.E(errKey)
	}
	return name, nil
}

// mayChange: the user may rename, move or delete a thing of the drive made by `by`.
func (d Drive) mayChange(userID, by string) bool {
	return d.CanManage || (d.CanWrite && by == userID) || (d.File != "" && d.Access == "edit")
}

// fileOf is the uploader of a file of the drive (not in its trash) and its folder ("" — the top).
func (s *Store) fileOf(ctx context.Context, q querier, d Drive, id string) (by, folder string, err error) {
	err = q.QueryRow(ctx, `SELECT coalesce(uploaded_by::text, ''), coalesce(folder_id::text, '') FROM files WHERE id::text = $1
		AND drive = $2 AND drive_owner IS NOT DISTINCT FROM $3 AND deleted_at IS NULL`, id, d.Kind, d.owner()).Scan(&by, &folder)
	if errors.Is(err, pgx.ErrNoRows) || (d.File != "" && id != d.File) {
		return "", "", ErrNotFound
	}
	if err == nil && folder == "" && d.Root != "" {
		return "", "", ErrNotFound
	}
	if err == nil && folder != "" { // a file of a folder in the trash is in the trash too
		_, err = s.path(ctx, d, folder)
	}
	return by, folder, err
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RenameFile gives a file of the drive another name (its uploader or those who manage the drive).
func (s *Store) RenameFile(ctx context.Context, userID string, d Drive, id, name string) error {
	if err := d.notRoot(id); err != nil {
		return err
	}
	name, err := checkName(name, "err.file_name")
	if err != nil {
		return err
	}
	if err := s.changeFile(ctx, s.db, userID, d, id); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE files SET name = $2 WHERE id::text = $1`, id, name)
	return err
}

// RenameFolder gives a folder of the drive another name (its maker or those who manage the drive).
func (s *Store) RenameFolder(ctx context.Context, userID string, d Drive, id, name string) error {
	name, err := checkName(name, "err.folder_name")
	if err != nil {
		return err
	}
	if err := d.notRoot(id); err != nil {
		return err
	}
	if _, err := s.path(ctx, d, id); err != nil {
		return err
	}
	var by string
	if err := s.db.QueryRow(ctx, `SELECT created_by::text FROM drive_folders WHERE id = $1`, id).Scan(&by); err != nil {
		return err
	}
	if d, err := s.Within(ctx, d, id); err != nil || !d.mayChange(userID, by) {
		return orForbidden(err)
	}
	_, err = s.db.Exec(ctx, `UPDATE drive_folders SET name = $2 WHERE id = $1`, id, name)
	return folderErr(err)
}

func folderErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return i18n.E("err.folder_exists")
	}
	return err
}

// Selection is what an action of «Диск» is done with: files and folders of one folder of a drive.
type Selection struct {
	Files, Folders []string
}

// Move puts files and folders of drive `from` into folder `to` ("" — the top) of drive `dst`, the same drive or
// another one the user writes in. A folder goes with all it holds; it cannot go into itself.
func (s *Store) Move(ctx context.Context, userID string, from Drive, sel Selection, dst Drive, to string) error {
	if from.File != "" {
		return ErrForbidden
	}
	to = dst.At(to)
	if err := s.CheckFolder(ctx, dst, to); err != nil {
		return err
	}
	var target *string
	if to != "" {
		target = &to
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, id := range sel.Files {
		if err := s.changeFile(ctx, tx, userID, from, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE files SET drive = $2, drive_owner = $3, folder_id = $4 WHERE id::text = $1`,
			id, dst.Kind, dst.owner(), target); err != nil {
			return err
		}
	}
	for _, id := range sel.Folders {
		if err := from.notRoot(id); err != nil {
			return err
		}
		if _, err := s.path(ctx, from, id); err != nil {
			return err
		}
		var by string
		var inside bool // the target is the folder itself or within it
		if err := tx.QueryRow(ctx, `WITH RECURSIVE down AS (
				SELECT id FROM drive_folders WHERE id = $1
				UNION ALL SELECT f.id FROM drive_folders f JOIN down ON f.parent_id = down.id)
			SELECT created_by::text, $2::uuid IS NOT NULL AND $2::uuid IN (SELECT id FROM down) FROM drive_folders WHERE id = $1`,
			id, target).Scan(&by, &inside); err != nil {
			return err
		}
		if in, err := s.Within(ctx, from, id); err != nil || !in.mayChange(userID, by) {
			return orForbidden(err)
		}
		if hidden, err := s.hiddenInside(ctx, tx, from, id); err != nil || hidden {
			return orForbidden(err)
		}
		if inside {
			return i18n.E("err.folder_into_itself")
		}
		if err := lockedInside(ctx, tx, userID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE drive_folders SET parent_id = $2 WHERE id = $1`, id, target); err != nil {
			return folderErr(err)
		}
		if from.Kind != dst.Kind || from.OwnerID != dst.OwnerID { // all it holds changes drive too
			if _, err := tx.Exec(ctx, `WITH RECURSIVE down AS (
					SELECT id FROM drive_folders WHERE id = $1
					UNION ALL SELECT f.id FROM drive_folders f JOIN down ON f.parent_id = down.id),
				fo AS (UPDATE drive_folders SET drive = $2, owner_id = $3 WHERE id IN (SELECT id FROM down))
				UPDATE files SET drive = $2, drive_owner = $3 WHERE folder_id IN (SELECT id FROM down)`,
				id, dst.Kind, dst.owner()); err != nil {
				return folderErr(err)
			}
		}
	}
	return tx.Commit(ctx)
}

// CopyBytes stores the bytes of a stored file once more under another key of the same storage.
type CopyBytes func(ctx context.Context, storageID *string, from, to string, size int64, contentType string) error

// Copy puts copies of files and folders (with all they hold) of drive `from` into folder `to` of drive `dst`. A copy
// that would get the name of something already there is named «name (2)», as in the file managers.
func (s *Store) Copy(ctx context.Context, userID string, from Drive, sel Selection, dst Drive, to string, copyBytes CopyBytes) error {
	to = dst.At(to)
	if err := s.CheckFolder(ctx, dst, to); err != nil {
		return err
	}
	for _, id := range sel.Files {
		if _, _, err := s.fileOf(ctx, s.db, from, id); err != nil {
			return err
		}
		if err := s.copyFile(ctx, userID, id, dst, to, copyBytes); err != nil {
			return err
		}
	}
	for _, id := range sel.Folders {
		if _, err := s.path(ctx, from, id); err != nil {
			return err
		}
		if err := s.copyFolder(ctx, userID, from, id, dst, to, 0, copyBytes); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) copyFile(ctx context.Context, userID, id string, dst Drive, to string, copyBytes CopyBytes) error {
	var key, name, ct string
	var size int64
	var storageID *string
	if err := s.db.QueryRow(ctx, `SELECT object_key, name, content_type, size, storage_id FROM files WHERE id::text = $1`, id).
		Scan(&key, &name, &ct, &size, &storageID); err != nil {
		return err
	}
	taken, err := s.fileNames(ctx, dst, to)
	if err != nil {
		return err
	}
	name = freeName(name, taken, true)
	newKey := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	if err := copyBytes(ctx, storageID, key, newKey, size, ct); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, storage_id, drive, drive_owner, folder_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, newKey, name, size, ct, userID, storageID, dst.Kind, dst.owner(), nullable(to))
	return err
}

// copyFolder copies the folder with what it holds that is open to the user (by the rights of the folders of from).
func (s *Store) copyFolder(ctx context.Context, userID string, from Drive, id string, dst Drive, to string, depth int, copyBytes CopyBytes) error {
	if depth > 50 {
		return nil
	}
	var name string
	if err := s.db.QueryRow(ctx, `SELECT name FROM drive_folders WHERE id = $1`, id).Scan(&name); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT lower(name) FROM drive_folders WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2
		AND parent_id IS NOT DISTINCT FROM $3 AND deleted_at IS NULL`, dst.Kind, dst.owner(), nullable(to))
	if err != nil {
		return err
	}
	taken, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var copyID string
	if err := s.db.QueryRow(ctx, `INSERT INTO drive_folders (drive, owner_id, parent_id, name, created_by) VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		dst.Kind, dst.owner(), nullable(to), freeName(name, taken, false), userID).Scan(&copyID); err != nil {
		return folderErr(err)
	}
	rows, err = s.db.Query(ctx, `SELECT id::text FROM files WHERE folder_id = $1 AND deleted_at IS NULL ORDER BY name`, id)
	if err != nil {
		return err
	}
	files, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := s.copyFile(ctx, userID, f, dst, copyID, copyBytes); err != nil {
			return err
		}
	}
	rows, err = s.db.Query(ctx, `SELECT id::text FROM drive_folders WHERE parent_id = $1 AND deleted_at IS NULL
		AND `+fmt.Sprintf(visibleSQL, "id", "$2")+` ORDER BY name`, id, from.viewerArg())
	if err != nil {
		return err
	}
	subs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, f := range subs {
		if err := s.copyFolder(ctx, userID, from, f, dst, copyID, depth+1, copyBytes); err != nil {
			return err
		}
	}
	return nil
}

// freeName is the name itself when it is not taken (taken — lower case), else «name (2)», «name (3)»…; a file's
// number goes before its extension.
func freeName(name string, taken []string, file bool) string {
	used := map[string]bool{}
	for _, t := range taken {
		used[t] = true
	}
	if !used[strings.ToLower(name)] {
		return name
	}
	base, ext := name, ""
	if file {
		if e := path.Ext(name); e != "" && e != name {
			base, ext = strings.TrimSuffix(name, e), e
		}
	}
	for n := 2; ; n++ {
		if c := fmt.Sprintf("%s (%d)%s", base, n, ext); !used[strings.ToLower(c)] {
			return c
		}
	}
}

func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Entry is a file of a download archive: its path inside the archive and where its bytes are.
type Entry struct {
	Path, Key, ContentType string
	StorageID              *string
	Size                   int64
	ModTime                time.Time
}

// Archive is what «Скачать» of files and folders gives: every file, folders with all they hold, as paths.
func (s *Store) Archive(ctx context.Context, d Drive, sel Selection) ([]Entry, error) {
	var out []Entry
	add := func(prefix, where string, arg any) error {
		rows, err := s.db.Query(ctx, `SELECT name, object_key, content_type, storage_id, size, created_at FROM files WHERE `+where+
			` AND drive = $2 AND drive_owner IS NOT DISTINCT FROM $3 AND deleted_at IS NULL AND `+fmt.Sprintf(visibleSQL, "folder_id", "$4")+
			` ORDER BY name`, arg, d.Kind, d.owner(), d.viewerArg())
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Entry, error) {
			var e Entry
			err := row.Scan(&e.Path, &e.Key, &e.ContentType, &e.StorageID, &e.Size, &e.ModTime)
			e.Path = prefix + e.Path
			return e, err
		})
		out = append(out, list...)
		return err
	}
	if len(sel.Files) > 0 {
		if err := add("", `id::text = ANY($1)`, sel.Files); err != nil {
			return nil, err
		}
	}
	var walk func(id, prefix string, depth int) error
	walk = func(id, prefix string, depth int) error {
		if depth > 50 {
			return nil
		}
		if err := add(prefix, `folder_id = $1`, id); err != nil {
			return err
		}
		rows, err := s.db.Query(ctx, `SELECT id::text, coalesce(parent_id::text, ''), name FROM drive_folders WHERE parent_id = $1 AND deleted_at IS NULL
			AND `+fmt.Sprintf(visibleSQL, "id", "$2")+` ORDER BY name`, id, d.viewerArg())
		if err != nil {
			return err
		}
		subs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Folder])
		if err != nil {
			return err
		}
		for _, f := range subs {
			if err := walk(f.ID, prefix+f.Name+"/", depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, id := range sel.Folders {
		trail, err := s.path(ctx, d, id)
		if err != nil {
			return nil, err
		}
		if err := walk(id, trail[len(trail)-1].Name+"/", 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}
