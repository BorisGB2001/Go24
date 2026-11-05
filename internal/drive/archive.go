package drive

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"go24/internal/i18n"
)

// An archive of «Диск» is shown without downloading it viewer does: what folders and files are in it, and
// any one file of it is downloaded alone. ZIP, TAR and TAR.GZ; RAR and 7z are only downloaded.

// ArchiveMax is the largest archive that is opened (a ZIP is read through a temporary file); ArchiveShown is how many
// of its entries are listed.
const (
	ArchiveMax   = 1 << 30
	ArchiveShown = 5000
)

var (
	ErrArchiveBig    = i18n.E("err.archive_big")
	ErrArchiveBroken = i18n.E("err.archive_broken")
	ErrArchiveLocked = i18n.E("err.archive_locked")
)

// ArchiveEntry is a folder or a file of an archive; Path is its full path with «/», Locked — a file of a ZIP under a
// password, which is not extracted.
type ArchiveEntry struct {
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Depth    int       `json:"depth"`
	Dir      bool      `json:"dir"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	Locked   bool      `json:"locked,omitempty"`
}

// Archive is the contents of an archive: the folders and files in the order of their paths (at most ArchiveShown —
// More), how many files there are and their unpacked size.
type Archive struct {
	Entries []ArchiveEntry `json:"entries"`
	Files   int            `json:"files"`
	Size    int64          `json:"size"`
	More    bool           `json:"more,omitempty"`
}

// ArchiveKind is how a file is read as an archive: "zip", "tar", "tgz" or "" — not an archive go24 opens.
func ArchiveKind(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".zip"):
		return "zip"
	case strings.HasSuffix(n, ".tar"):
		return "tar"
	case strings.HasSuffix(n, ".tar.gz"), strings.HasSuffix(n, ".tgz"):
		return "tgz"
	}
	return ""
}

// ListArchive reads the contents of an archive of the kind (ArchiveKind) of size bytes.
func ListArchive(kind string, src io.Reader, size int64) (Archive, error) {
	var a Archive
	seen := map[string]bool{}
	add := func(e ArchiveEntry) {
		if e.Path == "" || seen[e.Path] {
			return
		}
		seen[e.Path] = true
		if !e.Dir {
			a.Files++
			a.Size += e.Size
		}
		a.Entries = append(a.Entries, e)
	}
	err := walkArchive(kind, src, size, func(e ArchiveEntry, _ func() (io.Reader, error)) (bool, error) {
		// the folders of a path are shown though the archive may not list them itself
		for dir := path.Dir(e.Path); dir != "." && !seen[dir]; dir = path.Dir(dir) {
			add(entryOf(dir, true, 0, time.Time{}, false))
		}
		add(e)
		return false, nil
	})
	if err != nil {
		return Archive{}, err
	}
	// a tree viewer shows it: in each folder its folders first, then its files
	key := func(e ArchiveEntry) string {
		parts := strings.Split(e.Path, "/")
		for i := range parts {
			k := "0"
			if i == len(parts)-1 && !e.Dir {
				k = "1"
			}
			parts[i] = k + parts[i]
		}
		return strings.Join(parts, "\x00")
	}
	slices.SortFunc(a.Entries, func(x, y ArchiveEntry) int { return strings.Compare(key(x), key(y)) })
	if len(a.Entries) > ArchiveShown {
		a.Entries, a.More = a.Entries[:ArchiveShown], true
	}
	return a, nil
}

// ArchiveFile finds the file of the path in an archive and gives it to fn; ErrNotFound — there is no such file.
func ArchiveFile(kind string, src io.Reader, size int64, p string, fn func(ArchiveEntry, io.Reader) error) error {
	p = cleanEntry(p)
	found := false
	err := walkArchive(kind, src, size, func(e ArchiveEntry, open func() (io.Reader, error)) (bool, error) {
		if e.Dir || e.Path != p {
			return false, nil
		}
		found = true
		if e.Locked {
			return true, ErrArchiveLocked
		}
		r, err := open()
		if err != nil {
			return true, ErrArchiveBroken
		}
		return true, fn(e, r)
	})
	if err == nil && !found {
		err = ErrNotFound
	}
	return err
}

// walkArchive gives each entry of an archive to fn with the way to read it, until fn says it is done.
func walkArchive(kind string, src io.Reader, size int64, fn func(ArchiveEntry, func() (io.Reader, error)) (bool, error)) error {
	if size > ArchiveMax {
		return ErrArchiveBig
	}
	switch kind {
	case "zip":
		return walkZip(src, size, fn)
	case "tar":
		return walkTar(src, fn)
	case "tgz":
		gz, err := gzip.NewReader(src)
		if err != nil {
			return ErrArchiveBroken
		}
		defer gz.Close()
		return walkTar(gz, fn)
	}
	return ErrArchiveBroken
}

// walkZip reads the archive through a temporary file: a ZIP's directory is at its end.
func walkZip(src io.Reader, size int64, fn func(ArchiveEntry, func() (io.Reader, error)) (bool, error)) error {
	tmp, err := os.CreateTemp("", "go24-zip-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	n, err := io.Copy(tmp, io.LimitReader(src, ArchiveMax+1))
	if err != nil {
		return err
	}
	if n > ArchiveMax {
		return ErrArchiveBig
	}
	zr, err := zip.NewReader(tmp, n)
	if err != nil {
		return ErrArchiveBroken
	}
	for _, f := range zr.File {
		name := f.Name
		if f.NonUTF8 && !utf8.ValidString(name) {
			// Windows' «Сжатая ZIP-папка» writes the names in the DOS code page
			if s, err := charmap.CodePage866.NewDecoder().String(name); err == nil {
				name = s
			}
		}
		dir := strings.HasSuffix(name, "/") || f.FileInfo().IsDir()
		e := entryOf(name, dir, int64(f.UncompressedSize64), f.Modified, f.Flags&1 != 0)
		var rc io.ReadCloser
		done, err := fn(e, func() (io.Reader, error) {
			var err error
			rc, err = f.Open()
			return rc, err
		})
		if rc != nil {
			rc.Close()
		}
		if done || err != nil {
			return err
		}
	}
	return nil
}

func walkTar(src io.Reader, fn func(ArchiveEntry, func() (io.Reader, error)) (bool, error)) error {
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return ErrArchiveBroken
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		default:
			continue // links, devices and the headers of pax are not files to show
		}
		e := entryOf(h.Name, h.Typeflag == tar.TypeDir, h.Size, h.ModTime, false)
		if done, err := fn(e, func() (io.Reader, error) { return tr, nil }); done || err != nil {
			return err
		}
	}
}

// entryOf is an entry of the path as the archive names it: «\» of Windows and the leading «/» and «./» go away.
func entryOf(name string, dir bool, size int64, mod time.Time, locked bool) ArchiveEntry {
	p := cleanEntry(name)
	e := ArchiveEntry{Path: p, Name: path.Base(p), Depth: strings.Count(p, "/"), Dir: dir, Modified: mod, Locked: locked && !dir}
	if p == "" {
		e.Name = ""
	}
	if !dir {
		e.Size = size
	}
	return e
}

func cleanEntry(name string) string {
	p := path.Clean("/" + strings.ReplaceAll(name, `\`, "/"))
	return strings.TrimPrefix(p, "/")
}
