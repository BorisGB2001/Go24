package storages

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/storage"
)

// TestRcloneEndToEnd connects a WebDAV storage through a real rclone rcd and moves a file through it.
// Needs GO24_TEST_DATABASE_URL, GO24_TEST_RCLONE_RC_URL/_PASS and GO24_TEST_WEBDAV_URL (see `make test-storages`).
func TestRcloneEndToEnd(t *testing.T) {
	dbURL, rcURL, davURL := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_RCLONE_RC_URL"), os.Getenv("GO24_TEST_WEBDAV_URL")
	if dbURL == "" || rcURL == "" || davURL == "" {
		t.Skip("needs GO24_TEST_DATABASE_URL, GO24_TEST_RCLONE_RC_URL, GO24_TEST_WEBDAV_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var uid, other string
	pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('a', 'A') RETURNING id`).Scan(&uid)
	pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('b', 'B') RETURNING id`).Scan(&other)

	s := NewStore(pool, storage.NewRC(rcURL, "go24", os.Getenv("GO24_TEST_RCLONE_RC_PASS")), nil)
	in := Input{Name: "Test WebDAV", Type: "webdav", URL: davURL, Vendor: "other", User: "t", Password: "tp", Root: "go24test"}

	// an ordinary user may not point a storage at the internal network
	if _, err := s.Create(ctx, uid, false, in); key(err) != "err.storage_private_host" {
		t.Fatalf("internal host for a user: %v", err)
	}
	// wrong password: rclone check fails and the remote is not kept
	bad := in
	bad.Password = "wrong"
	if _, err := s.Create(ctx, uid, true, bad); key(err) != "err.storage_check" {
		t.Fatalf("wrong password: %v", err)
	}

	id, err := s.Create(ctx, uid, true, in) // admin may use internal hosts
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(ctx, other, false); len(list) != 0 {
		t.Fatalf("another user sees a personal storage: %v", list)
	}
	if err := s.Check(ctx, uid, false, id); err != nil {
		t.Fatal(err)
	}

	content := []byte("привет, go24 через rclone\n")
	fileKey := "go24/2026/10/abc/test file.txt"
	if err := s.Put(ctx, uid, false, id, fileKey, bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, other, false, id, "x/y.txt", strings.NewReader("x"), 1, "text/plain"); key(err) != "err.forbidden" {
		t.Fatalf("other user writes into a personal storage: %v", err)
	}
	if _, err := s.AddFile(ctx, NewFile{Key: fileKey, Name: "test file.txt", ContentType: "text/plain", Size: int64(len(content)), StorageID: id, UploadedBy: uid}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(ctx, &id, fileKey)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("read back %q", got)
	}
	if err := s.Delete(ctx, uid, false, id); key(err) != "err.storage_in_use" {
		t.Fatalf("delete with files: %v", err)
	}
	pool.Exec(ctx, `DELETE FROM files`)
	if err := s.Delete(ctx, uid, false, id); err != nil {
		t.Fatal(err)
	}
}

// TestRcloneSMB connects a Windows/NAS share by SMB (the path pasted as Windows writes it) through a real rclone rcd
// and moves a file through it. Needs GO24_TEST_DATABASE_URL, GO24_TEST_RCLONE_RC_URL/_PASS and GO24_TEST_SMB_HOST — a
// Samba server with the share «Общая» of tuser/tpass.
func TestRcloneSMB(t *testing.T) {
	dbURL, rcURL, smbHost := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_RCLONE_RC_URL"), os.Getenv("GO24_TEST_SMB_HOST")
	if dbURL == "" || rcURL == "" || smbHost == "" {
		t.Skip("needs GO24_TEST_DATABASE_URL, GO24_TEST_RCLONE_RC_URL, GO24_TEST_SMB_HOST")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var uid string
	pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('a', 'A') RETURNING id`).Scan(&uid)
	s := NewStore(pool, storage.NewRC(rcURL, "go24", os.Getenv("GO24_TEST_RCLONE_RC_PASS")), nil)
	in := Input{Name: "NAS", Type: "smb", Host: `\\` + smbHost + `\Общая\go24`, User: "tuser", Password: "wrong"}
	if _, err := s.Create(ctx, uid, true, in); key(err) != "err.storage_check" {
		t.Fatalf("wrong password: %v", err)
	}
	in.Password = "tpass"
	id, err := s.Create(ctx, uid, true, in)
	if err != nil {
		t.Fatal(err)
	}
	var typ, root string
	pool.QueryRow(ctx, `SELECT type, root FROM storages WHERE id = $1`, id).Scan(&typ, &root)
	if typ != "smb" || root != "Общая/go24" {
		t.Fatalf("storage %q %q", typ, root)
	}
	content := []byte("файл на сетевой папке\n")
	if err := s.Put(ctx, uid, false, id, "go24/2026/10/f.txt", bytes.NewReader(content), int64(len(content)), "text/plain"); err != nil {
		t.Fatal(err)
	}
	r, err := s.Open(ctx, &id, "go24/2026/10/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, content) {
		t.Fatalf("read back %q", got)
	}
	if err := s.Delete(ctx, uid, false, id); err != nil {
		t.Fatal(err)
	}
}
