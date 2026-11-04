package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLastLines tests extracting the last N lines from a multi-line string.
func TestLastLines(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"", 5, ""},
		{"single line", 3, "single line"},
		{"one\ntwo\nthree", 2, "two\nthree"},
		{"one\ntwo\nthree", 5, "one\ntwo\nthree"},
		{"line1\nline2\nline3\nline4\nline5\n\n", 3, "line3\nline4\nline5"},
	}
	for _, tt := range tests {
		got := lastLines(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("lastLines(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

// createTestTarGz creates in-memory tar.gz bytes for testing unpack.
func createTestTarGz(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, data := range entries {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}
		if strings.HasSuffix(name, "/") {
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		} else {
			hdr.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 {
			if _, err := tw.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestUnpackTar tests extraction of directory and file entries, path sanitization, and error handling.
func TestUnpackTar(t *testing.T) {
	dst := t.TempDir()

	entries := map[string][]byte{
		"dir/":                nil,
		"dir/hello.txt":       []byte("world"),
		"root.json":           []byte(`{"status":"ok"}`),
		"../../escaped.txt":   []byte("traversal prevented"),
		"/absolute/path.conf": []byte("config line"),
	}
	tgz := createTestTarGz(t, entries)

	if err := unpackTar(dst, tgz); err != nil {
		t.Fatalf("unpackTar failed: %v", err)
	}

	// Verify standard files.
	helloData, err := os.ReadFile(filepath.Join(dst, "dir", "hello.txt"))
	if err != nil || string(helloData) != "world" {
		t.Errorf("dir/hello.txt = (%q, %v), want 'world'", helloData, err)
	}
	rootData, err := os.ReadFile(filepath.Join(dst, "root.json"))
	if err != nil || string(rootData) != `{"status":"ok"}` {
		t.Errorf("root.json = (%q, %v)", rootData, err)
	}

	// Traversal path "../../escaped.txt" should be cleaned to "/escaped.txt" and placed inside dst.
	escapedData, err := os.ReadFile(filepath.Join(dst, "escaped.txt"))
	if err != nil || string(escapedData) != "traversal prevented" {
		t.Errorf("escaped.txt should be inside dst: (%q, %v)", escapedData, err)
	}

	// Absolute path "/absolute/path.conf" should be placed inside dst as dst/absolute/path.conf.
	absData, err := os.ReadFile(filepath.Join(dst, "absolute", "path.conf"))
	if err != nil || string(absData) != "config line" {
		t.Errorf("absolute/path.conf should be inside dst: (%q, %v)", absData, err)
	}

	// Invalid gzip data.
	if err := unpackTar(dst, []byte("not-gzip")); err == nil {
		t.Error("expected error for non-gzip payload")
	}

	// Corrupted gzip stream.
	corrupted := append(tgz[:len(tgz)/2], []byte("corrupted_tail")...)
	if err := unpackTar(t.TempDir(), corrupted); err == nil {
		t.Error("expected error for corrupted tar.gz payload")
	}
}

// TestUnpack tests unpack merging of plain config.tar.gz file and optional decrypted secret tar.gz bytes.
func TestUnpack(t *testing.T) {
	dst := t.TempDir()
	plainFile := filepath.Join(t.TempDir(), "config.tar.gz")

	plainEntries := map[string][]byte{
		"app.conf": []byte("port=8080"),
	}
	plainTgz := createTestTarGz(t, plainEntries)
	if err := os.WriteFile(plainFile, plainTgz, 0o600); err != nil {
		t.Fatal(err)
	}

	secretEntries := map[string][]byte{
		".env": []byte("DB_PASS=xyz"),
	}
	secretTgz := createTestTarGz(t, secretEntries)

	// Unpack both plain and secret.
	if err := unpack(dst, plainFile, secretTgz); err != nil {
		t.Fatalf("unpack failed: %v", err)
	}

	appConf, err := os.ReadFile(filepath.Join(dst, "app.conf"))
	if err != nil || string(appConf) != "port=8080" {
		t.Errorf("app.conf mismatch: (%q, %v)", appConf, err)
	}
	envData, err := os.ReadFile(filepath.Join(dst, ".env"))
	if err != nil || string(envData) != "DB_PASS=xyz" {
		t.Errorf(".env mismatch: (%q, %v)", envData, err)
	}

	// Plain file does not exist.
	if err := unpack(dst, filepath.Join(t.TempDir(), "no-such-plain.tar.gz"), nil); err == nil {
		t.Error("expected error for missing plain tar.gz")
	}
}

// TestPutObjects tests reading a directory of files and pushing them into Objects.
func TestPutObjects(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "text.txt"), []byte("text data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "photo.png"), []byte("png image data"), 0o600); err != nil {
		t.Fatal(err)
	}

	objs := newMemObjects()
	size, err := putObjects(ctx, objs, dir)
	if err != nil {
		t.Fatalf("putObjects failed: %v", err)
	}
	expectedSize := int64(len("text data") + len("png image data"))
	if size != expectedSize {
		t.Fatalf("putObjects size = %d, want %d", size, expectedSize)
	}

	if objs.puts["text.txt"] != "text data" {
		t.Errorf("text.txt mismatch: %q", objs.puts["text.txt"])
	}
	if objs.puts["sub/photo.png"] != "png image data" {
		t.Errorf("sub/photo.png mismatch: %q", objs.puts["sub/photo.png"])
	}

	// Error when Objects.Put fails.
	errObjs := newMemObjects()
	errObjs.putErr = errors.New("simulated put error")
	if _, err := putObjects(ctx, errObjs, dir); err == nil {
		t.Error("expected error when objs.Put fails")
	}

	// Cancelled context.
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := putObjects(cancelCtx, objs, dir); !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestRunnerPutBack tests restore validation, manifest checking, and config/files restore without DB.
func TestRunnerPutBack(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewStore(nil, "restore-secret-key")

	r := &Runner{
		Root:      root,
		ConfigDir: t.TempDir(),
		Store:     store,
	}

	// Invalid copy name: does not start with go24-.
	if _, _, err := r.putBack(ctx, job{Name: "not-a-backup"}); err == nil {
		t.Error("expected error for non-go24 archive name")
	}

	// Directory traversal in copy name.
	if _, _, err := r.putBack(ctx, job{Name: "../etc"}); err == nil {
		t.Error("expected error for directory traversal copy name")
	}

	// Valid folder name, but manifest.json does not exist.
	copyDir := filepath.Join(root, "go24-20261028-120000")
	if err := os.Mkdir(copyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.putBack(ctx, job{Name: "go24-20261028-120000"}); err == nil {
		t.Error("expected error for missing manifest.json")
	}

	// Invalid manifest JSON.
	if err := os.WriteFile(filepath.Join(copyDir, "manifest.json"), []byte("invalid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.putBack(ctx, job{Name: "go24-20261028-120000"}); err == nil {
		t.Error("expected error for invalid manifest JSON")
	}

	// Part requested is not in copy manifest.
	m := Manifest{
		Format:    1,
		CreatedAt: time.Now(),
		Parts:     []string{"go24"},
		Files:     []string{"go24.dump"},
	}
	mBytes, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(copyDir, "manifest.json"), mBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.putBack(ctx, job{Name: "go24-20261028-120000", Parts: []string{"files"}}); err == nil {
		t.Error("expected error when requested part is not in copy")
	}

	// Config restore with secrets: missing secret password in job.
	mConfig := Manifest{
		Format:    1,
		CreatedAt: time.Now(),
		Parts:     []string{"config", "secrets"},
		Files:     []string{"config.tar.gz", "secrets.tar.gz.enc"},
		Secrets:   true,
	}
	mConfigBytes, _ := json.Marshal(mConfig)
	if err := os.WriteFile(filepath.Join(copyDir, "manifest.json"), mConfigBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.putBack(ctx, job{Name: "go24-20261028-120000", Parts: []string{"config"}}); err == nil {
		t.Error("expected error when secret password is missing for config with secrets")
	}

	// Successful config restore: valid config.tar.gz and secrets.tar.gz.enc.
	plainTgz := createTestTarGz(t, map[string][]byte{"plain.txt": []byte("plain text")})
	if err := os.WriteFile(filepath.Join(copyDir, "config.tar.gz"), plainTgz, 0o600); err != nil {
		t.Fatal(err)
	}

	secretTgz := createTestTarGz(t, map[string][]byte{".env": []byte("SECRET=true")})
	encSecret, err := Encrypt("pass12345", secretTgz)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copyDir, "secrets.tar.gz.enc"), encSecret, 0o600); err != nil {
		t.Fatal(err)
	}

	sealedPass, err := store.seal("pass12345")
	if err != nil {
		t.Fatal(err)
	}

	where, _, err := r.putBack(ctx, job{
		Name:   "go24-20261028-120000",
		Parts:  []string{"config"},
		Secret: sealedPass,
	})
	if err != nil {
		t.Fatalf("putBack config failed: %v", err)
	}
	if !strings.HasPrefix(where, "restore/go24-20261028-120000-") {
		t.Errorf("where = %q, want restore/go24-20261028-120000-...", where)
	}

	// Verify unpacked files in where destination.
	unpackedPlain, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(where), "plain.txt"))
	if err != nil || string(unpackedPlain) != "plain text" {
		t.Errorf("unpacked plain.txt mismatch: (%q, %v)", unpackedPlain, err)
	}
	unpackedSecret, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(where), ".env"))
	if err != nil || string(unpackedSecret) != "SECRET=true" {
		t.Errorf("unpacked .env mismatch: (%q, %v)", unpackedSecret, err)
	}

	// Successful files restore via Objects mock.
	filesDir := filepath.Join(copyDir, "files")
	if err := os.Mkdir(filesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filesDir, "file.pdf"), []byte("pdf document data"), 0o600); err != nil {
		t.Fatal(err)
	}
	mFiles := Manifest{
		Format:    1,
		CreatedAt: time.Now(),
		Parts:     []string{"files"},
		Files:     []string{"files/"},
	}
	mFilesBytes, _ := json.Marshal(mFiles)
	if err := os.WriteFile(filepath.Join(copyDir, "manifest.json"), mFilesBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	objs := newMemObjects()
	r.Objects = objs
	_, size, err := r.putBack(ctx, job{
		Name:  "go24-20261028-120000",
		Parts: []string{"files"},
	})
	if err != nil {
		t.Fatalf("putBack files failed: %v", err)
	}
	if size != int64(len("pdf document data")) {
		t.Errorf("putBack files size = %d, want %d", size, len("pdf document data"))
	}
	if objs.puts["file.pdf"] != "pdf document data" {
		t.Errorf("restored file.pdf mismatch: %q", objs.puts["file.pdf"])
	}
}
