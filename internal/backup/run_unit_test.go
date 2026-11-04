package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestManifestJSON tests manifest encoding, decoding, and JSON field tags.
func TestManifestJSON(t *testing.T) {
	at := time.Date(2026, 10, 28, 15, 30, 0, 0, time.UTC)
	m := Manifest{
		Format:    1,
		CreatedAt: at,
		Version:   "0.9.5",
		Parts:     []string{"go24", "config", "secrets"},
		Files:     []string{"go24.dump", "config.tar.gz", "secrets.tar.gz.enc"},
		Secrets:   true,
		Trees: map[string]TreeStats{
			"files": {Files: 10, Fresh: 2, Bytes: 1024},
		},
	}

	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	var parsed Manifest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}

	if parsed.Format != m.Format {
		t.Errorf("Format = %d, want %d", parsed.Format, m.Format)
	}
	if !parsed.CreatedAt.Equal(m.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", parsed.CreatedAt, m.CreatedAt)
	}
	if parsed.Version != m.Version {
		t.Errorf("Version = %q, want %q", parsed.Version, m.Version)
	}
	if !parsed.Secrets {
		t.Errorf("Secrets = %v, want true", parsed.Secrets)
	}
	if stats, ok := parsed.Trees["files"]; !ok || stats.Bytes != 1024 || stats.Fresh != 2 || stats.Files != 10 {
		t.Errorf("Trees stats mismatch: %+v", stats)
	}
}

// TestRelName tests archive relative path construction given a base directory and copy name.
func TestRelName(t *testing.T) {
	tests := []struct {
		dir  string
		name string
		want string
	}{
		{"", "go24-20261028-150405", "go24-20261028-150405"},
		{"daily", "go24-20261028-150405", "daily/go24-20261028-150405"},
		{"nested/sub", "go24-20261028-150405", "nested/sub/go24-20261028-150405"},
	}
	for _, tt := range tests {
		got := relName(tt.dir, tt.name)
		if got != tt.want {
			t.Errorf("relName(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.want)
		}
	}
}

// TestSecretFile tests classification of configuration files as secrets based on name and file permissions.
func TestSecretFile(t *testing.T) {
	tests := []struct {
		rel  string
		mode fs.FileMode
		want bool
	}{
		{".env", 0o644, true},
		{"deploy/.env", 0o644, true},
		{"rclone.conf", 0o644, true},
		{"sub/rclone.conf", 0o644, true},
		{"server.key", 0o600, true},   // private 0600: group and other have 0 permissions
		{"id_rsa", 0o700, true},       // private 0700
		{"cert.key", 0o400, true},     // private 0400
		{"config.yaml", 0o644, false}, // group/other have read
		{"app.json", 0o664, false},    // group/other have read
		{"script.sh", 0o755, false},   // group/other have read
		{"data.txt", 0o640, false},    // group has read (0040 & 0044 != 0)
		{"public.txt", 0o604, false},  // other has read (0004 & 0044 != 0)
	}

	for _, tt := range tests {
		got := secretFile(tt.rel, tt.mode)
		if got != tt.want {
			t.Errorf("secretFile(%q, %v) = %v, want %v", tt.rel, tt.mode, got, tt.want)
		}
	}
}

// TestReadEnv tests parsing .env KEY=value files with comments, quotes, and whitespace.
func TestReadEnv(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")

	content := `
# Comment line
EMPTY_LINE=

KEY_SIMPLE=value123
export EXPORTED_KEY=exported_value
DOUBLE_QUOTES="quoted with spaces"
SINGLE_QUOTES='single quoted value'
UNMATCHED_QUOTES="unmatched'
SHORT_QUOTE="
WITH_SPACES   =   trimmed_value   
INVALID_LINE_NO_EQUALS
# Another comment
`
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	env := readEnv(envFile)
	if env["KEY_SIMPLE"] != "value123" {
		t.Errorf("KEY_SIMPLE = %q, want value123", env["KEY_SIMPLE"])
	}
	if env["EXPORTED_KEY"] != "exported_value" {
		t.Errorf("EXPORTED_KEY = %q, want exported_value", env["EXPORTED_KEY"])
	}
	if env["DOUBLE_QUOTES"] != "quoted with spaces" {
		t.Errorf("DOUBLE_QUOTES = %q, want 'quoted with spaces'", env["DOUBLE_QUOTES"])
	}
	if env["SINGLE_QUOTES"] != "single quoted value" {
		t.Errorf("SINGLE_QUOTES = %q, want 'single quoted value'", env["SINGLE_QUOTES"])
	}
	if env["UNMATCHED_QUOTES"] != `"unmatched'` {
		t.Errorf("UNMATCHED_QUOTES = %q", env["UNMATCHED_QUOTES"])
	}
	if env["SHORT_QUOTE"] != `"` {
		t.Errorf("SHORT_QUOTE = %q", env["SHORT_QUOTE"])
	}
	if env["WITH_SPACES"] != "trimmed_value" {
		t.Errorf("WITH_SPACES = %q, want trimmed_value", env["WITH_SPACES"])
	}
	if env["EMPTY_LINE"] != "" {
		t.Errorf("EMPTY_LINE = %q, want empty", env["EMPTY_LINE"])
	}
	if _, ok := env["INVALID_LINE_NO_EQUALS"]; ok {
		t.Error("expected line without '=' to be omitted")
	}

	// Non-existent file returns empty map.
	nonExistent := readEnv(filepath.Join(dir, "no-such-file.env"))
	if len(nonExistent) != 0 {
		t.Errorf("expected empty map for non-existent file, got %v", nonExistent)
	}
}

// TestRunnerDbURL tests constructing database URLs for go24, Authentik, and Matrix Synapse.
func TestRunnerDbURL(t *testing.T) {
	r := &Runner{
		DatabaseURL: "postgres://go24_user:go24_pass@dbhost:5432/go24?sslmode=disable",
	}

	env := map[string]string{
		"AUTHENTIK_DB_PASSWORD": "auth_db_password",
		"SYNAPSE_DB_PASSWORD":   "syn_db_password",
	}

	// go24 returns DatabaseURL directly.
	urlGo24, err := r.dbURL("go24", env)
	if err != nil {
		t.Fatalf("dbURL(go24) error: %v", err)
	}
	if urlGo24 != r.DatabaseURL {
		t.Errorf("dbURL(go24) = %q, want %q", urlGo24, r.DatabaseURL)
	}

	// authentik uses AUTHENTIK_DB_PASSWORD, db user authentik, path /authentik.
	urlAuth, err := r.dbURL("authentik", env)
	if err != nil {
		t.Fatalf("dbURL(authentik) error: %v", err)
	}
	wantAuth := "postgres://authentik:auth_db_password@dbhost:5432/authentik?sslmode=disable"
	if urlAuth != wantAuth {
		t.Errorf("dbURL(authentik) = %q, want %q", urlAuth, wantAuth)
	}

	// synapse uses SYNAPSE_DB_PASSWORD, db user synapse, path /synapse.
	urlSyn, err := r.dbURL("synapse", env)
	if err != nil {
		t.Fatalf("dbURL(synapse) error: %v", err)
	}
	wantSyn := "postgres://synapse:syn_db_password@dbhost:5432/synapse?sslmode=disable"
	if urlSyn != wantSyn {
		t.Errorf("dbURL(synapse) = %q, want %q", urlSyn, wantSyn)
	}

	// Missing password in env.
	if _, err := r.dbURL("authentik", map[string]string{}); err == nil {
		t.Error("expected error for missing AUTHENTIK_DB_PASSWORD")
	}
	if _, err := r.dbURL("synapse", map[string]string{}); err == nil {
		t.Error("expected error for missing SYNAPSE_DB_PASSWORD")
	}

	// Unknown database.
	if _, err := r.dbURL("unknown_db", env); err == nil {
		t.Error("expected error for unknown db")
	}

	// Malformed DatabaseURL.
	badRunner := &Runner{DatabaseURL: "://invalid-url"}
	if _, err := badRunner.dbURL("authentik", env); err == nil {
		t.Error("expected error for malformed DatabaseURL")
	}
}

// TestPackConfig tests packing configuration files into plain and secret tar.gz archives.
func TestPackConfig(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}

	// Regular plain files.
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("yaml: config"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "settings.json"), []byte(`{"key": "val"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Secret files: .env and a private key with 0600 permissions.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "private.key"), []byte("private key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Add a symlink (must be skipped).
	_ = os.Symlink(filepath.Join(dir, "config.yaml"), filepath.Join(dir, "link.yaml"))

	readTar := func(t *testing.T, data []byte) map[string]string {
		t.Helper()
		if len(data) == 0 {
			return nil
		}
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		tr := tar.NewReader(zr)
		files := make(map[string]string)
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatalf("tar next: %v", err)
			}
			b, _ := io.ReadAll(tr)
			files[hdr.Name] = string(b)
		}
		return files
	}

	// Pack with secrets = false: secrets must not appear anywhere.
	plainNoSec, secNoSec, err := packConfig(dir, false)
	if err != nil {
		t.Fatalf("packConfig(secrets=false): %v", err)
	}
	plainFiles := readTar(t, plainNoSec)
	if _, ok := plainFiles["config.yaml"]; !ok {
		t.Error("expected config.yaml in plain tar")
	}
	if _, ok := plainFiles["sub/settings.json"]; !ok {
		t.Error("expected sub/settings.json in plain tar")
	}
	if _, ok := plainFiles[".env"]; ok {
		t.Error(".env should not be in plain tar when secrets=false")
	}
	if _, ok := plainFiles["sub/private.key"]; ok {
		t.Error("private.key should not be in plain tar when secrets=false")
	}
	if _, ok := plainFiles["link.yaml"]; ok {
		t.Error("symlink should be skipped")
	}
	if len(secNoSec) != 0 {
		// Tar with no files might be an empty tar (header/footer) or empty bytes.
		// Check that no secret files are inside.
		secFiles := readTar(t, secNoSec)
		if len(secFiles) != 0 {
			t.Errorf("expected no files in secret tar when secrets=false, got %v", secFiles)
		}
	}

	// Pack with secrets = true: secrets must be in secret tar.
	plainSec, secSec, err := packConfig(dir, true)
	if err != nil {
		t.Fatalf("packConfig(secrets=true): %v", err)
	}
	plainFiles2 := readTar(t, plainSec)
	if _, ok := plainFiles2[".env"]; ok {
		t.Error(".env should not be in plain tar")
	}
	if _, ok := plainFiles2["sub/private.key"]; ok {
		t.Error("private.key should not be in plain tar")
	}

	secFiles2 := readTar(t, secSec)
	if _, ok := secFiles2[".env"]; !ok {
		t.Error("expected .env in secret tar")
	}
	if _, ok := secFiles2["sub/private.key"]; !ok {
		t.Error("expected sub/private.key in secret tar")
	}
	if _, ok := secFiles2["config.yaml"]; ok {
		t.Error("plain config.yaml should not be in secret tar")
	}

	// Non-existent directory returns error.
	if _, _, err := packConfig(filepath.Join(dir, "nonexistent"), true); err == nil {
		t.Error("expected error for non-existent directory")
	}
}

// TestRunnerMakeAndCollisions tests archive creation, collision resolution (-2, -3),
// secret encryption, manifest writing, and error cleanup.
func TestRunnerMakeAndCollisions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	configDir := t.TempDir()

	// Write dummy config files.
	if err := os.WriteFile(filepath.Join(configDir, "go24.conf"), []byte("domain = localhost"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, ".env"), []byte("SECRET_KEY=12345678"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewStore(nil, "runner-secret")
	sealedPass, err := store.seal("backup-password-123")
	if err != nil {
		t.Fatal(err)
	}

	r := &Runner{
		Root:        root,
		ConfigDir:   configDir,
		Version:     "2.4.0",
		Store:       store,
		DatabaseURL: "postgres://user:pass@host/go24",
		Dump: func(ctx context.Context, dbURL, file string) error {
			return os.WriteFile(file, []byte("pg_dump data for "+filepath.Base(file)), 0o600)
		},
	}

	st := Settings{
		Dir:    "daily",
		Parts:  []string{"go24", "config"},
		Secret: sealedPass,
	}

	at := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	// First make: creates daily/go24-20261028-120000.
	name1, parts1, size1, err := r.make(ctx, st, at)
	if err != nil {
		t.Fatalf("r.make #1 failed: %v", err)
	}
	wantName1 := "daily/go24-20261028-120000"
	if name1 != wantName1 {
		t.Errorf("make #1 name = %q, want %q", name1, wantName1)
	}
	if size1 <= 0 {
		t.Errorf("make #1 size = %d, want > 0", size1)
	}
	if len(parts1) == 0 {
		t.Errorf("make #1 parts empty")
	}

	dir1 := filepath.Join(root, filepath.FromSlash(name1))
	// Verify created files: manifest.json, go24.dump, config.tar.gz, secrets.tar.gz.enc.
	for _, expectedFile := range []string{"manifest.json", "go24.dump", "config.tar.gz", "secrets.tar.gz.enc"} {
		if _, err := os.Stat(filepath.Join(dir1, expectedFile)); err != nil {
			t.Errorf("expected %s in copy folder: %v", expectedFile, err)
		}
	}

	// Verify manifest contents.
	manifestBytes, err := os.ReadFile(filepath.Join(dir1, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(manifestBytes, &m); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if m.Version != "2.4.0" || m.Format != 1 || !m.Secrets {
		t.Errorf("unexpected manifest content: %+v", m)
	}

	// Second make with the EXACT same timestamp 'at': collision -> daily/go24-20261028-120000-2.
	name2, _, _, err := r.make(ctx, st, at)
	if err != nil {
		t.Fatalf("r.make #2 (collision) failed: %v", err)
	}
	wantName2 := "daily/go24-20261028-120000-2"
	if name2 != wantName2 {
		t.Errorf("make #2 name = %q, want %q", name2, wantName2)
	}

	// Third make with the EXACT same timestamp 'at': collision -> daily/go24-20261028-120000-3.
	name3, _, _, err := r.make(ctx, st, at)
	if err != nil {
		t.Fatalf("r.make #3 (collision) failed: %v", err)
	}
	wantName3 := "daily/go24-20261028-120000-3"
	if name3 != wantName3 {
		t.Errorf("make #3 name = %q, want %q", name3, wantName3)
	}

	// Error path: Dump fails -> must return error and remove .partial folder.
	failingRunner := &Runner{
		Root:        root,
		ConfigDir:   configDir,
		Version:     "2.4.0",
		Store:       store,
		DatabaseURL: "postgres://user:pass@host/go24",
		Dump: func(ctx context.Context, dbURL, file string) error {
			return errors.New("simulated pg_dump failure")
		},
	}
	failAt := time.Date(2026, 10, 28, 13, 0, 0, 0, time.UTC)
	_, _, _, err = failingRunner.make(ctx, Settings{Dir: "daily", Parts: []string{"go24"}}, failAt)
	if err == nil {
		t.Error("expected error from make when Dump fails")
	}
	partialPath := filepath.Join(root, "daily", "go24-20261028-130000.partial")
	if _, err := os.Stat(partialPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected .partial folder to be cleaned up on error, found: %v", err)
	}

	// Error path: ConfigDir is empty when config part is selected.
	noConfigRunner := &Runner{
		Root:      root,
		ConfigDir: "",
		Store:     store,
	}
	_, _, _, err = noConfigRunner.make(ctx, Settings{Dir: "daily", Parts: []string{"config"}}, failAt)
	if err == nil {
		t.Error("expected error when ConfigDir is empty")
	}
}

// TestRunnerPruneNoDB tests that prune returns nil when Keep <= 0 without touching any database.
func TestRunnerPruneNoDB(t *testing.T) {
	ctx := context.Background()
	r := &Runner{}

	if err := r.prune(ctx, Settings{Keep: 0}); err != nil {
		t.Errorf("prune(Keep=0) error: %v", err)
	}
	if err := r.prune(ctx, Settings{Keep: -1}); err != nil {
		t.Errorf("prune(Keep=-1) error: %v", err)
	}
}
