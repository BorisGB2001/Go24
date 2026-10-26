package drive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// live: the folder f.folder_id and those above it are not in the trash
const live = `NOT EXISTS (WITH RECURSIVE up AS (
		SELECT id, parent_id, deleted_at, 1 AS depth FROM drive_folders WHERE id = f.folder_id
		UNION ALL SELECT p.id, p.parent_id, p.deleted_at, up.depth + 1 FROM drive_folders p JOIN up ON p.id = up.parent_id WHERE up.depth < 50)
	SELECT 1 FROM up WHERE deleted_at IS NOT NULL)`

// below: the folder $4 and all within it
const below = `WITH RECURSIVE down AS (
		SELECT id, 0 AS depth FROM drive_folders WHERE id::text = $4
		UNION ALL SELECT c.id, down.depth + 1 FROM drive_folders c JOIN down ON c.parent_id = down.id WHERE down.depth < 50)
	SELECT id FROM down`

// Search is the files of the whole drive (all its folders, not the trash) whose name or text has q ("" — any), newest
// first, and the folders whose name has it (none for an empty q).
func (s *Store) Search(ctx context.Context, d Drive, q string) (folders []File, files []File, err error) {
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, coalesce(st.name, ''), coalesce(u.name, ''),
			coalesce(f.uploaded_by::text, ''), f.size, f.created_at, coalesce(f.folder_id::text, ''), coalesce(p.name, ''),
			`+lockCols+`,
			coalesce((SELECT string_agg(eu.name, ', ' ORDER BY eu.name) FROM users eu WHERE eu.id = ANY(f.editing)
				AND f.editing_at > now() - interval '12 hours'), '')
		FROM files f LEFT JOIN storages st ON st.id = f.storage_id LEFT JOIN users u ON u.id = f.uploaded_by
			LEFT JOIN drive_folders p ON p.id = f.folder_id `+lockJoin+`
		WHERE f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2 AND f.deleted_at IS NULL AND (f.name ILIKE $3 OR `+contentMatch("5")+`) AND `+live+`
			AND ($4 = '' OR f.folder_id IN (`+below+`)) AND `+fmt.Sprintf(visibleSQL, "f.folder_id", "$6")+`
		ORDER BY f.created_at DESC LIMIT 5000`, d.Kind, d.owner(), like, d.Root, tsQuery(q), d.viewerArg())
	if err != nil {
		return nil, nil, err
	}
	if files, err = pgx.CollectRows(rows, pgx.RowToStructByPos[File]); err != nil || strings.TrimSpace(q) == "" {
		return nil, files, err
	}
	// a folder is found as a File with no size: ID, Name, its parent as FolderID / Where
	rows, err = s.db.Query(ctx, `SELECT f.id, f.name, '', '', '', '', 0::bigint, f.created_at, coalesce(f.parent_id::text, ''), coalesce(p.name, ''),
			'', '', NULL::timestamptz, ''
		FROM drive_folders f LEFT JOIN drive_folders p ON p.id = f.parent_id
		WHERE f.drive = $1 AND f.owner_id IS NOT DISTINCT FROM $2 AND f.deleted_at IS NULL AND f.name ILIKE $3
			AND `+strings.ReplaceAll(live, "f.folder_id", "f.parent_id")+`
			AND ($4 = '' OR f.parent_id IN (`+below+`)) AND `+fmt.Sprintf(visibleSQL, "f.id", "$5")+`
		ORDER BY lower(f.name) LIMIT 200`, d.Kind, d.owner(), like, d.Root, d.viewerArg())
	if err != nil {
		return nil, nil, err
	}
	folders, err = pgx.CollectRows(rows, pgx.RowToStructByPos[File])
	return folders, files, err
}

// Recent is a file the user uploaded or changed lately, on any drive.
type Recent struct {
	File
	Drive, DriveOwner, ProjectKey, ProjectName string
}

// Recent is the files the user uploaded or changed in the last days, newest first, on the drives they may read.
func (s *Store) Recent(ctx context.Context, userID string, days, max int) ([]Recent, error) {
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, coalesce(st.name, ''), coalesce(u.name, ''),
			coalesce(f.uploaded_by::text, ''), f.size, f.created_at, coalesce(f.folder_id::text, ''), coalesce(p.name, ''),
			f.drive, coalesce(f.drive_owner::text, ''), coalesce(pr.key, ''), coalesce(pr.name, '')
		FROM files f LEFT JOIN storages st ON st.id = f.storage_id LEFT JOIN users u ON u.id = f.uploaded_by
			LEFT JOIN drive_folders p ON p.id = f.folder_id LEFT JOIN projects pr ON f.drive = 'project' AND pr.id = f.drive_owner
		WHERE f.drive IS NOT NULL AND f.deleted_at IS NULL AND coalesce(f.changed_by, f.uploaded_by) = $1 AND f.created_at > $2 AND `+live+`
		ORDER BY f.created_at DESC LIMIT $3`, userID, time.Now().AddDate(0, 0, -days), max*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []Recent
	for rows.Next() {
		var r Recent
		f := &r.File
		if err := rows.Scan(&f.ID, &f.Name, &f.ContentType, &f.StorageName, &f.Uploader, &f.UploaderID, &f.Size, &f.CreatedAt,
			&f.FolderID, &f.Where, &r.Drive, &r.DriveOwner, &r.ProjectKey, &r.ProjectName); err != nil {
			return nil, err
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := list[:0]
	for _, r := range list {
		if len(out) < max && s.CanReadFile(ctx, userID, r.ID, r.Drive, r.DriveOwner) {
			out = append(out, r)
		}
	}
	return out, nil
}
