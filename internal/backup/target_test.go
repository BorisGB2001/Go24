package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// fakeRC is rclone-rcd: what was asked of it.
type fakeRC struct {
	calls []string
	fail  error
}

func (f *fakeRC) CreateRemote(_ context.Context, name, typ string, p map[string]string) error {
	f.calls = append(f.calls, "create "+name+" "+typ+" "+p["endpoint"]+" "+p["access_key_id"]+" "+p["secret_access_key"]+p["pass"])
	return nil
}
func (f *fakeRC) DeleteRemote(_ context.Context, name string) error {
	f.calls = append(f.calls, "delete "+name)
	return nil
}
func (f *fakeRC) Mkdir(_ context.Context, remote, dir string) error {
	f.calls = append(f.calls, "mkdir "+remote+":"+dir)
	return nil
}
func (f *fakeRC) CopyDir(_ context.Context, src, dst string) error {
	f.calls = append(f.calls, "copy "+src+" "+dst)
	return f.fail
}
func (f *fakeRC) Purge(_ context.Context, fs, p string) error {
	f.calls = append(f.calls, "purge "+fs+p)
	return nil
}

// TestTarget: an outside place — the settings checked, its password kept sealed and kept when not given again (for
// the same type); a made copy goes there by rclone-rcd and the journal tells where; one that did not go is done
// with the reason and the administrators are told; the old copies are removed there too.
func TestTarget(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('boss', 'Борис', 'boss@go24.test', true) RETURNING id`).Scan(&boss))
	store := NewStore(pool, strings.Repeat("k", 32))
	rc := &fakeRC{}
	r := &Runner{Store: store, Root: t.TempDir(), RC: rc, RCRoot: "/backups", DatabaseURL: "postgres://go24@postgres/go24",
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dump: func(_ context.Context, _, file string) error { return os.WriteFile(file, []byte("PGDMP"), 0o600) }}

	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Target: Target{Type: "s3", Endpoint: "minio:9000"}}, Secrets{}); err == nil {
		t.Error("S3 without the key and the bucket kept")
	}
	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Target: Target{Type: "ftp", Host: "h"}}, Secrets{}); err == nil {
		t.Error("an unknown place kept")
	}
	if err := store.Check(ctx, rc); err == nil {
		t.Error("checked without a place")
	}
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Keep: 1,
		Target: Target{Type: "s3", Endpoint: "https://s3.example.com", User: "AKID", Path: "/go24-bk/ночные/"}}, Secrets{TargetPassword: "s3cret"}))
	st, err := store.Settings(ctx)
	must(err)
	if st.Target.Path != "go24-bk/ночные" || !st.Target.HasPassword() || strings.Contains(st.Target.Secret, "s3cret") {
		t.Fatalf("target = %+v", st.Target)
	}
	must(store.SaveSettings(ctx, boss, st, Secrets{})) // the password is not given again: kept
	must(store.Check(ctx, rc))
	if strings.Join(rc.calls, "|") != "delete go24-backup|create go24-backup s3 https://s3.example.com AKID s3cret|mkdir go24-backup:go24-bk/ночные" {
		t.Errorf("check calls = %q", rc.calls)
	}

	rc.calls = nil
	_, err = store.Queue(ctx, boss)
	must(err)
	must(r.Next(ctx))
	runs, err := store.Runs(ctx, 5)
	must(err)
	first := runs[0]
	if first.Status != "done" || first.Target != "s3 go24-bk/ночные/"+first.Name || first.UploadError != "" {
		t.Fatalf("run = %+v", first)
	}
	if last := rc.calls[len(rc.calls)-1]; last != "copy /backups/"+first.Name+" go24-backup:go24-bk/ночные/"+first.Name {
		t.Errorf("copy = %q", last)
	}

	// the place fails: the copy is in the folder, the reason in the journal, the administrator told
	rc.fail, rc.calls = errors.New("403 Forbidden"), nil
	_, err = store.Queue(ctx, "")
	must(err)
	must(r.Next(ctx))
	runs, err = store.Runs(ctx, 5)
	must(err)
	if runs[0].Status != "done" || runs[0].Target != "" || runs[0].UploadError != "403 Forbidden" {
		t.Errorf("failed upload = %+v", runs[0])
	}
	var told string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE kind = 'backup_failed' AND user_id = $1`, boss).Scan(&told))
	if told != "s3: 403 Forbidden" {
		t.Errorf("told %q", told)
	}
	// Keep 1: the first copy is removed from the folder and from the place
	if !strings.Contains(strings.Join(rc.calls, "|"), "purge go24-backup:go24-bk/ночные/"+first.Name) {
		t.Errorf("not purged: %q", rc.calls)
	}

	// another type: its password must be given anew; none — the folder only
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Target: Target{Type: "sftp", Host: "nas", User: "bk"}}, Secrets{}))
	if st, _ := store.Settings(ctx); st.Target.HasPassword() {
		t.Error("the S3 key kept for SFTP")
	}
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}}, Secrets{}))
	if st, _ := store.Settings(ctx); st.Target != (Target{}) {
		t.Errorf("no place: %+v", st.Target)
	}
}
