package backup

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Objects are the files of go24 (the S3 bucket, see storage.Store); Put — to restore them.
type Objects interface {
	Walk(ctx context.Context, fn func(key string, size int64, mod time.Time) error) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
}

// mirror writes a tree of files into dst as rsync --link-dest does: a file the previous copy (prev, "" — none) has
// with the same size and time is a hard link to it, the others are written anew. Every copy is whole, the files
// that did not change take no room. It gives how many files and bytes were written anew.
type mirror struct {
	dst, prev    string
	files, fresh int
	bytes        int64
}

// put writes one file; rel is its path in the tree (cleaned: nothing outside dst).
func (m *mirror) put(rel string, size int64, mod time.Time, open func() (io.ReadCloser, error)) error {
	rel = path.Clean("/" + strings.ReplaceAll(rel, `\`, "/"))[1:]
	if rel == "" || rel == "." {
		return nil
	}
	file := filepath.Join(m.dst, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	m.files++
	if m.prev != "" {
		old := filepath.Join(m.prev, filepath.FromSlash(rel))
		if fi, err := os.Stat(old); err == nil && fi.Mode().IsRegular() && fi.Size() == size && fi.ModTime().Equal(mod.Truncate(time.Second)) {
			if err := os.Link(old, file); err == nil {
				return nil
			}
		}
	}
	r, err := open()
	if err != nil {
		return err
	}
	defer r.Close()
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	m.fresh++
	m.bytes += n
	return os.Chtimes(file, mod, mod.Truncate(time.Second))
}

// copyObjects mirrors the bucket into dst.
func copyObjects(ctx context.Context, objs Objects, dst, prev string) (*mirror, error) {
	m := &mirror{dst: dst, prev: prev}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	err := objs.Walk(ctx, func(key string, size int64, mod time.Time) error {
		if strings.HasSuffix(key, "/") { // a folder marker
			return nil
		}
		return m.put(key, size, mod, func() (io.ReadCloser, error) { return objs.Get(ctx, key) })
	})
	return m, err
}

// copyTree mirrors a folder of the server into dst (files only; links and the like are left).
func copyTree(ctx context.Context, src, dst, prev string) (*mirror, error) {
	m := &mirror{dst: dst, prev: prev}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return nil, err
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == src {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		return m.put(filepath.ToSlash(rel), info.Size(), info.ModTime(), func() (io.ReadCloser, error) { return os.Open(p) })
	})
	return m, err
}
