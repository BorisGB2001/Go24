package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Restorable are the parts of a copy put back from the page, in its order. The media of Matrix are not: Synapse's
// volume is not go24-worker's to write — the page tells how to put the copy's media/ back by hand.
var Restorable = []string{"go24", "authentik", "synapse", "files", "config"}

// Copy is a copy of the journal one can restore from: made, its folder kept.
func (s *Store) Copy(ctx context.Context, id int64) (Run, error) {
	rows, err := s.db.Query(ctx, `
		SELECT r.id, r.status, r.created_at, COALESCE(u.name, ''), r.started_at, r.finished_at, r.name, r.parts, r.size, r.error,
			r.auto, r.removed_at, r.target, r.target_path, r.upload_error, r.kind
		FROM backup_runs r LEFT JOIN users u ON u.id = r.created_by
		WHERE r.id = $1 AND r.kind = 'backup' AND r.status = 'done' AND r.removed_at IS NULL AND r.name <> ''`, id)
	if err != nil {
		return Run{}, err
	}
	run, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[Run])
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, i18n.E("err.not_found")
	}
	return run, err
}

// QueueRestore asks go24-worker to put back the parts of the copy id; password opens the copy's secrets (needed with
// "config" when the copy has them); safety — a copy of how things are is made first. Nothing else is done meanwhile.
func (s *Store) QueueRestore(ctx context.Context, by string, id int64, parts []string, password string, safety bool) (int64, error) {
	c, err := s.Copy(ctx, id)
	if err != nil {
		return 0, err
	}
	var want []string
	for _, p := range Restorable {
		if contains(parts, p) && contains(c.Parts, p) {
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		return 0, i18n.E("backup.err_restore_parts")
	}
	sealed := ""
	if contains(want, "config") && contains(c.Parts, "secrets") {
		if password == "" {
			return 0, i18n.E("backup.err_restore_password")
		}
		if sealed, err = s.seal(password); err != nil {
			return 0, err
		}
	}
	var busy int64
	err = s.db.QueryRow(ctx, `SELECT id FROM backup_runs WHERE status IN ('queued', 'running') ORDER BY id LIMIT 1`).Scan(&busy)
	if err == nil {
		return busy, i18n.E("backup.err_busy")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var rid int64
	err = s.db.QueryRow(ctx, `INSERT INTO backup_runs (kind, created_by, name, parts, source, secret, safety)
		VALUES ('restore', NULLIF($1, '')::uuid, $2, $3, $4, $5, $6) RETURNING id`, by, c.Name, want, id, sealed, safety).Scan(&rid)
	return rid, err
}

// restore puts back what the line j asks for and writes how it went; the administrators are told either way.
func (r *Runner) restore(ctx context.Context, j job) error {
	start := time.Now()
	where, size, err := r.putBack(ctx, j)
	ctx = context.WithoutCancel(ctx)
	if ferr := r.Store.finishUp(ctx, j.ID, j.Name, j.Parts, size, err, where, "", nil); ferr != nil {
		return ferr
	}
	if err != nil {
		r.Log.Error("restore failed", "id", j.ID, "name", j.Name, "err", err)
		return r.Store.tell(ctx, "backup_restore_failed", err.Error(), r.AdminGroups)
	}
	r.Log.Info("restored", "id", j.ID, "name", j.Name, "parts", j.Parts, "took", time.Since(start).Round(time.Second))
	return r.Store.tell(ctx, "backup_restored", path.Base(j.Name), r.AdminGroups)
}

// putBack checks everything first (the copy's files, the password of its secrets, the databases' users), makes the
// safety copy, then restores the databases, the files and unpacks the configuration; it gives where the
// configuration went (relative to the backups' folder) and how many bytes of files were written.
func (r *Runner) putBack(ctx context.Context, j job) (string, int64, error) {
	rel, err := CleanDir(j.Name)
	if err != nil || !strings.HasPrefix(path.Base(rel), "go24-") {
		return "", 0, fmt.Errorf("not a copy's folder: %q", j.Name)
	}
	dir := filepath.Join(r.Root, filepath.FromSlash(rel))
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", 0, fmt.Errorf("the copy: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", 0, fmt.Errorf("manifest.json: %w", err)
	}
	for _, p := range j.Parts {
		if !contains(m.Parts, p) {
			return "", 0, fmt.Errorf("%s: not in the copy", p)
		}
	}
	var secret []byte
	if contains(j.Parts, "config") && m.Secrets {
		password, err := r.Store.openSecret(j.Secret)
		if err != nil {
			return "", 0, err
		}
		if password == "" {
			return "", 0, i18n.E("backup.err_restore_password")
		}
		enc, err := os.ReadFile(filepath.Join(dir, "secrets.tar.gz.enc"))
		if err != nil {
			return "", 0, err
		}
		if secret, err = Decrypt(password, enc); err != nil {
			return "", 0, err
		}
	}
	env := readEnv(filepath.Join(r.ConfigDir, ".env")) // the passwords of now, not of the copy
	urls := map[string]string{}
	for _, db := range []string{"go24", "authentik", "synapse"} {
		if !contains(j.Parts, db) {
			continue
		}
		if urls[db], err = r.dbURL(db, env); err != nil {
			return "", 0, err
		}
		if _, err := os.Stat(filepath.Join(dir, db+".dump")); err != nil {
			return "", 0, fmt.Errorf("%s: %w", db, err)
		}
	}
	if j.Safety {
		if err := r.safetyCopy(ctx, j); err != nil {
			return "", 0, fmt.Errorf("the copy before restoring: %w", err)
		}
	}
	for _, db := range []string{"authentik", "synapse", "go24"} {
		if urls[db] == "" {
			continue
		}
		file := filepath.Join(dir, db+".dump")
		if db == "go24" {
			err = r.restoreGo24(ctx, urls[db], file)
		} else {
			err = r.pgRestore(ctx, urls[db], file)
		}
		if err != nil {
			return "", 0, fmt.Errorf("%s: %w", db, err)
		}
	}
	var size int64
	if contains(j.Parts, "files") {
		if r.Objects == nil {
			return "", 0, fmt.Errorf("files: not reachable from go24-worker")
		}
		if size, err = putObjects(ctx, r.Objects, filepath.Join(dir, "files")); err != nil {
			return "", size, fmt.Errorf("files: %w", err)
		}
	}
	where := ""
	if contains(j.Parts, "config") {
		where = "restore/" + path.Base(rel) + "-" + time.Now().Format("20060102-150405")
		if err := unpack(filepath.Join(r.Root, filepath.FromSlash(where)), filepath.Join(dir, "config.tar.gz"), secret); err != nil {
			return "", size, fmt.Errorf("config: %w", err)
		}
	}
	return where, size, nil
}

// safetyCopy makes a copy of how things are (the parts of the settings and the ones restored) as a line of its own.
func (r *Runner) safetyCopy(ctx context.Context, j job) error {
	st, err := r.Store.Settings(ctx)
	if err != nil {
		return err
	}
	for _, p := range j.Parts {
		if !st.Has(p) {
			st.Parts = append(st.Parts, p)
		}
	}
	var id int64
	if err := r.Store.db.QueryRow(ctx, `INSERT INTO backup_runs (status, started_at, created_by) VALUES ('running', now(), NULLIF($1, '')::uuid)
		RETURNING id`, j.By).Scan(&id); err != nil {
		return err
	}
	name, parts, size, err := r.make(ctx, st, time.Now())
	if ferr := r.Store.finish(context.WithoutCancel(ctx), id, name, parts, size, err); ferr != nil && err == nil {
		err = ferr
	}
	return err
}

// restoreGo24 restores go24's database keeping the journal of the copies and the backups' settings as they are now
// (the copy's are older), then brings its tables up to this version (the migrations).
func (r *Runner) restoreGo24(ctx context.Context, dbURL, file string) error {
	var runs, set []byte
	if err := r.Store.db.QueryRow(ctx, `SELECT COALESCE(json_agg(r), '[]') FROM backup_runs r`).Scan(&runs); err != nil {
		return err
	}
	if err := r.Store.db.QueryRow(ctx, `SELECT COALESCE((SELECT to_json(c) FROM company_settings c WHERE key = $1), 'null')`,
		settingKey).Scan(&set); err != nil {
		return err
	}
	if err := r.pgRestore(ctx, dbURL, file); err != nil {
		return err
	}
	r.Store.db.Reset() // the tables are new: no statement prepared for the old ones
	if r.Migrate != nil {
		if err := r.Migrate(ctx); err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
	}
	return pgx.BeginFunc(ctx, r.Store.db, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`CREATE TEMP TABLE kept_runs ON COMMIT DROP AS SELECT * FROM json_populate_recordset(NULL::backup_runs, $1)`, []any{string(runs)}},
			{`UPDATE kept_runs SET created_by = NULL WHERE created_by NOT IN (SELECT id FROM users)`, nil},
			{`DELETE FROM backup_runs`, nil},
			{`INSERT INTO backup_runs SELECT * FROM kept_runs`, nil},
			{`SELECT setval(pg_get_serial_sequence('backup_runs', 'id'), GREATEST((SELECT max(id) FROM backup_runs), 1))`, nil},
			{`DELETE FROM company_settings WHERE key = $1`, []any{settingKey}},
			{`INSERT INTO company_settings (key, value, updated_at) SELECT key, value, updated_at
				FROM json_populate_record(NULL::company_settings, $1) WHERE key IS NOT NULL`, []any{string(set)}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return fmt.Errorf("the journal: %w", err)
			}
		}
		return nil
	})
}

// wipe drops the database's schemas before the dump is loaded (in the same transaction): tables made since the copy
// would stay and the migrations would fail on them.
const wipe = `DO $$DECLARE s name; BEGIN
FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' LOOP
	EXECUTE format('DROP SCHEMA %I CASCADE', s);
END LOOP; END $$;
CREATE SCHEMA public;
`

// abort ends psql's input when pg_restore failed: the transaction (the schemas dropped too) is rolled back.
const abort = "\nDO $$BEGIN RAISE EXCEPTION 'pg_restore failed'; END $$;\n"

// pgRestore replaces the database with the dump in one transaction: an error — the database is as it was.
func (r *Runner) pgRestore(ctx context.Context, dbURL, file string) error {
	if r.Restore != nil {
		return r.Restore(ctx, dbURL, file)
	}
	var derr, lerr bytes.Buffer
	load := exec.CommandContext(ctx, "psql", "--no-psqlrc", "--quiet", "--single-transaction", "--set=ON_ERROR_STOP=1", "--dbname="+dbURL)
	in, err := load.StdinPipe()
	if err != nil {
		return err
	}
	load.Stdout, load.Stderr = io.Discard, &lerr
	load.Env = append(os.Environ(), "PGOPTIONS=-c client_min_messages=warning") // not the notices of every table dropped
	if err := load.Start(); err != nil {
		return fmt.Errorf("psql: %w", err)
	}
	dump := exec.CommandContext(ctx, "pg_restore", "--no-owner", "--no-privileges", "--file=-", file)
	dump.Stdout, dump.Stderr = in, &derr
	_, dumpErr := io.WriteString(in, wipe)
	if dumpErr == nil {
		dumpErr = dump.Run()
	}
	if dumpErr != nil {
		_, _ = io.WriteString(in, abort)
	}
	_ = in.Close()
	loadErr := load.Wait()
	if dumpErr != nil {
		return fmt.Errorf("pg_restore: %v: %s", dumpErr, lastLines(derr.String(), 5))
	}
	if loadErr != nil {
		return fmt.Errorf("psql: %v: %s", loadErr, lastLines(lerr.String(), 5))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// putObjects writes the copy's files/ back to the bucket under the same keys (those there are replaced, others stay).
func putObjects(ctx context.Context, objs Objects, dir string) (int64, error) {
	var size int64
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		typ := mime.TypeByExtension(filepath.Ext(p))
		if typ == "" {
			typ = "application/octet-stream"
		}
		if err := objs.Put(ctx, filepath.ToSlash(rel), f, info.Size(), typ); err != nil {
			return fmt.Errorf("%s: %w", filepath.ToSlash(rel), err)
		}
		size += info.Size()
		return nil
	})
	return size, err
}

// unpack writes the copy's configuration (and its secrets, opened) into a folder of their own, for an administrator
// to put in place: the configuration go24 runs with is not touched.
func unpack(dst, plain string, secret []byte) error {
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	raw, err := os.ReadFile(plain)
	if err != nil {
		return err
	}
	for _, tgz := range [][]byte{raw, secret} {
		if len(tgz) == 0 {
			continue
		}
		if err := unpackTar(dst, tgz); err != nil {
			return err
		}
	}
	return nil
}

// unpackTar unpacks a tar.gz into dst: folders and plain files only, nothing outside dst.
func unpackTar(dst string, tgz []byte) error {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Clean("/" + hdr.Name)
		if name == "/" {
			continue
		}
		p := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fs.FileMode(hdr.Mode)&0o700|0o600)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		}
	}
}
