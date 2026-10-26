package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestEncrypt: the secrets open with the password they were sealed with only.
func TestEncrypt(t *testing.T) {
	enc, err := Encrypt("пароль копий", []byte("GO24_DB_PASSWORD=x"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("GO24_DB_PASSWORD")) {
		t.Fatal("not encrypted")
	}
	if plain, err := Decrypt("пароль копий", enc); err != nil || string(plain) != "GO24_DB_PASSWORD=x" {
		t.Fatalf("decrypt: %q, %v", plain, err)
	}
	if _, err := Decrypt("другой пароль", enc); err == nil {
		t.Fatal("a wrong password opened it")
	}
	for in, want := range map[string]string{"": "", " /копии/ночь/ ": "копии/ночь", `a\b`: "a/b", "a/../b": "b"} {
		if got, err := CleanDir(in); err != nil || got != want {
			t.Errorf("CleanDir(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"..", "../etc", "a/../../b"} {
		if _, err := CleanDir(bad); err == nil {
			t.Errorf("CleanDir(%q): no error", bad)
		}
	}
}

type fakeObject struct {
	data string
	mod  time.Time
}

type fakeObjects struct{ m map[string]fakeObject }

func (f *fakeObjects) Walk(_ context.Context, fn func(string, int64, time.Time) error) error {
	keys := make([]string, 0, len(f.m))
	for k := range f.m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if err := fn(k, int64(len(f.m[k].data)), f.m[k].mod); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeObjects) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.m[key].data)), nil
}

func (f *fakeObjects) Put(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	if err == nil {
		f.m[key] = fakeObject{string(b), time.Now()}
	}
	return err
}

func manifestSize(t *testing.T, root, name string) int64 {
	t.Helper()
	fi, err := os.Stat(filepath.Join(root, name, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}

// TestSlot: the time a scheduled copy is due — daily at its time, weekly on its day; the next one after it.
func TestSlot(t *testing.T) {
	loc := time.FixedZone("AMS", 3600)
	at := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	day := Settings{Schedule: "daily", At: "03:30"}
	week := Settings{Schedule: "weekly", At: "23:00", Weekday: 7} // Sunday
	for _, c := range []struct {
		st        Settings
		now, slot string
		next      string
	}{
		{day, "2026-10-26 10:00", "2026-10-26 03:30", "2026-10-27 03:30"},
		{day, "2026-10-26 03:29", "2026-10-25 03:30", "2026-10-26 03:30"},
		{day, "2026-10-26 03:30", "2026-10-26 03:30", "2026-10-27 03:30"},
		{week, "2026-10-26 10:00", "2026-10-25 23:00", "2026-11-01 23:00"}, // Monday
		{week, "2026-10-25 22:59", "2026-10-18 23:00", "2026-10-25 23:00"},
		{Settings{Schedule: "weekly", At: "01:00", Weekday: 1}, "2026-10-26 02:00", "2026-10-26 01:00", "2026-11-02 01:00"},
	} {
		if got := c.st.Slot(at(c.now)); !got.Equal(at(c.slot)) {
			t.Errorf("%+v at %s: slot %v, want %s", c.st, c.now, got, c.slot)
		}
		if got := c.st.Next(at(c.now)); !got.Equal(at(c.next)) {
			t.Errorf("%+v at %s: next %v, want %s", c.st, c.now, got, c.next)
		}
	}
	if !(Settings{}).Slot(time.Now()).IsZero() || !(Settings{}).Next(time.Now()).IsZero() {
		t.Error("a slot without a schedule")
	}
}

func untar(t *testing.T, data []byte) map[string]string {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(z)
	files := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		files[h.Name] = string(b)
	}
}

// TestRun: «Сделать сейчас» queues one copy (not a second while it waits); the worker makes it into the chosen
// folder — the dumps of the chosen databases (Authentik's with the password of .env), the configuration without the
// secrets and the secrets encrypted with the backups' password, manifest.json; the journal tells the folder, the
// parts and the size. A failing dump leaves no folder and the journal tells the error.
func TestRun(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	conf, root := t.TempDir(), t.TempDir()
	must(os.WriteFile(filepath.Join(conf, ".env"), []byte("# go24\nAUTHENTIK_DB_PASSWORD='ak secret'\nGO24_DB_PASSWORD=g\n"), 0o600))
	must(os.WriteFile(filepath.Join(conf, "docker-compose.yml"), []byte("services: {}\n"), 0o644))
	must(os.MkdirAll(filepath.Join(conf, "matrix"), 0o755))
	must(os.WriteFile(filepath.Join(conf, "matrix", "homeserver.yaml"), []byte("macaroon: s\n"), 0o600))
	must(os.WriteFile(filepath.Join(conf, "matrix", "element-config.json"), []byte("{}"), 0o644))

	store := NewStore(pool, strings.Repeat("k", 32))
	var dumped []string
	fail := false
	r := &Runner{Store: store, Root: root, ConfigDir: conf, DatabaseURL: "postgres://go24:g@postgres:5432/go24?sslmode=disable",
		Version: "0.5.0", AdminGroups: []string{"go24-admins"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dump: func(_ context.Context, u, file string) error {
			if fail {
				return errors.New("connection refused")
			}
			dumped = append(dumped, u)
			return os.WriteFile(file, []byte("PGDMP "+u), 0o600)
		}}

	st, err := store.Settings(ctx)
	must(err)
	if !slices.Equal(st.Parts, []string{"go24", "config"}) || st.HasPassword() || st.Keep != DefaultKeep || st.Schedule != "" {
		t.Errorf("default settings = %+v", st)
	}
	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"config"}}, Secrets{Password: "short"}); err == nil {
		t.Error("a short password kept")
	}
	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"nothing"}}, Secrets{}); err == nil {
		t.Error("nothing to copy kept")
	}
	must(store.SaveSettings(ctx, boss, Settings{Dir: "ночные/", Parts: []string{"config", "authentik", "go24"}}, Secrets{Password: "пароль копий"}))
	st, err = store.Settings(ctx)
	must(err)
	if st.Dir != "ночные" || !slices.Equal(st.Parts, []string{"go24", "authentik", "config"}) || strings.Contains(st.Secret, "пароль") {
		t.Fatalf("settings = %+v", st)
	}
	var raw string
	must(pool.QueryRow(ctx, `SELECT value::text FROM company_settings WHERE key = 'backup'`).Scan(&raw))
	if strings.Contains(raw, "пароль копий") {
		t.Fatal("the password is kept open")
	}

	id, err := store.Queue(ctx, boss)
	must(err)
	if again, err := store.Queue(ctx, boss); err == nil || again != id {
		t.Errorf("a second copy queued: %d, %v", again, err)
	}
	must(r.Next(ctx))
	must(r.Next(ctx)) // nothing more
	runs, err := store.Runs(ctx, 10)
	must(err)
	if len(runs) != 1 || runs[0].Status != "done" || runs[0].By != "Борис" || !strings.HasPrefix(runs[0].Name, "ночные/go24-") ||
		!slices.Equal(runs[0].Parts, []string{"go24", "authentik", "config", "secrets"}) || runs[0].Size == 0 || runs[0].FinishedAt == nil {
		t.Fatalf("runs = %+v", runs)
	}
	if len(dumped) != 2 || dumped[0] != r.DatabaseURL || dumped[1] != "postgres://authentik:ak%20secret@postgres:5432/authentik?sslmode=disable" {
		t.Errorf("dumped = %v", dumped)
	}
	dir := filepath.Join(root, filepath.FromSlash(runs[0].Name))
	var m Manifest
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	must(err)
	must(json.Unmarshal(b, &m))
	if !m.Secrets || m.Version != "0.5.0" || !slices.Equal(m.Files, []string{"go24.dump", "authentik.dump", "config.tar.gz", "secrets.tar.gz.enc"}) {
		t.Errorf("manifest = %+v", m)
	}
	b, err = os.ReadFile(filepath.Join(dir, "config.tar.gz"))
	must(err)
	plain := untar(t, b)
	if plain["docker-compose.yml"] != "services: {}\n" || plain["matrix/element-config.json"] != "{}" {
		t.Errorf("config = %v", plain)
	}
	if _, ok := plain[".env"]; ok {
		t.Error(".env in the open part")
	}
	if _, ok := plain["matrix/homeserver.yaml"]; ok {
		t.Error("an owner-only file in the open part")
	}
	b, err = os.ReadFile(filepath.Join(dir, "secrets.tar.gz.enc"))
	must(err)
	b, err = Decrypt("пароль копий", b)
	must(err)
	if sec := untar(t, b); !strings.Contains(sec[".env"], "AUTHENTIK_DB_PASSWORD") || sec["matrix/homeserver.yaml"] != "macaroon: s\n" || len(sec) != 2 {
		t.Errorf("secrets = %v", sec)
	}

	// no password: the secrets are not copied at all
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"config"}}, Secrets{Clear: true}))
	_, err = store.Queue(ctx, "")
	must(err)
	must(r.Next(ctx))
	runs, err = store.Runs(ctx, 10)
	must(err)
	if runs[0].Status != "done" || !slices.Equal(runs[0].Parts, []string{"config"}) || runs[0].By != "" || strings.Contains(runs[0].Name, "/") {
		t.Errorf("no password: %+v", runs[0])
	}
	if _, err := os.Stat(filepath.Join(root, runs[0].Name, "secrets.tar.gz.enc")); err == nil {
		t.Error("secrets without a password")
	}

	// a dump failing: no folder left, the error in the journal
	must(store.SaveSettings(ctx, boss, Settings{Dir: "x", Parts: []string{"go24"}}, Secrets{}))
	fail = true
	_, err = store.Queue(ctx, boss)
	must(err)
	must(r.Next(ctx))
	runs, err = store.Runs(ctx, 10)
	must(err)
	if runs[0].Status != "failed" || !strings.Contains(runs[0].Error, "connection refused") || runs[0].Name != "" {
		t.Errorf("failed: %+v", runs[0])
	}
	if left, _ := os.ReadDir(filepath.Join(root, "x")); len(left) != 0 {
		t.Errorf("left: %v", left)
	}
	// the administrators (not Anna) are told
	var told []string
	rows, err := pool.Query(ctx, `SELECT user_id::text || ' ' || body || ' ' || url FROM notifications WHERE kind = 'backup_failed'`)
	must(err)
	for rows.Next() {
		var s string
		must(rows.Scan(&s))
		told = append(told, s)
	}
	if len(told) != 1 || !strings.HasPrefix(told[0], boss+" go24: connection refused /settings/backup") {
		t.Errorf("told = %v", told)
	}
	fail = false

	// the files of go24 and the media of Matrix: the second copy writes anew only what changed, the rest are hard
	// links to the first one
	objs := &fakeObjects{m: map[string]fakeObject{"disk/a.txt": {"текст", time.Unix(1700000000, 0)}, "mail/b.bin": {"bb", time.Unix(1700000001, 0)},
		"../evil": {"x", time.Unix(1700000002, 0)}, "dir/": {"", time.Unix(1700000003, 0)}}}
	r.Objects, r.MatrixDir = objs, t.TempDir()
	must(os.MkdirAll(filepath.Join(r.MatrixDir, "media_store", "local_content", "ab"), 0o755))
	must(os.WriteFile(filepath.Join(r.MatrixDir, "media_store", "local_content", "ab", "cd"), []byte("картинка"), 0o644))
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"files", "media"}, Keep: 0}, Secrets{}))
	stats := func() (Run, Manifest) {
		t.Helper()
		_, err := store.Queue(ctx, boss)
		must(err)
		must(r.Next(ctx))
		runs, err := store.Runs(ctx, 1)
		must(err)
		var m Manifest
		if runs[0].Status == "done" {
			b, err := os.ReadFile(filepath.Join(root, runs[0].Name, "manifest.json"))
			must(err)
			must(json.Unmarshal(b, &m))
		}
		return runs[0], m
	}
	first, m1 := stats()
	if first.Status != "done" || !slices.Equal(first.Parts, []string{"files", "media"}) || m1.Trees["files"].Files != 3 || m1.Trees["files"].Fresh != 3 ||
		m1.Trees["media"].Fresh != 1 || first.Size != int64(len("текст")+2+1+len("картинка"))+manifestSize(t, root, first.Name) {
		t.Fatalf("first: %+v %+v", first, m1)
	}
	for rel, want := range map[string]string{"files/disk/a.txt": "текст", "files/mail/b.bin": "bb", "files/evil": "x", "media/local_content/ab/cd": "картинка"} {
		if b, err := os.ReadFile(filepath.Join(root, first.Name, filepath.FromSlash(rel))); err != nil || string(b) != want {
			t.Errorf("%s: %q, %v", rel, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "evil")); err == nil {
		t.Error("a key wrote outside the copy")
	}
	objs.m["mail/b.bin"] = fakeObject{"bbb", time.Unix(1700000100, 0)} // changed
	second, m2 := stats()
	if m2.Trees["files"].Files != 3 || m2.Trees["files"].Fresh != 1 || m2.Trees["files"].Bytes != 3 || m2.Trees["media"].Fresh != 0 {
		t.Errorf("second: %+v", m2)
	}
	a1, _ := os.Stat(filepath.Join(root, first.Name, "files", "disk", "a.txt"))
	a2, _ := os.Stat(filepath.Join(root, second.Name, "files", "disk", "a.txt"))
	if !os.SameFile(a1, a2) {
		t.Error("an unchanged file is not a hard link")
	}
	if b, _ := os.ReadFile(filepath.Join(root, second.Name, "files", "mail", "b.bin")); string(b) != "bbb" {
		t.Errorf("changed file: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, first.Name, "files", "mail", "b.bin")); string(b) != "bb" {
		t.Errorf("the first copy changed: %q", b)
	}
	r.Objects = nil
	if bad, _ := stats(); bad.Status != "failed" || !strings.Contains(bad.Error, "files: not reachable") {
		t.Errorf("no files: %+v", bad)
	}

	// the schedule: daily at 03:00 — a copy once for its time, not for the time before it was set; only the two
	// latest kept: the older folders removed, their lines stay
	must(store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Schedule: "daily", At: "03:00", Keep: 2}, Secrets{}))
	st, err = store.Settings(ctx)
	must(err)
	since := st.Since
	day := time.Date(since.Year(), since.Month(), since.Day(), 3, 0, 0, 0, since.Location())
	if !day.After(since) {
		day = day.AddDate(0, 0, 1)
	}
	if ok, err := store.queueDue(ctx, day.Add(-time.Minute)); err != nil || ok {
		t.Errorf("due before its time: %v, %v", ok, err)
	}
	if ok, err := store.queueDue(ctx, day.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("not due at its time: %v, %v", ok, err)
	}
	if ok, err := store.queueDue(ctx, day.Add(2*time.Minute)); err != nil || ok {
		t.Errorf("due twice: %v, %v", ok, err)
	}
	_, err = pool.Exec(ctx, `UPDATE backup_runs SET created_at = $1 WHERE auto`, day.Add(time.Minute))
	must(err)
	must(r.Next(ctx))
	if ok, err := store.queueDue(ctx, day.Add(23*time.Hour)); err != nil || ok {
		t.Errorf("due again the same day: %v, %v", ok, err)
	}
	runs, err = store.Runs(ctx, 10)
	must(err)
	if !runs[0].Auto || runs[0].Status != "done" || runs[0].By != "" {
		t.Errorf("scheduled run = %+v", runs[0])
	}
	var kept, removed int
	for _, run := range runs {
		if run.Status != "done" {
			continue
		}
		_, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(run.Name)))
		if run.Removed != nil {
			removed++
			if statErr == nil {
				t.Errorf("%s is still there", run.Name)
			}
		} else {
			kept++
			if statErr != nil {
				t.Errorf("%s: %v", run.Name, statErr)
			}
		}
	}
	if kept != 2 || removed != 3 {
		t.Errorf("kept %d, removed %d: %+v", kept, removed, runs)
	}
	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, At: "25:00"}, Secrets{}); err == nil {
		t.Error("a wrong time kept")
	}
	if err := store.SaveSettings(ctx, boss, Settings{Parts: []string{"go24"}, Keep: -1}, Secrets{}); err == nil {
		t.Error("a wrong number of copies kept")
	}
}
