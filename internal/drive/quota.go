package drive

import (
	"context"

	"go24/internal/i18n"
)

// Limits are how much a drive of each kind may hold, in bytes (0 — no limit), how many former versions of a file are
// kept (0 — all of them) and what of the drives the company has turned off («Настройки → Диск»: public
// links, the locking of documents, the search by the text of files.
type Limits struct {
	User, Company, Project      int64
	Versions                    int
	NoLinks, NoLocks, NoContent bool
}

// the switches of Limits, kept in drive_limits as 1 (off) or 0
const (
	offLinks   = "no_links"
	offLocks   = "no_locks"
	offContent = "no_content"
)

// off: the company has turned the thing of drive_limits off.
func (s *Store) off(ctx context.Context, kind string) (bool, error) {
	var off bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM drive_limits WHERE kind = $1 AND bytes = 1)`, kind).Scan(&off)
	return off, err
}

func bit(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// Of is the limit of the drive (a folder shared with someone is its owner's «Мой диск»).
func (l Limits) Of(d Drive) int64 {
	switch d.Kind {
	case "company":
		return l.Company
	case "project":
		return l.Project
	}
	return l.User
}

// Limits of the drives, set by an administrator.
func (s *Store) Limits(ctx context.Context) (Limits, error) {
	var l Limits
	rows, err := s.db.Query(ctx, `SELECT kind, bytes FROM drive_limits`)
	if err != nil {
		return l, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var b int64
		if err := rows.Scan(&kind, &b); err != nil {
			return l, err
		}
		switch kind {
		case "user":
			l.User = b
		case "company":
			l.Company = b
		case "project":
			l.Project = b
		case "versions":
			l.Versions = int(b)
		case offLinks:
			l.NoLinks = b == 1
		case offLocks:
			l.NoLocks = b == 1
		case offContent:
			l.NoContent = b == 1
		}
	}
	return l, rows.Err()
}

// SaveLimits keeps the limits of the drives.
func (s *Store) SaveLimits(ctx context.Context, l Limits) error {
	if l.User < 0 || l.Company < 0 || l.Project < 0 || l.Versions < 0 {
		return ErrNotFound
	}
	_, err := s.db.Exec(ctx, `INSERT INTO drive_limits (kind, bytes) VALUES ('user', $1), ('company', $2), ('project', $3), ('versions', $4),
			('no_links', $5), ('no_locks', $6), ('no_content', $7)
		ON CONFLICT (kind) DO UPDATE SET bytes = EXCLUDED.bytes`, l.User, l.Company, l.Project, l.Versions, bit(l.NoLinks), bit(l.NoLocks), bit(l.NoContent))
	return err
}

// Usage is how much a drive holds and may hold (Limit 0 — no limit).
type Usage struct {
	Used, Limit int64
}

// Percent of the limit used (0 without a limit), at most 100.
func (u Usage) Percent() int {
	if u.Limit <= 0 {
		return 0
	}
	return int(min(100, u.Used*100/u.Limit))
}

// used counts the files of drives with their former versions and their trash. by groups the files (the drive, the
// owner); where narrows them.
const used = `SELECT f.drive, coalesce(f.drive_owner::text, '') AS owner,
		sum(f.size + coalesce((SELECT sum(v.size) FROM drive_file_versions v WHERE v.file_id = f.id), 0))::bigint AS used
	FROM files f WHERE f.drive IS NOT NULL`

// Usage of the drive.
func (s *Store) Usage(ctx context.Context, d Drive) (Usage, error) {
	l, err := s.Limits(ctx)
	if err != nil {
		return Usage{}, err
	}
	u := Usage{Limit: l.Of(d)}
	err = s.db.QueryRow(ctx, `SELECT coalesce(sum(used), 0)::bigint FROM (`+used+` AND f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2
		GROUP BY 1, 2) x`, d.Kind, d.owner()).Scan(&u.Used)
	return u, err
}

// ErrFull: the drive would hold more than its limit.
var ErrFull = i18n.E("err.disk_full")

// Fits: size more bytes fit in the drive.
func (s *Store) Fits(ctx context.Context, d Drive, size int64) error {
	u, err := s.Usage(ctx, d)
	if err == nil && u.Limit > 0 && u.Used+size > u.Limit {
		return ErrFull
	}
	return err
}

// DriveUsage is one drive in the administrator's list: whose it is and how much it holds.
type DriveUsage struct {
	Kind, OwnerID, Name, ProjectKey string
	Used                            int64
}

// Usages are all the drives that hold anything, the fullest first.
func (s *Store) Usages(ctx context.Context) ([]DriveUsage, error) {
	rows, err := s.db.Query(ctx, `SELECT x.drive, x.owner, coalesce(u.name, p.name, ''), coalesce(p.key, ''), x.used
		FROM (`+used+` GROUP BY 1, 2) x
		LEFT JOIN users u ON x.drive = 'user' AND u.id::text = x.owner
		LEFT JOIN projects p ON x.drive = 'project' AND p.id::text = x.owner
		ORDER BY x.used DESC, 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []DriveUsage
	for rows.Next() {
		var d DriveUsage
		if err := rows.Scan(&d.Kind, &d.OwnerID, &d.Name, &d.ProjectKey, &d.Used); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}
