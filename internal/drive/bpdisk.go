package drive

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The steps of the «Диск» group of the designer of business processes: a folder made, a file or a folder
// copied, moved, put into the trash or read, of an employee — with their rights on the drives, as «От имени
// пользователя» of those steps.

// Object is a file or a folder of a drive as a step of «Диск» finds it.
type Object struct {
	ID, Kind, Name   string // Kind: file | folder
	Which            string // the drive, as Open takes it: company, my, a project's key
	Drive            Drive
	FolderID, Folder string    // the folder it lies in, its name ("" — the drive's top)
	ByID, By         string    // who uploaded (made) it
	Size             int64     // a file's
	ContentType      string    // a file's
	CreatedAt        time.Time // a file's: of its current version
}

// Link is the object's address in go24: a file's card, a folder in its drive.
func (o Object) Link() string {
	if o.Kind == "file" {
		return "/files/" + o.ID + "/about"
	}
	return o.Drive.Path() + "?folder=" + o.ID
}

// FolderLink is the address of the folder it lies in.
func (o Object) FolderLink() string {
	if o.FolderID == "" {
		return o.Drive.Path()
	}
	return o.Drive.Path() + "?folder=" + o.FolderID
}

// ObjectOf finds a file or a folder the user sees by ref — its id or a link (/files/ID/about, /disk/…?folder=ID).
func (s *Store) ObjectOf(ctx context.Context, userID, ref string) (Object, error) {
	ref = strings.TrimSpace(ref)
	kind := ""
	if i := strings.Index(ref, "/files/"); i >= 0 {
		kind = "file"
		ref, _, _ = strings.Cut(strings.TrimPrefix(ref[i:], "/files/"), "/")
	}
	if i := strings.Index(ref, "folder="); i >= 0 {
		kind = "folder"
		ref, _, _ = strings.Cut(ref[i+len("folder="):], "&")
	}
	if ref == "" || strings.ContainsAny(ref, "/?& ") {
		return Object{}, ErrNotFound
	}
	var o Object
	var owner, key string
	err := pgx.ErrNoRows
	if kind != "folder" {
		o.Kind = "file"
		err = s.db.QueryRow(ctx, `SELECT f.id::text, f.name, f.drive, coalesce(f.drive_owner::text, ''), coalesce(p.key, ''),
				coalesce(f.folder_id::text, ''), coalesce(df.name, ''), coalesce(f.uploaded_by::text, ''), coalesce(u.name, ''),
				f.size, f.content_type, f.created_at
			FROM files f LEFT JOIN projects p ON f.drive = 'project' AND p.id = f.drive_owner
				LEFT JOIN drive_folders df ON df.id = f.folder_id LEFT JOIN users u ON u.id = f.uploaded_by
			WHERE f.id::text = $1 AND f.drive IS NOT NULL AND f.deleted_at IS NULL`, ref).
			Scan(&o.ID, &o.Name, &o.Drive.Kind, &owner, &key, &o.FolderID, &o.Folder, &o.ByID, &o.By, &o.Size, &o.ContentType, &o.CreatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) && kind != "file" {
		o.Kind = "folder"
		err = s.db.QueryRow(ctx, `SELECT f.id::text, f.name, f.drive, coalesce(f.owner_id::text, ''), coalesce(p.key, ''),
				coalesce(f.parent_id::text, ''), coalesce(up.name, ''), coalesce(f.created_by::text, ''), coalesce(u.name, ''), f.created_at
			FROM drive_folders f LEFT JOIN projects p ON f.drive = 'project' AND p.id = f.owner_id
				LEFT JOIN drive_folders up ON up.id = f.parent_id LEFT JOIN users u ON u.id = f.created_by
			WHERE f.id::text = $1 AND f.deleted_at IS NULL`, ref).
			Scan(&o.ID, &o.Name, &o.Drive.Kind, &owner, &key, &o.FolderID, &o.Folder, &o.ByID, &o.By, &o.CreatedAt)
	}
	if errors.Is(err, pgx.ErrNoRows) || isFKOrUUID(err) {
		return Object{}, ErrNotFound
	}
	if err != nil {
		return Object{}, err
	}
	switch o.Drive.Kind {
	case "company":
		o.Which = "company"
	case "project":
		o.Which = key
	default: // someone else's «Мой диск» is not the steps' to reach
		if owner != userID {
			return Object{}, ErrNotFound
		}
		o.Which = "my"
	}
	if o.Drive, err = s.Open(ctx, userID, o.Which); err != nil {
		return Object{}, err
	}
	if o.Kind == "file" {
		_, err = s.FileOn(ctx, o.Drive, o.ID)
	} else {
		_, err = s.path(ctx, o.Drive, o.ID)
	}
	if err != nil {
		return Object{}, err
	}
	return o, nil
}

// MakeFolder makes the folders of dir ("a/b") at the top of the drive which, as the user, those already there
// staying, and finds the deepest.
func (s *Store) MakeFolder(ctx context.Context, userID, which, dir string) (Object, error) {
	d, err := s.Open(ctx, userID, which)
	if err != nil {
		return Object{}, err
	}
	id, err := s.EnsurePath(ctx, userID, d, "", dir)
	if err != nil {
		return Object{}, err
	}
	if id == "" {
		return Object{}, ErrNotFound
	}
	return s.ObjectOf(ctx, userID, id)
}

// CopyObject copies (or moves) the object into the folder of the drive which ("" — its top), as the user.
func (s *Store) CopyObject(ctx context.Context, userID string, o Object, which, folderID string, move bool, copyBytes CopyBytes) error {
	dst, err := s.Open(ctx, userID, which)
	if err != nil {
		return err
	}
	sel := Selection{Files: []string{o.ID}}
	if o.Kind == "folder" {
		sel = Selection{Folders: []string{o.ID}}
	}
	if move {
		return s.Move(ctx, userID, o.Drive, sel, dst, folderID)
	}
	return s.Copy(ctx, userID, o.Drive, sel, dst, folderID, copyBytes)
}

// TrashObject puts the object into its drive's trash, as the user.
func (s *Store) TrashObject(ctx context.Context, userID string, o Object) error {
	if o.Kind == "folder" {
		return s.DeleteFolder(ctx, userID, o.Drive, o.ID)
	}
	return s.DeleteFile(ctx, userID, o.Drive, o.ID)
}
