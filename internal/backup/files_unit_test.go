package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// memObjects implements the Objects interface in memory for unit testing without S3.
type memObjects struct {
	files   map[string]string
	modTime map[string]time.Time
	walkErr error
	getErr  error
	putErr  error
	puts    map[string]string
}

func newMemObjects() *memObjects {
	return &memObjects{
		files:   make(map[string]string),
		modTime: make(map[string]time.Time),
		puts:    make(map[string]string),
	}
}

func (m *memObjects) Walk(ctx context.Context, fn func(key string, size int64, mod time.Time) error) error {
	if m.walkErr != nil {
		return m.walkErr
	}
	keys := make([]string, 0, len(m.files))
	for k := range m.files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		data := m.files[k]
		if err := fn(k, int64(len(data)), m.modTime[k]); err != nil {
			return err
		}
	}
	return nil
}

func (m *memObjects) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	data, ok := m.files[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

func (m *memObjects) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	if m.putErr != nil {
		return m.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.puts[key] = string(b)
	return nil
}

// TestMirrorPutBasic tests putting a new file into the destination folder.
func TestMirrorPutBasic(t *testing.T) {
	dst := t.TempDir()
	m := &mirror{dst: dst}

	mod := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	content := "file content hello"

	err := m.put("documents/readme.txt", int64(len(content)), mod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	})
	if err != nil {
		t.Fatalf("put failed: %v", err)
	}

	if m.files != 1 || m.fresh != 1 || m.bytes != int64(len(content)) {
		t.Fatalf("mirror stats mismatch: files=%d, fresh=%d, bytes=%d", m.files, m.fresh, m.bytes)
	}

	outPath := filepath.Join(dst, "documents", "readme.txt")
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(got) != content {
		t.Fatalf("file content = %q, want %q", string(got), content)
	}

	fi, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat created file: %v", err)
	}
	if !fi.ModTime().Equal(mod) {
		t.Fatalf("modtime = %v, want %v", fi.ModTime(), mod)
	}
}

// TestMirrorPutRelPaths tests relative path sanitization, leading slashes, and empty or dot paths.
func TestMirrorPutRelPaths(t *testing.T) {
	dst := t.TempDir()
	m := &mirror{dst: dst}
	mod := time.Now().Truncate(time.Second)

	// Empty and dot paths should be ignored without error or stats change.
	for _, emptyRel := range []string{"", ".", "/", "/.", `\`} {
		err := m.put(emptyRel, 0, mod, func() (io.ReadCloser, error) {
			t.Fatal("open should not be called for empty rel")
			return nil, nil
		})
		if err != nil {
			t.Errorf("put(%q) error: %v", emptyRel, err)
		}
		if m.files != 0 || m.fresh != 0 {
			t.Errorf("put(%q) modified stats: %+v", emptyRel, m)
		}
	}

	// Slashes, backslashes, and dot-dot inside path.
	err := m.put(`\sub\nested\..\file.txt`, 4, mod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("data")), nil
	})
	if err != nil {
		t.Fatalf("put with backslashes failed: %v", err)
	}
	expectedFile := filepath.Join(dst, "sub", "file.txt")
	if _, err := os.Stat(expectedFile); err != nil {
		t.Fatalf("expected file at %s: %v", expectedFile, err)
	}
}

// TestMirrorPutHardlink tests hardlinking behavior when a previous backup folder is present.
func TestMirrorPutHardlink(t *testing.T) {
	dir := t.TempDir()
	dst1 := filepath.Join(dir, "bk1")
	dst2 := filepath.Join(dir, "bk2")

	mod := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	content := "shared payload for hard link test"

	// First backup.
	m1 := &mirror{dst: dst1}
	if err := m1.put("data/item.bin", int64(len(content)), mod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	}); err != nil {
		t.Fatalf("first put failed: %v", err)
	}

	// Second backup with prev pointing to dst1: identical size and modtime.
	m2 := &mirror{dst: dst2, prev: dst1}
	openCalled := false
	if err := m2.put("data/item.bin", int64(len(content)), mod, func() (io.ReadCloser, error) {
		openCalled = true
		return io.NopCloser(strings.NewReader(content)), nil
	}); err != nil {
		t.Fatalf("second put failed: %v", err)
	}

	if openCalled {
		t.Error("open was called for an identical unchanged file")
	}
	if m2.files != 1 {
		t.Errorf("m2.files = %d, want 1", m2.files)
	}
	if m2.fresh != 0 {
		t.Errorf("m2.fresh = %d, want 0 (hard-linked)", m2.fresh)
	}
	if m2.bytes != 0 {
		t.Errorf("m2.bytes = %d, want 0", m2.bytes)
	}

	// Verify that the file in bk2 is a hard link to the file in bk1.
	fi1, err := os.Stat(filepath.Join(dst1, "data", "item.bin"))
	if err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(filepath.Join(dst2, "data", "item.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(fi1, fi2) {
		t.Error("expected bk2/data/item.bin to be a hard link to bk1/data/item.bin")
	}

	// Third backup: modified modtime -> must write anew.
	dst3 := filepath.Join(dir, "bk3")
	m3 := &mirror{dst: dst3, prev: dst1}
	diffMod := mod.Add(time.Hour)
	if err := m3.put("data/item.bin", int64(len(content)), diffMod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(content)), nil
	}); err != nil {
		t.Fatalf("put with modified time failed: %v", err)
	}
	if m3.fresh != 1 || m3.bytes != int64(len(content)) {
		t.Errorf("expected fresh write when modtime differs: %+v", m3)
	}

	// Fourth backup: modified size -> must write anew.
	dst4 := filepath.Join(dir, "bk4")
	m4 := &mirror{dst: dst4, prev: dst1}
	diffContent := content + " extra"
	if err := m4.put("data/item.bin", int64(len(diffContent)), mod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(diffContent)), nil
	}); err != nil {
		t.Fatalf("put with modified size failed: %v", err)
	}
	if m4.fresh != 1 || m4.bytes != int64(len(diffContent)) {
		t.Errorf("expected fresh write when size differs: %+v", m4)
	}
}

// TestMirrorPutErrors tests error propagation when opening or writing fails.
func TestMirrorPutErrors(t *testing.T) {
	dst := t.TempDir()
	m := &mirror{dst: dst}
	mod := time.Now()

	expectedErr := errors.New("open error simulated")
	err := m.put("test.txt", 10, mod, func() (io.ReadCloser, error) {
		return nil, expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("put error = %v, want %v", err, expectedErr)
	}
	if m.fresh != 0 || m.bytes != 0 {
		t.Errorf("failed put should not increment fresh or bytes: %+v", m)
	}

	// Creating a file where a directory exists with the same name.
	dirConflict := filepath.Join(dst, "conflict")
	if err := os.Mkdir(dirConflict, 0o700); err != nil {
		t.Fatal(err)
	}
	err = m.put("conflict", 5, mod, func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("hello")), nil
	})
	if err == nil {
		t.Error("expected error when trying to write a file over an existing directory")
	}
}

// TestCopyObjects tests mirroring an Objects bucket to a local destination folder.
func TestCopyObjects(t *testing.T) {
	ctx := context.Background()
	objs := newMemObjects()
	mod := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)

	// Add folder marker (ends with /) and regular files.
	objs.files["folder/"] = ""
	objs.modTime["folder/"] = mod

	objs.files["docs/spec.txt"] = "specification text"
	objs.modTime["docs/spec.txt"] = mod

	objs.files["avatar.png"] = "binarypngdata"
	objs.modTime["avatar.png"] = mod

	dst1 := filepath.Join(t.TempDir(), "copy1")
	m1, err := copyObjects(ctx, objs, dst1, "")
	if err != nil {
		t.Fatalf("copyObjects first run failed: %v", err)
	}
	if m1.files != 2 || m1.fresh != 2 {
		t.Fatalf("first copyObjects stats mismatch: files=%d, fresh=%d", m1.files, m1.fresh)
	}

	// Ensure files exist on disk.
	if _, err := os.Stat(filepath.Join(dst1, "docs", "spec.txt")); err != nil {
		t.Errorf("spec.txt not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst1, "avatar.png")); err != nil {
		t.Errorf("avatar.png not found: %v", err)
	}

	// Second run with prev: should hard link both unchanged files.
	dst2 := filepath.Join(t.TempDir(), "copy2")
	m2, err := copyObjects(ctx, objs, dst2, dst1)
	if err != nil {
		t.Fatalf("copyObjects second run failed: %v", err)
	}
	if m2.files != 2 || m2.fresh != 0 || m2.bytes != 0 {
		t.Fatalf("second copyObjects stats mismatch: files=%d, fresh=%d, bytes=%d", m2.files, m2.fresh, m2.bytes)
	}

	// Test error propagation from Walk.
	objsErr := newMemObjects()
	objsErr.walkErr = errors.New("walk failure")
	if _, err := copyObjects(ctx, objsErr, filepath.Join(t.TempDir(), "err"), ""); err == nil {
		t.Error("expected error when objs.Walk fails")
	}

	// Test error propagation from Get.
	objsGetErr := newMemObjects()
	objsGetErr.files["bad.txt"] = "data"
	objsGetErr.modTime["bad.txt"] = mod
	objsGetErr.getErr = errors.New("get failure")
	if _, err := copyObjects(ctx, objsGetErr, filepath.Join(t.TempDir(), "err2"), ""); err == nil {
		t.Error("expected error when objs.Get fails")
	}
}

// TestCopyTree tests mirroring a directory tree from src to dst.
func TestCopyTree(t *testing.T) {
	ctx := context.Background()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	mod := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	f1 := filepath.Join(src, "file1.txt")
	if err := os.WriteFile(f1, []byte("tree file 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(f1, mod, mod)

	f2 := filepath.Join(src, "sub", "file2.txt")
	if err := os.WriteFile(f2, []byte("tree file 2"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(f2, mod, mod)

	// Symlink in src must be skipped by copyTree.
	_ = os.Symlink(f1, filepath.Join(src, "link.txt"))

	dst1 := filepath.Join(t.TempDir(), "dst1")
	m1, err := copyTree(ctx, src, dst1, "")
	if err != nil {
		t.Fatalf("copyTree failed: %v", err)
	}
	if m1.files != 2 || m1.fresh != 2 {
		t.Fatalf("copyTree stats mismatch: files=%d, fresh=%d", m1.files, m1.fresh)
	}

	// Link was skipped.
	if _, err := os.Lstat(filepath.Join(dst1, "link.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("symlink was unexpectedly copied")
	}

	// Second run with prev hardlinks unchanged files.
	dst2 := filepath.Join(t.TempDir(), "dst2")
	m2, err := copyTree(ctx, src, dst2, dst1)
	if err != nil {
		t.Fatalf("copyTree with prev failed: %v", err)
	}
	if m2.files != 2 || m2.fresh != 0 || m2.bytes != 0 {
		t.Fatalf("copyTree with prev stats mismatch: files=%d, fresh=%d, bytes=%d", m2.files, m2.fresh, m2.bytes)
	}

	// Non-existent src returns nil without error.
	mNone, err := copyTree(ctx, filepath.Join(t.TempDir(), "nonexistent"), filepath.Join(t.TempDir(), "dstNone"), "")
	if err != nil {
		t.Fatalf("copyTree for nonexistent src should not return error: %v", err)
	}
	if mNone.files != 0 {
		t.Fatalf("expected 0 files for nonexistent src, got %d", mNone.files)
	}

	// Context cancellation cancels copyTree.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := copyTree(cancelCtx, src, filepath.Join(t.TempDir(), "dstCancel"), ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
