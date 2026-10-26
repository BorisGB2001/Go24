package backup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Runner makes the copies in go24-worker. Root is the backups' folder; ConfigDir — the snapshot of deploy/ (see
// backup-init in compose); DatabaseURL — go24's database (the others are reached with the passwords of .env);
// Dump makes a dump of a database into a file (nil — pg_dump, custom format); AdminGroups — the administrators'
// groups of Authentik, told of a failed copy with the administrators made in go24; Objects — the files of go24;
// MatrixDir — the data folder of Synapse (its media_store is copied); RC — rclone-rcd for the outside place, RCRoot —
// the backups' folder as rclone-rcd sees it. Restoring: Restore loads a dump into a database (nil — pg_restore into
// psql), Migrate brings go24's restored database up to this version.
type Runner struct {
	RC          Remote
	RCRoot      string
	Store       *Store
	Root        string
	ConfigDir   string
	DatabaseURL string
	Objects     Objects
	MatrixDir   string
	Version     string
	AdminGroups []string
	Log         *slog.Logger
	Dump        func(ctx context.Context, dbURL, file string) error
	Restore     func(ctx context.Context, dbURL, file string) error
	Migrate     func(ctx context.Context) error
}

// Manifest is manifest.json of a copy: what it holds and how to put it back.
type Manifest struct {
	Format    int       `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"go24_version,omitempty"`
	Parts     []string  `json:"parts"`
	Files     []string  `json:"files"`
	Secrets   bool      `json:"secrets"` // secrets.tar.gz.enc is there (encrypted with the backups' password)
	// Trees: of the files' folders (files/, media/) — how many files, how many of them and bytes written anew (the
	// others are hard links to the previous copy)
	Trees map[string]TreeStats `json:"trees,omitempty"`
}

// TreeStats are the numbers of a folder of files of a copy.
type TreeStats struct {
	Files int   `json:"files"`
	Fresh int   `json:"fresh"`
	Bytes int64 `json:"fresh_bytes"`
}

// Loop asks for the scheduled copies and makes the copies asked for until ctx ends.
func (r *Runner) Loop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		if _, err := r.Store.queueDue(ctx, time.Now()); err != nil && ctx.Err() == nil {
			r.Log.Error("backup schedule", "err", err)
		}
		if err := r.Next(ctx); err != nil && ctx.Err() == nil {
			r.Log.Error("backup", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Next makes the oldest copy asked for, if any.
func (r *Runner) Next(ctx context.Context) error {
	j, err := r.Store.take(ctx)
	if err != nil || j.ID == 0 {
		return err
	}
	if j.Kind == "restore" {
		return r.restore(ctx, j)
	}
	id := j.ID
	st, err := r.Store.Settings(ctx)
	if err != nil {
		return r.Store.finish(ctx, id, "", nil, 0, err)
	}
	start := time.Now()
	name, parts, size, runErr := r.make(ctx, st, start)
	var where, dst string
	var upErr error
	if runErr == nil && st.Target.Type != "" { // to the outside place too; the copy in the folder stays either way
		if dst, upErr = r.Store.upload(ctx, r.RC, st.Target, r.RCRoot, name); upErr == nil {
			where = st.Target.at(dst)
		}
	}
	ctx = context.WithoutCancel(ctx)
	if err := r.Store.finishUp(ctx, id, name, parts, size, runErr, where, dst, upErr); err != nil {
		return err
	}
	if runErr != nil {
		r.Log.Error("backup failed", "id", id, "err", runErr)
		return r.Store.tellFailed(ctx, runErr, r.AdminGroups)
	}
	r.Log.Info("backup made", "id", id, "name", name, "size", size, "where", where, "took", time.Since(start).Round(time.Second))
	if upErr != nil {
		r.Log.Error("backup upload failed", "id", id, "err", upErr)
		if err := r.Store.tellFailed(ctx, fmt.Errorf("%s: %w", st.Target.Type, upErr), r.AdminGroups); err != nil {
			return err
		}
	}
	return r.prune(ctx, st)
}

// prune removes the copies older than the Keep latest made: their folders and what went to the outside place of
// the settings now (one put to a place there was before stays there); the journal keeps the lines.
func (r *Runner) prune(ctx context.Context, st Settings) error {
	keep := st.Keep
	if keep <= 0 {
		return nil
	}
	rows, err := r.Store.db.Query(ctx, `SELECT id, name, target_path FROM backup_runs WHERE kind = 'backup' AND status = 'done' AND removed_at IS NULL AND name <> ''
		ORDER BY id DESC OFFSET $1`, keep)
	if err != nil {
		return err
	}
	type old struct {
		ID         int64
		Name       string
		TargetPath string
	}
	connected := false
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[old])
	if err != nil {
		return err
	}
	for _, o := range list {
		rel, err := CleanDir(o.Name)
		if err != nil || !strings.HasPrefix(filepath.Base(rel), "go24-") { // only a copy's own folder
			continue
		}
		if o.TargetPath != "" && st.Target.Type != "" && r.RC != nil {
			if !connected {
				if err := r.Store.connect(ctx, r.RC, st.Target); err != nil {
					r.Log.Warn("backup prune: the outside place", "err", err)
				}
				connected = true
			}
			if err := r.RC.Purge(ctx, remoteName+":", o.TargetPath); err != nil {
				r.Log.Warn("backup prune: the outside place", "path", o.TargetPath, "err", err)
			}
		}
		if err := os.RemoveAll(filepath.Join(r.Root, filepath.FromSlash(rel))); err != nil {
			r.Log.Warn("backup prune", "name", o.Name, "err", err)
			continue
		}
		if _, err := r.Store.db.Exec(ctx, `UPDATE backup_runs SET removed_at = now() WHERE id = $1`, o.ID); err != nil {
			return err
		}
	}
	return nil
}

// make writes the copy into <Root>/<Dir>/go24-YYYYMMDD-HHMMSS (-2… when there is one): being written it is «….partial», removed on an error.
func (r *Runner) make(ctx context.Context, st Settings, at time.Time) (string, []string, int64, error) {
	name := "go24-" + at.Format("20060102-150405")
	base := filepath.Join(r.Root, filepath.FromSlash(st.Dir))
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, 0, err
	}
	for n := 2; ; n++ { // two copies within a second: «-2», «-3»…
		if _, err := os.Stat(filepath.Join(base, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("go24-%s-%d", at.Format("20060102-150405"), n)
	}
	tmp := filepath.Join(base, name+".partial")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		return "", nil, 0, err
	}
	m := Manifest{Format: 1, CreatedAt: at, Version: r.Version}
	err := func() error {
		env := map[string]string{}
		if st.Has("config") || st.Has("authentik") || st.Has("synapse") {
			env = readEnv(filepath.Join(r.ConfigDir, ".env"))
		}
		for _, db := range []string{"go24", "authentik", "synapse"} {
			if !st.Has(db) {
				continue
			}
			u, err := r.dbURL(db, env)
			if err != nil {
				return err
			}
			file := db + ".dump"
			if err := r.dump(ctx, u, filepath.Join(tmp, file)); err != nil {
				return fmt.Errorf("%s: %w", db, err)
			}
			m.Parts, m.Files = append(m.Parts, db), append(m.Files, file)
		}
		for _, t := range []struct{ part, dir string }{{"files", "files"}, {"media", "media"}} {
			if !st.Has(t.part) {
				continue
			}
			prev, err := r.Store.lastDir(ctx, t.part)
			if err != nil {
				return err
			}
			if prev != "" {
				prev = filepath.Join(r.Root, filepath.FromSlash(prev), t.dir)
			}
			var mr *mirror
			switch {
			case t.part == "files" && r.Objects != nil:
				mr, err = copyObjects(ctx, r.Objects, filepath.Join(tmp, t.dir), prev)
			case t.part == "media" && r.MatrixDir != "":
				mr, err = copyTree(ctx, filepath.Join(r.MatrixDir, "media_store"), filepath.Join(tmp, t.dir), prev)
			default:
				err = fmt.Errorf("not reachable from go24-worker")
			}
			if err != nil {
				return fmt.Errorf("%s: %w", t.part, err)
			}
			if m.Trees == nil {
				m.Trees = map[string]TreeStats{}
			}
			m.Trees[t.dir] = TreeStats{Files: mr.files, Fresh: mr.fresh, Bytes: mr.bytes}
			m.Parts, m.Files = append(m.Parts, t.part), append(m.Files, t.dir+"/")
		}
		if st.Has("config") {
			if r.ConfigDir == "" {
				return fmt.Errorf("config: no snapshot of the configuration (GO24_BACKUP_CONFIG_DIR)")
			}
			password, err := r.Store.Password(st)
			if err != nil {
				return err
			}
			plain, secret, err := packConfig(r.ConfigDir, password != "")
			if err != nil {
				return fmt.Errorf("config: %w", err)
			}
			if err := os.WriteFile(filepath.Join(tmp, "config.tar.gz"), plain, 0o600); err != nil {
				return err
			}
			m.Parts, m.Files = append(m.Parts, "config"), append(m.Files, "config.tar.gz")
			if password != "" {
				enc, err := Encrypt(password, secret)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(tmp, "secrets.tar.gz.enc"), enc, 0o600); err != nil {
					return err
				}
				m.Parts, m.Files, m.Secrets = append(m.Parts, "secrets"), append(m.Files, "secrets.tar.gz.enc"), true
			}
		}
		raw, _ := json.MarshalIndent(m, "", "  ")
		return os.WriteFile(filepath.Join(tmp, "manifest.json"), raw, 0o600)
	}()
	if err == nil {
		err = os.Rename(tmp, filepath.Join(base, name))
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", m.Parts, 0, err
	}
	// the size is what the copy took anew: the files written, not the hard links to the previous copy
	var size int64
	for _, t := range m.Trees {
		size += t.Bytes
	}
	top := filepath.Join(base, name)
	_ = filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
		if _, tree := m.Trees[d.Name()]; err == nil && d.IsDir() && filepath.Dir(p) == top && tree {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			if i, err := d.Info(); err == nil {
				size += i.Size()
			}
		}
		return nil
	})
	return relName(st.Dir, name), m.Parts, size, nil
}

func relName(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// dbURL: go24's database as go24 reaches it; Authentik's and Matrix's — on the same PostgreSQL with their users and
// the passwords of .env.
func (r *Runner) dbURL(db string, env map[string]string) (string, error) {
	if db == "go24" {
		return r.DatabaseURL, nil
	}
	key := map[string]string{"authentik": "AUTHENTIK_DB_PASSWORD", "synapse": "SYNAPSE_DB_PASSWORD"}[db]
	pw := env[key]
	if pw == "" {
		return "", fmt.Errorf("%s: no %s in the configuration", db, key)
	}
	u, err := url.Parse(r.DatabaseURL)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(db, pw)
	u.Path = "/" + db
	return u.String(), nil
}

func (r *Runner) dump(ctx context.Context, dbURL, file string) error {
	if r.Dump != nil {
		return r.Dump(ctx, dbURL, file)
	}
	cmd := exec.CommandContext(ctx, "pg_dump", "--format=custom", "--file="+file, "--dbname="+dbURL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return os.Chmod(file, 0o600)
}

// readEnv reads KEY=value lines (quotes taken off); a file not there — nothing.
func readEnv(file string) map[string]string {
	env := map[string]string{}
	f, err := os.Open(file)
	if err != nil {
		return env
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env
}

// secretFile: a file of the configuration holding secrets — .env, rclone.conf, or one its owner alone reads.
func secretFile(rel string, mode fs.FileMode) bool {
	base := filepath.Base(rel)
	return base == ".env" || base == "rclone.conf" || mode.Perm()&0o044 == 0
}

// packConfig packs the configuration into two tar.gz: the plain files and the secrets (made only when wanted).
func packConfig(dir string, secrets bool) (plain, secret []byte, err error) {
	var pb, sb bytes.Buffer
	pz, sz := gzip.NewWriter(&pb), gzip.NewWriter(&sb)
	pt, stw := tar.NewWriter(pz), tar.NewWriter(sz)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() { // links, sockets: not copied
			return nil
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		w := pt
		if info.Mode().IsRegular() && secretFile(rel, info.Mode()) {
			if !secrets {
				return nil
			}
			w = stw
		}
		if info.IsDir() {
			hdr.Name += "/"
			return pt.WriteHeader(hdr)
		}
		if err := w.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	for _, c := range []io.Closer{pt, pz, stw, sz} {
		if cerr := c.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return nil, nil, err
	}
	return pb.Bytes(), sb.Bytes(), nil
}
