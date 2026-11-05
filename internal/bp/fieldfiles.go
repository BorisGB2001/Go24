package bp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"path"
	"strings"
	"time"

	"go24/internal/i18n"
)

// The «Файл» fields of a process, as the parameters of that type: the value is a file's id (of a multiple
// field — the ids, one a line). A file attached at the start or in an answer to a request belongs to the process
// (bp_files): those who see the process — or its post in the feed — download it.

// FileIDs are the files of the «Файл» fields among the values.
func FileIDs(fields []Field, values map[string]string) []string {
	var out []string
	for _, f := range fields {
		if f.Kind == "file" {
			for _, id := range f.List(values[f.Key]) {
				if !contains(out, id) {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// claimFiles makes the files of the process's: each one the user has just uploaded (theirs, of no process yet), or one
// already of the process or of its parent (handed to a child process, copied by «Изменить переменную»). Another file
// is refused: one may not get at someone's file by its id.
func (s *Store) claimFiles(ctx context.Context, instance, userID, parent string, ids []string) error {
	for _, id := range ids {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files f WHERE f.id::text = $1 AND (
				f.uploaded_by::text = $2 AND NOT EXISTS (SELECT 1 FROM bp_files b WHERE b.file_id = f.id)
				OR EXISTS (SELECT 1 FROM bp_files b WHERE b.file_id = f.id AND b.instance_id::text IN ($3, $4))))`,
			id, userID, instance, parent).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.bp_file")
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO bp_files (instance_id, file_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			instance, id); err != nil {
			return err
		}
	}
	return nil
}

// checkFiles: each file may become the process's (see claimFiles) — before the process starts.
func (s *Store) checkFiles(ctx context.Context, userID, parent string, ids []string) error {
	for _, id := range ids {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files f WHERE f.id::text = $1 AND (
				f.uploaded_by::text = $2 AND NOT EXISTS (SELECT 1 FROM bp_files b WHERE b.file_id = f.id)
				OR EXISTS (SELECT 1 FROM bp_files b WHERE b.file_id = f.id AND b.instance_id::text = $3)))`,
			id, userID, parent).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return i18n.E("err.bp_file")
		}
	}
	return nil
}

// FileSeen: the file is of a process the user sees (has started, decides in, administers — Instance), or whose post in
// the feed they read.
func (s *Store) FileSeen(ctx context.Context, userID, fileID string) bool {
	rows, err := s.db.Query(ctx, `SELECT instance_id::text FROM bp_files WHERE file_id::text = $1`, fileID)
	if err != nil {
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.Instance(ctx, userID, id); err == nil {
			return true
		}
		var ok bool
		if s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM feed_posts p WHERE p.bp_instance::text = $1 AND (p.author_id::text = $2
			OR EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = p.id AND r.user_id::text = $2)))`, id, userID).Scan(&ok) == nil && ok {
			return true
		}
	}
	return false
}

// FileNames are the names of the files by their ids (a missing one is left out).
func (s *Store) FileNames(ctx context.Context, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	rows, err := s.db.Query(ctx, `SELECT id::text, name FROM files WHERE id::text = ANY($1)`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			out[id] = name
		}
	}
	return out
}

// fileText is a «Файл» field's value in a text: each file's name and its address.
func (s *Store) fileText(ctx context.Context, f Field, v string) string {
	ids := f.List(v)
	names := s.FileNames(ctx, ids)
	var out []string
	for _, id := range ids {
		out = append(out, strings.TrimSpace(names[id]+" "+"/files/"+id))
	}
	return strings.Join(out, ", ")
}

// MaxMailFiles bounds the bytes of the files a letter of a process carries.
const MaxMailFiles = 20 << 20

// ProcessFile is a file of a process with its bytes, for a letter.
type ProcessFile struct {
	Name, ContentType string
	Data              []byte
}

// processFiles are the files of the process's «Файл» fields with their bytes (a letter's attachments), not more than
// MaxMailFiles in all.
func (s *Store) processFiles(ctx context.Context, x Instance) ([]ProcessFile, error) {
	var out []ProcessFile
	var total int64
	for _, id := range FileIDs(AllFields(x.Fields, x.Steps), x.Values) {
		var key, name, ct string
		var size int64
		var storage *string
		if err := s.db.QueryRow(ctx, `SELECT object_key, name, content_type, size, storage_id FROM files WHERE id::text = $1`, id).
			Scan(&key, &name, &ct, &size, &storage); err != nil {
			continue // deleted since
		}
		if total += size; total > MaxMailFiles {
			return nil, i18n.E("err.bp_files_big")
		}
		if s.OpenFile == nil {
			return nil, i18n.E("err.bp_file")
		}
		rc, err := s.OpenFile(ctx, storage, key)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, MaxMailFiles+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		out = append(out, ProcessFile{Name: name, ContentType: ct, Data: data})
	}
	return out, nil
}

// attachToTask puts copies of the files of the process's «Файл» fields into the task, as its attachments: the task
// keeps them whatever happens to the process.
func (s *Store) attachToTask(ctx context.Context, x Instance, taskID string) error {
	for _, id := range FileIDs(AllFields(x.Fields, x.Steps), x.Values) {
		var key, name, ct string
		var size int64
		var storage *string
		if err := s.db.QueryRow(ctx, `SELECT object_key, name, content_type, size, storage_id FROM files WHERE id::text = $1`, id).
			Scan(&key, &name, &ct, &size, &storage); err != nil {
			continue
		}
		if s.CopyBytes == nil {
			return i18n.E("err.bp_file")
		}
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		to := path.Join("go24", time.Now().Format("2006/01"), hex.EncodeToString(b), name)
		if err := s.CopyBytes(ctx, storage, key, to, size, ct); err != nil {
			return err
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, storage_id, task_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, to, name, size, ct, x.StartedBy.ID, storage, taskID); err != nil {
			return err
		}
		_ = s.Tasks.FileAdded(ctx, x.StartedBy.ID, taskID, name)
	}
	return nil
}
