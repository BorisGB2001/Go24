package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRestore: restoring from a copy — asked only for what the copy holds, with its password for the secrets; a
// wrong password changes nothing; then a safety copy first, the databases loaded (Authentik before go24), the
// journal and the backups' settings of now kept over go24's restored database, the files put back to the bucket,
// the configuration with its secrets unpacked into a folder of its own, the administrators told.
func TestRestore(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@go24.test', true) RETURNING id`).Scan(&anna))

	conf, root := t.TempDir(), t.TempDir()
	must(os.WriteFile(filepath.Join(conf, ".env"), []byte("AUTHENTIK_DB_PASSWORD=ak\n"), 0o600))
	must(os.WriteFile(filepath.Join(conf, "docker-compose.yml"), []byte("services: {}\n"), 0o644))
	store := NewStore(pool, strings.Repeat("k", 32))
	objs := &fakeObjects{m: map[string]fakeObject{"disk/a.txt": {"текст", time.Unix(1700000000, 0)}}}
	var restored []string
	migrated := 0
	r := &Runner{Store: store, Root: root, ConfigDir: conf, DatabaseURL: "postgres://go24:g@postgres:5432/go24?sslmode=disable",
		AdminGroups: []string{"go24-admins"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Objects: objs,
		Dump: func(_ context.Context, u, file string) error { return os.WriteFile(file, []byte("PGDMP "+u), 0o600) },
		Restore: func(ctx context.Context, u, file string) error {
			b, err := os.ReadFile(file)
			if err != nil || string(b) != "PGDMP "+u {
				t.Errorf("restore %s from %s: %q, %v", u, file, b, err)
			}
			restored = append(restored, u)
			if strings.Contains(u, "/go24?") { // go24's database as it was: no journal, its old settings, no Анна
				_, err = pool.Exec(ctx, `DELETE FROM backup_runs; DELETE FROM users WHERE oidc_sub = 'anna';
					UPDATE company_settings SET value = '{"parts": ["go24"]}' WHERE key = 'backup'`)
			}
			return err
		},
		Migrate: func(context.Context) error { migrated++; return nil }}

	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24", "authentik", "files", "config"}, Keep: 0}, Secrets{Password: "пароль копий"}))
	copyID, err := store.Queue(ctx, anna)
	must(err)
	must(r.Next(ctx))
	c, err := store.Copy(ctx, copyID)
	must(err)
	if c.Status != "done" || !slices.Equal(c.Parts, []string{"go24", "authentik", "files", "config", "secrets"}) {
		t.Fatalf("copy = %+v", c)
	}
	objs.m["disk/a.txt"] = fakeObject{"испорчено", time.Now()}

	if _, err := store.QueueRestore(ctx, boss, copyID+100, []string{"go24"}, "", false); err == nil {
		t.Error("restore from no copy queued")
	}
	if _, err := store.QueueRestore(ctx, boss, copyID, []string{"media", "synapse"}, "", false); err == nil {
		t.Error("restore of what the copy does not hold queued")
	}
	if _, err := store.QueueRestore(ctx, boss, copyID, []string{"config"}, "", false); err == nil {
		t.Error("the secrets without the password queued")
	}
	// a wrong password: nothing is done
	bad, err := store.QueueRestore(ctx, boss, copyID, []string{"go24", "authentik", "files", "config", "media"}, "неверный", true)
	must(err)
	if _, err := store.Queue(ctx, boss); err == nil {
		t.Error("a copy queued while restoring")
	}
	must(r.Next(ctx))
	runs, err := store.Runs(ctx, 10)
	must(err)
	if len(runs) != 2 || runs[0].ID != bad || runs[0].Kind != "restore" || runs[0].Status != "failed" || len(restored) != 0 ||
		!slices.Equal(runs[0].Parts, []string{"go24", "authentik", "files", "config"}) {
		t.Fatalf("wrong password: %v, %+v", restored, runs)
	}
	var secrets int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM backup_runs WHERE secret <> ''`).Scan(&secrets))
	if secrets != 0 {
		t.Error("the copy's password is kept")
	}

	id, err := store.QueueRestore(ctx, boss, copyID, []string{"go24", "authentik", "files", "config"}, "пароль копий", true)
	must(err)
	must(r.Next(ctx))
	if len(restored) != 2 || !strings.Contains(restored[0], "/authentik?") || !strings.Contains(restored[1], "/go24?") || migrated != 1 {
		t.Errorf("restored %v, migrated %d", restored, migrated)
	}
	runs, err = store.Runs(ctx, 10)
	must(err)
	// newest first: the safety copy (made while restoring), the restoring, the failed one, the copy (Анна is gone)
	if len(runs) != 4 || runs[0].Kind != "backup" || runs[0].Status != "done" || runs[0].By != "Борис" ||
		runs[1].ID != id || runs[1].Status != "done" || runs[1].Error != "" || !strings.HasPrefix(runs[1].Target, "restore/go24-") ||
		runs[3].ID != copyID || runs[3].By != "" {
		t.Fatalf("journal after restoring: %+v", runs)
	}
	st, err := store.Settings(ctx)
	must(err)
	if !st.Has("files") || !st.HasPassword() {
		t.Errorf("the backups' settings are the copy's: %+v", st)
	}
	next, err := store.Queue(ctx, boss)
	must(err)
	if next <= id {
		t.Errorf("the journal's numbers go back: %d after %d", next, id)
	}
	if string(objs.m["disk/a.txt"].data) != "текст" {
		t.Errorf("file not restored: %q", objs.m["disk/a.txt"].data)
	}
	dir := filepath.Join(root, filepath.FromSlash(runs[1].Target))
	for rel, want := range map[string]string{"docker-compose.yml": "services: {}\n", ".env": "AUTHENTIK_DB_PASSWORD=ak\n"} {
		if b, err := os.ReadFile(filepath.Join(dir, rel)); err != nil || string(b) != want {
			t.Errorf("unpacked %s: %q, %v", rel, b, err)
		}
	}
	var told []string
	rows, err := pool.Query(ctx, `SELECT kind || ':' || body FROM notifications WHERE user_id = $1 ORDER BY id`, boss)
	must(err)
	for rows.Next() {
		var s string
		must(rows.Scan(&s))
		told = append(told, s)
	}
	if len(told) != 2 || !strings.HasPrefix(told[0], "backup_restore_failed:") || told[1] != "backup_restored:"+filepath.Base(c.Name) {
		t.Errorf("told %v", told)
	}
}

// TestPgRestore: the real pg_dump, pg_restore and psql (skipped without them) — a dump loaded replaces the database
// (a table made since the copy is gone), a broken dump changes nothing.
func TestPgRestore(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if _, err := exec.LookPath("pg_restore"); err != nil || dbURL == "" {
		t.Skip("no pg_restore or GO24_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec1 := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	exec1(`DROP SCHEMA public CASCADE; CREATE SCHEMA public; CREATE TABLE kept (x int); INSERT INTO kept VALUES (1)`)
	r := &Runner{}
	file := filepath.Join(t.TempDir(), "go24.dump")
	if err := r.dump(ctx, dbURL, file); err != nil {
		t.Fatal(err)
	}
	exec1(`INSERT INTO kept VALUES (2); CREATE TABLE junk (y int); CREATE SCHEMA extra; CREATE TABLE extra.t (z int)`)
	if err := r.pgRestore(ctx, dbURL, file); err != nil {
		t.Fatal(err)
	}
	pool.Reset()
	if n := count(`SELECT count(*) FROM kept`); n != 1 {
		t.Errorf("kept: %d rows", n)
	}
	if n := count(`SELECT count(*) FROM pg_class WHERE relname IN ('junk', 't') AND relkind = 'r'`); n != 0 {
		t.Error("tables made since the copy stay")
	}
	exec1(`INSERT INTO kept VALUES (3)`)
	broken := filepath.Join(t.TempDir(), "broken.dump")
	if err := os.WriteFile(broken, []byte("PGDMP broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.pgRestore(ctx, dbURL, broken); err == nil || !strings.Contains(err.Error(), "pg_restore") {
		t.Errorf("broken dump: %v", err)
	}
	pool.Reset()
	if n := count(`SELECT count(*) FROM kept`); n != 2 {
		t.Errorf("after a broken dump kept has %d rows", n)
	}
}
