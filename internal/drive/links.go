package drive

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"go24/internal/i18n"
)

// Link is the public link of a file or a folder of a drive.
type Link struct {
	Token     string
	FileID    string // or
	FolderID  string
	Password  bool
	ExpiresAt *time.Time
	Downloads int
	CreatedAt time.Time
}

// ErrLinksOff: the company has turned public links off.
var ErrLinksOff = i18n.E("err.disk_links_off")

// SetLink makes (or changes) the public link of a file (kind "file") or a folder of the drive: those who write in the
// drive. An empty password keeps the one set before unless clear is asked; expires nil — no end.
func (s *Store) SetLink(ctx context.Context, userID string, d Drive, kind, id, password string, clearPassword bool, expires *time.Time) (string, error) {
	if !d.CanWrite {
		return "", ErrForbidden
	}
	if off, err := s.off(ctx, offLinks); err != nil {
		return "", err
	} else if off {
		return "", ErrLinksOff
	}
	if err := s.mayLink(ctx, d, kind, id); err != nil {
		return "", err
	}
	if expires != nil && expires.Before(time.Now()) {
		return "", i18n.E("err.link_expired_date")
	}
	hash := ""
	if password != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", err
		}
		hash = string(h)
	}
	col := "folder_id"
	if kind == "file" {
		col = "file_id"
	}
	var token string
	err := s.db.QueryRow(ctx, `INSERT INTO drive_links (token, `+col+`, password_hash, expires_at, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (`+col+`) WHERE `+col+` IS NOT NULL DO UPDATE SET expires_at = EXCLUDED.expires_at,
			password_hash = CASE WHEN $6 THEN '' WHEN EXCLUDED.password_hash <> '' THEN EXCLUDED.password_hash ELSE drive_links.password_hash END
		RETURNING token`, randomHex(16), id, hash, expires, userID, clearPassword).Scan(&token)
	return token, err
}

// mayLink: the file (kind "file") or the folder is in the drive, and the user writes where it is.
func (s *Store) mayLink(ctx context.Context, d Drive, kind, id string) error {
	folder := id
	if kind == "file" {
		var err error
		if _, folder, err = s.fileOf(ctx, s.db, d, id); err != nil {
			return err
		}
	} else if _, err := s.path(ctx, d, id); err != nil {
		return err
	}
	if in, err := s.Within(ctx, d, folder); err != nil || !in.CanWrite {
		return orForbidden(err)
	}
	return nil
}

// DeleteLink switches the public link of a file or a folder off.
func (s *Store) DeleteLink(ctx context.Context, userID string, d Drive, kind, id string) error {
	if !d.CanWrite {
		return ErrForbidden
	}
	if err := s.mayLink(ctx, d, kind, id); err != nil {
		return err
	}
	if kind == "file" {
		_, err := s.db.Exec(ctx, `DELETE FROM drive_links WHERE file_id::text = $1`, id)
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM drive_links WHERE folder_id::text = $1`, id)
	return err
}

// Links are the public links of the things of a drive's folder, by the file's or folder's id.
func (s *Store) Links(ctx context.Context, ids []string) (map[string]Link, error) {
	rows, err := s.db.Query(ctx, `SELECT token, coalesce(file_id::text, ''), coalesce(folder_id::text, ''), password_hash <> '',
			expires_at, downloads, created_at
		FROM drive_links WHERE (file_id::text = ANY($1) OR folder_id::text = ANY($1))
			AND NOT EXISTS (SELECT 1 FROM drive_limits WHERE kind = 'no_links' AND bytes = 1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Link])
	out := map[string]Link{}
	for _, l := range list {
		out[l.FileID+l.FolderID] = l
	}
	return out, err
}

// Shared is what a public link opens.
type Shared struct {
	Link
	hash  string
	Drive Drive // as nobody in particular may read it
	Name  string
	Size  int64
}

// Expired: the link's time is over.
func (l Link) Expired(now time.Time) bool { return l.ExpiresAt != nil && now.After(*l.ExpiresAt) }

// Open finds a public link by its token: not expired, its file or folder not in the trash.
func (s *Store) OpenLink(ctx context.Context, token string) (Shared, error) {
	var sh Shared
	if off, err := s.off(ctx, offLinks); err != nil {
		return sh, err
	} else if off {
		return sh, ErrNotFound // public links are off: none opens until they are on again
	}
	var kind, owner string
	err := s.db.QueryRow(ctx, `SELECT l.token, coalesce(l.file_id::text, ''), coalesce(l.folder_id::text, ''), l.password_hash <> '',
			l.expires_at, l.downloads, l.created_at, l.password_hash,
			coalesce(f.drive, d.drive), coalesce(f.drive_owner::text, d.owner_id::text, ''), coalesce(f.name, d.name), coalesce(f.size, 0)
		FROM drive_links l LEFT JOIN files f ON f.id = l.file_id AND f.deleted_at IS NULL
			LEFT JOIN drive_folders d ON d.id = l.folder_id
		WHERE l.token = $1 AND (f.id IS NOT NULL OR d.id IS NOT NULL)`, token).
		Scan(&sh.Token, &sh.FileID, &sh.FolderID, &sh.Password, &sh.ExpiresAt, &sh.Downloads, &sh.CreatedAt, &sh.hash, &kind, &owner, &sh.Name, &sh.Size)
	if errors.Is(err, pgx.ErrNoRows) {
		return sh, ErrNotFound
	}
	if err != nil {
		return sh, err
	}
	if sh.Expired(time.Now()) {
		return sh, ErrNotFound
	}
	sh.Drive = Drive{Kind: kind, OwnerID: owner}
	if sh.FolderID != "" {
		if _, err := s.path(ctx, sh.Drive, sh.FolderID); err != nil {
			return sh, err
		}
	} else if _, _, err := s.fileOf(ctx, s.db, sh.Drive, sh.FileID); err != nil {
		return sh, err
	}
	return sh, nil
}

// CheckPassword: the password of a link with one.
func (sh Shared) CheckPassword(password string) bool {
	return !sh.Password || bcrypt.CompareHashAndPassword([]byte(sh.hash), []byte(password)) == nil
}

// PasswordStamp is what the browser keeps after the password was typed (changes when the password does).
func (sh Shared) PasswordStamp() string {
	if len(sh.hash) < 20 {
		return ""
	}
	return sh.hash[len(sh.hash)-20:]
}

// SharedList is a folder within a shared folder: what it holds and the path from the shared folder down.
func (s *Store) SharedList(ctx context.Context, sh Shared, folderID string) (folders []Folder, files []File, trail []Folder, err error) {
	if folderID == "" {
		folderID = sh.FolderID
	}
	if folders, files, trail, err = s.List(ctx, sh.Drive, folderID); err != nil {
		return nil, nil, nil, err
	}
	for i, f := range trail { // the shared folder must be on the path; above it is not shown
		if f.ID == sh.FolderID {
			return folders, files, trail[i:], nil
		}
	}
	return nil, nil, nil, ErrNotFound
}

// SharedFile is a file a link opens: the linked file itself or one within the shared folder.
func (s *Store) SharedFile(ctx context.Context, sh Shared, fileID string) error {
	if sh.FileID != "" {
		if fileID != sh.FileID {
			return ErrNotFound
		}
		return nil
	}
	var folder string
	if err := s.db.QueryRow(ctx, `SELECT coalesce(folder_id::text, '') FROM files WHERE id::text = $1 AND deleted_at IS NULL`, fileID).Scan(&folder); err != nil {
		return ErrNotFound
	}
	if folder == "" {
		return ErrNotFound
	}
	_, _, _, err := s.SharedList(ctx, sh, folder)
	return err
}

// Downloaded counts a download through the link.
func (s *Store) Downloaded(ctx context.Context, token string) {
	_, _ = s.db.Exec(ctx, `UPDATE drive_links SET downloads = downloads + 1 WHERE token = $1`, token)
}
