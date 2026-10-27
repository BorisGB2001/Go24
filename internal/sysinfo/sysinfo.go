// Package sysinfo is «Состояние системы», as «Проверка системы» of a portal box: the running copies of go24, the
// services it uses, the database and the free space of its folders.
package sysinfo

import (
	"context"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Version is go24's version, set at build time (-ldflags "-X go24/internal/sysinfo.Version=…" from VERSION).
var Version = "dev"

// Stale is how long a copy of go24 may stay silent before it is shown as stopped.
const Stale = 3 * time.Minute

// Node is a running copy of go24: the pages (web), the background (worker) or both (all).
type Node struct {
	Role      string    `json:"role"`
	Host      string    `json:"host"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
	SeenAt    time.Time `json:"seen_at"`
	Alive     bool      `json:"alive"`
}

// Store keeps the copies' heartbeats in the database.
type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Beat says once a minute that this copy is alive, until ctx is done.
func (s *Store) Beat(ctx context.Context, role string, onErr func(error)) {
	host, _ := os.Hostname()
	started := time.Now()
	// a container made anew has a new name: the silent copies of this role are gone for good
	if _, err := s.db.Exec(ctx, `DELETE FROM system_nodes WHERE role = $1 AND seen_at < now() - $2::interval`, role, Stale.String()); err != nil && onErr != nil {
		onErr(err)
	}
	for t := time.NewTicker(time.Minute); ; {
		if _, err := s.db.Exec(ctx, `INSERT INTO system_nodes (id, role, host, version, started_at) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (id) DO UPDATE SET role = $2, version = $4, started_at = $5, seen_at = now()`,
			host+"/"+role, role, host, Version, started); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Nodes are the copies seen in the last hour, the newest first; those silent longer than Stale are not Alive.
func (s *Store) Nodes(ctx context.Context) ([]Node, error) {
	rows, err := s.db.Query(ctx, `SELECT role, host, version, started_at, seen_at, seen_at > now() - $1::interval
		FROM system_nodes WHERE seen_at > now() - interval '1 hour' ORDER BY role, seen_at DESC`, Stale.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		if err := rows.Scan(&n.Role, &n.Host, &n.Version, &n.StartedAt, &n.SeenAt, &n.Alive); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// Database is what PostgreSQL says of go24's database.
type Database struct {
	Version     string `json:"version"`
	Size        int64  `json:"size"`
	Migration   string `json:"migration"` // the last migration applied
	Connections int    `json:"connections"`
}

func (s *Store) Database(ctx context.Context) (Database, error) {
	var d Database
	err := s.db.QueryRow(ctx, `SELECT current_setting('server_version'), pg_database_size(current_database()),
		(SELECT coalesce(max(version), '') FROM schema_migrations),
		(SELECT count(*) FROM pg_stat_activity WHERE datname = current_database())`).
		Scan(&d.Version, &d.Size, &d.Migration, &d.Connections)
	return d, err
}

// Probe is a service go24 uses, checked by a call: an HTTP address (any answer below 500 means it works) or a
// function of its own.
type Probe struct {
	ID     string // authentik, matrix, livekit, office, storage, rclone, asterisk, whisper
	Target string // the address shown
	URL    string
	Check  func(ctx context.Context) error
}

// Check is the result of a probe.
type Check struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
	Millis int64  `json:"ms"`
}

// Run checks the services at once, each within timeout, in the probes' order.
func Run(ctx context.Context, probes []Probe, timeout time.Duration) []Check {
	out := make([]Check, len(probes))
	done := make(chan struct{}, len(probes))
	for i, p := range probes {
		go func() {
			defer func() { done <- struct{}{} }()
			c, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			t0 := time.Now()
			err := p.run(c)
			out[i] = Check{ID: p.ID, Target: p.Target, OK: err == nil, Millis: time.Since(t0).Milliseconds()}
			if err != nil {
				out[i].Error = err.Error()
			}
		}()
	}
	for range probes {
		<-done
	}
	return out
}

func (p Probe) run(ctx context.Context) error {
	if p.Check != nil {
		return p.Check(ctx)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return &statusError{resp.Status}
	}
	return nil
}

type statusError struct{ status string }

func (e *statusError) Error() string { return "HTTP " + e.status }

// HTTPProbe is a probe of the service at base (nothing when base is empty) by its path; the address shown is base
// without its credentials and path.
func HTTPProbe(id, base, path string) []Probe {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return nil
	}
	target := base
	if i := strings.Index(target, "://"); i >= 0 {
		rest := target[i+3:]
		if j := strings.IndexByte(rest, '@'); j >= 0 {
			rest = rest[j+1:]
		}
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			rest = rest[:j]
		}
		target = target[:i+3] + rest
	}
	return []Probe{{ID: id, Target: target, URL: base + path}}
}

// Disk is the space of the file system a folder of go24 lies on.
type Disk struct {
	ID    string `json:"id"`   // backup, records, telephony
	Path  string `json:"path"` // as go24 sees it
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
	Error string `json:"error,omitempty"`
}

// Low is too little free space: under 10 % or 2 GB, whichever is less.
func (d Disk) Low() bool {
	return d.Error == "" && d.Total > 0 && d.Free < min(d.Total/10, int64(2<<30))
}

// UsedPercent is the share of the space taken.
func (d Disk) UsedPercent() int {
	if d.Total == 0 {
		return 0
	}
	return int((d.Total - d.Free) * 100 / d.Total)
}

// DiskOf is the space of the folder at path.
func DiskOf(id, path string) Disk {
	d := Disk{ID: id, Path: path}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		d.Error = err.Error()
		return d
	}
	d.Total, d.Free = int64(st.Blocks)*int64(st.Bsize), int64(st.Bavail)*int64(st.Bsize)
	return d
}
