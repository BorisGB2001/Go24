package apps

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// The limits of a static application's archive has for its archives.
const (
	MaxArchive   = 20 << 20 // the archive itself
	MaxFiles     = 500
	MaxFileSize  = 10 << 20 // a file in it, unpacked
	MaxUnpacked  = 50 << 20 // all the files, unpacked
	StaticPrefix = "/apps/static/"
)

var ErrArchive = i18n.E("err.app_archive")

// StaticFile is a file of a static application.
type StaticFile struct {
	Type string
	Data []byte
}

// unzip reads the archive's files: index.html at its root (or in its one folder, as an archive of a folder is),
// paths without «..», within the limits.
func unzip(raw []byte) (map[string]StaticFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, ErrArchive
	}
	files := map[string]StaticFile{}
	var total int64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		if strings.HasPrefix(name, "__MACOSX/") || path.Base(name) == ".DS_Store" {
			continue
		}
		clean := path.Clean("/" + name)[1:]
		if clean == "" || clean != name || strings.Contains(name, "..") {
			return nil, ErrArchive
		}
		if len(files) >= MaxFiles || f.UncompressedSize64 > MaxFileSize {
			return nil, ErrArchive
		}
		rc, err := f.Open()
		if err != nil {
			return nil, ErrArchive
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxFileSize+1))
		rc.Close()
		if err != nil || len(data) > MaxFileSize {
			return nil, ErrArchive
		}
		if total += int64(len(data)); total > MaxUnpacked {
			return nil, ErrArchive
		}
		files[clean] = StaticFile{Data: data}
	}
	if _, ok := files["index.html"]; !ok { // an archive of the folder: its one folder is the root
		var root string
		for name := range files {
			dir, _, _ := strings.Cut(name, "/")
			if dir == name || root != "" && dir != root {
				return nil, ErrArchive
			}
			root = dir
		}
		moved := map[string]StaticFile{}
		for name, f := range files {
			moved[strings.TrimPrefix(name, root+"/")] = f
		}
		if _, ok := moved["index.html"]; !ok || root == "" {
			return nil, ErrArchive
		}
		files = moved
	}
	for name, f := range files {
		f.Type = mime.TypeByExtension(path.Ext(name))
		if f.Type == "" {
			f.Type = "application/octet-stream"
		}
		files[name] = f
	}
	return files, nil
}

// SetArchive puts the archive's files in place of the static application's ones.
func (s *Store) SetArchive(ctx context.Context, id string, raw []byte) error {
	if len(raw) > MaxArchive {
		return ErrArchive
	}
	files, err := unzip(raw)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE apps SET static = true, handler_url = '', api_only = false WHERE id::text = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return i18n.E("err.not_found")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_files WHERE app_id::text = $1`, id); err != nil {
			return err
		}
		for name, f := range files {
			if _, err := tx.Exec(ctx, `INSERT INTO app_files (app_id, path, ctype, data) VALUES ($1::uuid, $2, $3, $4)`,
				id, name, f.Type, f.Data); err != nil {
				return err
			}
		}
		return nil
	})
}

// StaticFile is a file of an installed static application; a path ending in «/» (or empty) is its index.html.
func (s *Store) StaticFile(ctx context.Context, id, name string) (StaticFile, error) {
	if name == "" || strings.HasSuffix(name, "/") {
		name += "index.html"
	}
	var f StaticFile
	err := s.db.QueryRow(ctx, `SELECT f.ctype, f.data FROM app_files f JOIN apps a ON a.id = f.app_id
		WHERE a.id::text = $1 AND f.path = $2 AND a.static AND a.installed_at IS NOT NULL`, id, name).Scan(&f.Type, &f.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, i18n.E("err.not_found")
	}
	return f, err
}

// StaticFiles: each static application's number of files and their size in KB.
func (s *Store) StaticFiles(ctx context.Context) (map[string][2]int64, error) {
	rows, err := s.db.Query(ctx, `SELECT app_id::text, count(*), (coalesce(sum(length(data)), 0) + 1023) / 1024 FROM app_files GROUP BY app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]int64{}
	for rows.Next() {
		var id string
		var n, size int64
		if err := rows.Scan(&id, &n, &size); err != nil {
			return nil, err
		}
		out[id] = [2]int64{n, size}
	}
	return out, rows.Err()
}

// StaticURL is the address of the static application's page in go24 (base — go24's address).
func (a App) StaticURL(base string) string {
	return strings.TrimRight(base, "/") + StaticPrefix + a.ID + "/index.html"
}
