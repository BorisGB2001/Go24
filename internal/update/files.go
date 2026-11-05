package update

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// keepPaths belong to the installation, not to a release: an update neither writes nor removes them, the snapshot of
// the files for a rollback leaves them out (the same list as «make sync» keeps on the server).
var keepPaths = []string{
	".git",
	"deploy/.env",
	"deploy/backups",
	"deploy/rclone/rclone.conf",
	"deploy/theme/theme.css",
	"deploy/theme/icons",
	"deploy/authentik/blueprints/generated",
	"deploy/matrix/homeserver.yaml",
	"deploy/matrix/element-config.json",
	"deploy/go24-dev-ca.crt",
}

// Kept says whether a path of the installation (slash-separated, relative) is its own, not a release's.
func Kept(rel string) bool {
	for _, k := range keepPaths {
		if rel == k || strings.HasPrefix(rel, k+"/") {
			return true
		}
	}
	// the installation's languages and corrections; the examples next to them are a release's
	return strings.HasPrefix(rel, "deploy/locales/") && strings.HasSuffix(rel, ".json")
}

// codeDirs are the folders of pure code: a file there that the new version has not is removed (a Go file or a
// migration left over would break the build or the database), even with no list of the previous version's files.
var codeDirs = []string{"cmd/", "internal/"}

// maxArchive limits what an archive may unpack to.
const maxArchive = 2 << 30

// Unpack unpacks a .tar.gz into dir (new, empty) and returns the root of its files: an archive of one folder
// (go24-0.5.8/…) — that folder. Only folders and plain files are taken; a path out of dir is an error.
func Unpack(r io.Reader, dir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." {
			continue
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return "", fmt.Errorf("update: bad path in archive: %q", h.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if total += h.Size; total > maxArchive {
				return "", errors.New("update: archive too big")
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return "", err
			}
			if err := writeFile(dst, io.LimitReader(tr, h.Size), fs.FileMode(h.Mode)&0o755|0o644); err != nil {
				return "", err
			}
		}
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(ents) == 1 && ents[0].IsDir() {
		return filepath.Join(dir, ents[0].Name()), nil
	}
	return dir, nil
}

func writeFile(dst string, r io.Reader, mode fs.FileMode) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

// Files are the plain files under root (slash-separated, relative, sorted), without the installation's own.
func Files(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel != "." && Kept(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// Install lays the files of src (an unpacked release or snapshot) over the installation dir, owned by uid:gid (-1 —
// as they come). The installation's own files are not touched. A file of the code folders that src has not is
// removed, and so is one of old (the files of the version being replaced) that src has not. It returns src's files.
func Install(src, dir string, old []string, uid, gid int) ([]string, error) {
	files, err := Files(src)
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		from, to := filepath.Join(src, filepath.FromSlash(rel)), filepath.Join(dir, filepath.FromSlash(rel))
		if err := mkdirs(dir, path.Dir(rel), uid, gid); err != nil {
			return nil, err
		}
		st, err := os.Stat(from)
		if err != nil {
			return nil, err
		}
		in, err := os.Open(from)
		if err != nil {
			return nil, err
		}
		tmp := to + ".go24-update"
		err = writeFile(tmp, in, st.Mode().Perm())
		in.Close()
		if err == nil && uid >= 0 {
			err = os.Lchown(tmp, uid, gid)
		}
		if err == nil {
			err = os.Rename(tmp, to)
		}
		if err != nil {
			os.Remove(tmp)
			return nil, err
		}
	}
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	var gone []string
	for _, cd := range codeDirs {
		cur, err := Files(filepath.Join(dir, cd))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, f := range cur {
			gone = append(gone, cd+f)
		}
	}
	for _, rel := range append(gone, old...) {
		if !have[rel] && !Kept(rel) {
			if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	return files, nil
}

// mkdirs makes the folders of rel under dir, owned by uid:gid.
func mkdirs(dir, rel string, uid, gid int) error {
	if rel == "." || rel == "" {
		return nil
	}
	cur := dir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		if err := os.Mkdir(cur, 0o755); err == nil {
			if uid >= 0 {
				if err := os.Lchown(cur, uid, gid); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return nil
}

// Snapshot writes a .tar.gz of the installation's files (not its own ones: settings, data, backups) for a rollback.
func Snapshot(dir string, w io.Writer) error {
	files, err := Files(dir)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: rel, Mode: int64(st.Mode().Perm()), Size: st.Size(), ModTime: st.ModTime(), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
