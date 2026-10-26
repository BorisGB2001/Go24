package drive

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Connected is a folder shared with the user or a project's drive they connected to their «Мой диск»
// («Подключить к своему Диску»): shown at its top, it opens as a drive of its own.
type Connected struct {
	Which       string // what Open takes: sh:FOLDER or a project key
	Name, Owner string // the folder's name and whose «Мой диск» it is; a project's name ("" owner)
	Path        string // its address in go24
	Project     bool
	CreatedAt   time.Time
}

// Connect shows the folder shared with the user (which "sh:ID") or the drive of a project of theirs (its key) in
// their «Мой диск»; connected twice it stays one.
func (s *Store) Connect(ctx context.Context, userID, which string) error {
	d, err := s.connectable(ctx, userID, which)
	if err != nil {
		return err
	}
	folder, project := connection(d)
	_, err = s.db.Exec(ctx, `INSERT INTO drive_connections (user_id, folder_id, project_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		userID, folder, project)
	return err
}

// Disconnect takes the folder or the project's drive out of the user's «Мой диск»; their access stays.
func (s *Store) Disconnect(ctx context.Context, userID, which string) error {
	if id, ok := strings.CutPrefix(which, "sh:"); ok {
		_, err := s.db.Exec(ctx, `DELETE FROM drive_connections WHERE user_id = $1 AND folder_id::text = $2`, userID, id)
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM drive_connections c USING projects p WHERE c.user_id = $1 AND p.id = c.project_id AND p.key = $2`,
		userID, which)
	return err
}

// connectable is the drive the user may connect: a folder shared with them, a project's drive (not a collab's).
func (s *Store) connectable(ctx context.Context, userID, which string) (Drive, error) {
	if strings.HasPrefix(which, "sf:") || which == "my" || which == "company" {
		return Drive{}, ErrNotFound
	}
	d, err := s.Open(ctx, userID, which)
	if err == nil && (d.Collab || (d.Kind != "project" && d.Root == "")) {
		return Drive{}, ErrNotFound
	}
	return d, err
}

// connection is the folder or the project a connection of the drive points at.
func connection(d Drive) (folder, project *string) {
	if d.Root != "" {
		return &d.Root, nil
	}
	return nil, &d.OwnerID
}

// IsConnected: the drive is connected to the user's «Мой диск».
func (s *Store) IsConnected(ctx context.Context, userID string, d Drive) bool {
	if d.Root == "" && d.Kind != "project" {
		return false
	}
	folder, project := connection(d)
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM drive_connections WHERE user_id = $1
		AND folder_id IS NOT DISTINCT FROM $2 AND project_id IS NOT DISTINCT FROM $3)`, userID, folder, project).Scan(&ok)
	return ok
}

// Connections are what the user connected to their «Мой диск» and still may open, by name.
func (s *Store) Connections(ctx context.Context, userID string) ([]Connected, error) {
	rows, err := s.db.Query(ctx, `SELECT CASE WHEN c.folder_id IS NOT NULL THEN 'sh:' || c.folder_id::text ELSE p.key END, c.created_at
		FROM drive_connections c LEFT JOIN projects p ON p.id = c.project_id WHERE c.user_id = $1 ORDER BY c.created_at`, userID)
	if err != nil {
		return nil, err
	}
	type conn struct {
		which string
		at    time.Time
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (conn, error) {
		var c conn
		err := row.Scan(&c.which, &c.at)
		return c, err
	})
	if err != nil {
		return nil, err
	}
	var out []Connected
	for _, c := range list {
		d, err := s.connectable(ctx, userID, c.which)
		if err != nil { // no longer shared with them, not in the project, in the trash: not shown
			continue
		}
		out = append(out, Connected{Which: c.which, Name: d.Name, Owner: d.Owner, Path: d.Path(), Project: d.Root == "", CreatedAt: c.at})
	}
	return out, nil
}
