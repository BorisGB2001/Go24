package drive

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Space is «Очистка» of a drive: what its used place is made of, and its biggest files.
type Space struct {
	Files, Versions, Trash              int64 // bytes: the current files, their former versions, the trash (with its versions)
	FileCount, VersionCount, TrashCount int
	Big                                 []BigFile
}

// Used is all the drive holds, as Usage counts it.
func (sp Space) Used() int64 { return sp.Files + sp.Versions + sp.Trash }

// BigFile is one of the biggest files of a drive with its former versions.
type BigFile struct {
	ID, Name, FolderID, Where string
	Size, VersionsSize        int64
	Versions                  int
	CreatedAt                 time.Time
}

// Total is the file with its former versions.
func (b BigFile) Total() int64 { return b.Size + b.VersionsSize }

// goneSQL is the folders of the drive ($1, $2) in the trash, with all the folders within them.
const goneSQL = `WITH RECURSIVE gone AS (
		SELECT id, 0 AS depth FROM drive_folders WHERE drive = $1 AND owner_id IS NOT DISTINCT FROM $2 AND deleted_at IS NOT NULL
		UNION ALL SELECT f.id, gone.depth + 1 FROM drive_folders f JOIN gone ON f.parent_id = gone.id WHERE gone.depth < 50)`

// bigMax is how many of the biggest files «Очистка» lists.
const bigMax = 20

// mayClean: the drive is cleaned by the one who manages all of it — the owner of «Мой диск», an administrator of
// the company's, a manager of a project.
func (d Drive) mayClean() bool { return d.CanManage && d.Root == "" && d.File == "" }

// MayClean: the viewer may see «Очистка» of the drive.
func (d Drive) MayClean() bool { return d.mayClean() }

// Space counts what the drive holds.
func (s *Store) Space(ctx context.Context, d Drive) (Space, error) {
	var sp Space
	if !d.mayClean() {
		return sp, ErrForbidden
	}
	err := s.db.QueryRow(ctx, goneSQL+`, x AS (SELECT f.size, v.size AS vsize, v.n,
				f.deleted_at IS NOT NULL OR coalesce(f.folder_id IN (SELECT id FROM gone), false) AS trashed
			FROM files f LEFT JOIN LATERAL (SELECT sum(size) AS size, count(*) AS n FROM drive_file_versions WHERE file_id = f.id) v ON true
			WHERE f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2)
		SELECT coalesce(sum(size) FILTER (WHERE NOT trashed), 0)::bigint, count(*) FILTER (WHERE NOT trashed),
			coalesce(sum(vsize) FILTER (WHERE NOT trashed), 0)::bigint, coalesce(sum(n) FILTER (WHERE NOT trashed), 0)::int,
			coalesce(sum(size + coalesce(vsize, 0)) FILTER (WHERE trashed), 0)::bigint, count(*) FILTER (WHERE trashed)
		FROM x`, d.Kind, d.owner()).
		Scan(&sp.Files, &sp.FileCount, &sp.Versions, &sp.VersionCount, &sp.Trash, &sp.TrashCount)
	if err != nil {
		return sp, err
	}
	rows, err := s.db.Query(ctx, goneSQL+` SELECT f.id::text, f.name, coalesce(f.folder_id::text, ''), coalesce(p.name, ''), f.size,
			coalesce(v.size, 0)::bigint, coalesce(v.n, 0)::int, f.created_at
		FROM files f LEFT JOIN drive_folders p ON p.id = f.folder_id
			LEFT JOIN LATERAL (SELECT sum(size) AS size, count(*) AS n FROM drive_file_versions WHERE file_id = f.id) v ON true
		WHERE f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2 AND f.deleted_at IS NULL
			AND NOT coalesce(f.folder_id IN (SELECT id FROM gone), false)
		ORDER BY f.size + coalesce(v.size, 0) DESC, f.name LIMIT $3`, d.Kind, d.owner(), bigMax)
	if err != nil {
		return sp, err
	}
	sp.Big, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (BigFile, error) {
		var b BigFile
		err := row.Scan(&b.ID, &b.Name, &b.FolderID, &b.Where, &b.Size, &b.VersionsSize, &b.Versions, &b.CreatedAt)
		return b, err
	})
	return sp, err
}

// PurgeVersions deletes the former versions of the drive's files — of those listed, or of all of them when none are;
// the current versions stay. It returns how many went.
func (s *Store) PurgeVersions(ctx context.Context, d Drive, files []string) (int, error) {
	if !d.mayClean() {
		return 0, ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM drive_file_versions v USING files f
		WHERE f.id = v.file_id AND f.drive = $1 AND f.drive_owner IS NOT DISTINCT FROM $2 AND ($3::text[] IS NULL OR f.id::text = ANY($3))`,
		d.Kind, d.owner(), nilIfEmpty(files))
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// EmptyTrash deletes for good all that is in the drive's trash.
func (s *Store) EmptyTrash(ctx context.Context, userID string, d Drive) error {
	if !d.mayClean() {
		return ErrForbidden
	}
	list, err := s.Trash(ctx, d)
	if err != nil {
		return err
	}
	var sel Selection
	for _, t := range list {
		if t.Kind == "folder" {
			sel.Folders = append(sel.Folders, t.ID)
		} else {
			sel.Files = append(sel.Files, t.ID)
		}
	}
	return s.Purge(ctx, userID, d, sel)
}

// trimVersions keeps at most keep former versions of the file (0 — all of them), the newest.
func trimVersions(ctx context.Context, tx pgx.Tx, id string, keep int) error {
	if keep <= 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM drive_file_versions WHERE id IN (
		SELECT id FROM drive_file_versions WHERE file_id::text = $1 ORDER BY created_at DESC, id OFFSET $2)`, id, keep)
	return err
}

func nilIfEmpty(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return list
}
