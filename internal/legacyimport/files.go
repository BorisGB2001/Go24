package legacyimport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"path"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/anon"
)

// PutFunc stores an object (the built-in storage in production, a stub in tests).
type PutFunc func(ctx context.Context, key string, r io.Reader, size int64, contentType string) error

// ImportFiles attaches the portal task and comment files to the imported tasks as EMPTY placeholders:
// the name (anonymized) and type are kept, the content is not — documents cannot be anonymized.
//
//	files.tsv  ATTACHED_ID, TASK_ID, NAME (base64), CONTENT_TYPE, CREATED_BY, KIND (task|comment), CREATE_TIME
//	users.tsv  as for Import — needed to apply the same pseudonyms to file names
//
// Returns how many attachments were added (already imported ones are skipped).
func ImportFiles(ctx context.Context, db *pgxpool.Pool, put PutFunc, o Options) (int, error) {
	users, err := readTSV(filepath.Join(o.Dir, "users.tsv"), 7)
	if err != nil {
		return 0, err
	}
	files, err := readTSV(filepath.Join(o.Dir, "files.tsv"), 7)
	if err != nil {
		return 0, err
	}
	for _, u := range users {
		o.Anon.AddPerson(anon.Person{ID: atoi(u[0]), First: b64(u[1]), Last: b64(u[2]), Middle: b64(u[3]),
			Gender: u[4], Email: b64(u[5]), Login: b64(u[6])})
	}

	added := 0
	for _, f := range files {
		attachedID, legacyTask := atoi(f[0]), atoi(f[1])
		var taskID string
		if err := db.QueryRow(ctx, `SELECT id FROM tasks WHERE legacy_id = $1`, legacyTask).Scan(&taskID); err != nil {
			continue // the task was not imported
		}
		var exists bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE legacy_id = $1)`, attachedID).Scan(&exists); err != nil {
			return added, err
		}
		if exists {
			continue
		}
		name := safeName(o.Anon.Text(b64(f[2])))
		ct := f[3]
		if ct == "" {
			if ct = mime.TypeByExtension(path.Ext(name)); ct == "" {
				ct = "application/octet-stream"
			}
		}
		var uploader *string
		if by := atoi(f[4]); by > 0 {
			var id string
			if db.QueryRow(ctx, `SELECT id FROM users WHERE oidc_sub = $1`, fmt.Sprintf("legacy:%d", by)).Scan(&id) == nil {
				uploader = &id
			}
		}
		// No object is written: an empty file needs no bytes (and rclone serve s3 refuses zero-length bodies
		// without Content-Length). Downloads of size-0 files are answered without touching the storage.
		key := path.Join("legacy-empty", fmt.Sprint(attachedID), name)
		if put != nil {
			if err := put(ctx, key, bytes.NewReader(nil), 0, ct); err != nil {
				return added, fmt.Errorf("store %s: %w", key, err)
			}
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO files (object_key, name, size, content_type, uploaded_by, task_id, legacy_id, created_at)
			VALUES ($1, $2, 0, $3, $4, $5, $6, coalesce($7, now()))`,
			key, name, ct, uploader, taskID, attachedID, o.parseTime(f[6])); err != nil {
			return added, fmt.Errorf("file %d: %w", attachedID, err)
		}
		added++
	}
	return added, nil
}

// safeName keeps a file name usable as an object key segment.
func safeName(n string) string {
	n = strings.TrimSpace(strings.NewReplacer("/", "_", "\\", "_").Replace(n))
	if n == "" || n == "." || n == ".." {
		return "file"
	}
	return n
}
