package drive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/text/encoding/charmap"
)

// TestArchive: the contents of a ZIP and a TAR.GZ — the folders a path implies (before the files), Windows' names in cp866, a file under a
// password — and one file of it read alone.
func TestArchive(t *testing.T) {
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	add := func(h *zip.FileHeader, body string) {
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	add(&zip.FileHeader{Name: "a/b/c.txt", Method: zip.Deflate}, "hello")
	dos, _ := charmap.CodePage866.NewEncoder().String("Отчёт.txt")
	add(&zip.FileHeader{Name: dos, NonUTF8: true}, "12")
	add(&zip.FileHeader{Name: `..\up.txt`, Flags: 1}, "x")
	add(&zip.FileHeader{Name: "0.txt"}, "")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	a, err := ListArchive("zip", bytes.NewReader(zb.Bytes()), int64(zb.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range a.Entries {
		paths = append(paths, e.Path)
	}
	if got := strings.Join(paths, " "); got != "a a/b a/b/c.txt 0.txt up.txt Отчёт.txt" || a.Files != 4 || a.Size != 8 {
		t.Fatalf("zip: %q, %d files, %d bytes", got, a.Files, a.Size)
	}
	if e := a.Entries[2]; e.Name != "c.txt" || e.Depth != 2 || e.Dir || !a.Entries[0].Dir || !a.Entries[4].Locked {
		t.Errorf("zip entries: %+v", a.Entries)
	}
	read := func(kind string, data []byte, p string) (string, error) {
		var out string
		err := ArchiveFile(kind, bytes.NewReader(data), int64(len(data)), p, func(_ ArchiveEntry, r io.Reader) error {
			b, err := io.ReadAll(r)
			out = string(b)
			return err
		})
		return out, err
	}
	if s, err := read("zip", zb.Bytes(), "/a/b/c.txt"); err != nil || s != "hello" {
		t.Errorf("zip file: %q %v", s, err)
	}
	if _, err := read("zip", zb.Bytes(), "up.txt"); !errors.Is(err, ErrArchiveLocked) {
		t.Errorf("a locked file: %v", err)
	}
	if _, err := read("zip", zb.Bytes(), "a/b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a folder as a file: %v", err)
	}
	if _, err := ListArchive("zip", strings.NewReader("not a zip"), 9); !errors.Is(err, ErrArchiveBroken) {
		t.Errorf("not a zip: %v", err)
	}
	if _, err := ListArchive("zip", strings.NewReader(""), ArchiveMax+1); !errors.Is(err, ErrArchiveBig) {
		t.Errorf("too big: %v", err)
	}

	var tb bytes.Buffer
	gz := gzip.NewWriter(&tb)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "./docs/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "./docs/r.md", Typeflag: tar.TypeReg, Size: 3, Mode: 0o644})
	tw.Write([]byte("# r"))
	tw.WriteHeader(&tar.Header{Name: "./link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	tw.Close()
	gz.Close()
	a, err = ListArchive("tgz", bytes.NewReader(tb.Bytes()), int64(tb.Len()))
	if err != nil || len(a.Entries) != 2 || a.Entries[1].Path != "docs/r.md" || a.Files != 1 {
		t.Fatalf("tgz: %+v %v", a, err)
	}
	if s, err := read("tgz", tb.Bytes(), "docs/r.md"); err != nil || s != "# r" {
		t.Errorf("tgz file: %q %v", s, err)
	}
	if _, err := read("tgz", tb.Bytes(), "link"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a link is not a file: %v", err)
	}
}
