package drive

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/tasks"
)

// Document is an office document of «Документы»: a file on a drive the user may open.
type Document struct {
	Recent
	Base    string     // the address of its drive for the user: /disk/my, /disk/company, /disk/p/KEY, /disk/sh/FOLDER, /disk/sf/FILE
	Owner   string     // whose «Мой диск» it is on, when not the user's own
	Changer string     // who uploaded its current version
	Single  bool       // shared with the user by itself, not within a folder
	Opened  *time.Time // when the user last opened it in the editor
}

// DocFilter narrows «Документы»: Scope "" — all, "mine" — uploaded by the user, "shared" — by others, "recent" — those
// the user opened, the last opened first («Недавние»); Exts — the
// extensions of the kind (lower case, none — every office document); Q — a part of the name or words of the text.
type DocFilter struct {
	Scope, Q string
	Exts     []string
}

// Documents is «Документы»: the files with the extensions on the drives the user works with — their «Мой диск», the
// company's (not for a guest), those of the projects they are in, the folders and the files shared with them, newest first.
func (s *Store) Documents(ctx context.Context, userID string, exts []string, f DocFilter, max int) ([]Document, error) {
	if len(f.Exts) > 0 {
		exts = f.Exts
	}
	var viewer *string // whose access the rights of the folders limit: nobody's for an administrator
	if !tasks.IsAdmin(ctx) {
		viewer = &userID
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(f.Q)) + "%"
	// root: the topmost folder above the file shared with the user, when the file is on someone else's «Мой диск»
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, coalesce(st.name, ''), coalesce(u.name, ''),
			coalesce(f.uploaded_by::text, ''), f.size, f.created_at, coalesce(f.folder_id::text, ''), coalesce(p.name, ''),
			`+lockCols+`,
			coalesce((SELECT string_agg(eu.name, ', ' ORDER BY eu.name) FROM users eu WHERE eu.id = ANY(f.editing)
				AND f.editing_at > now() - interval '12 hours'), ''),
			f.drive, coalesce(f.drive_owner::text, ''), coalesce(pr.key, ''), coalesce(pr.name, ''),
			coalesce(root.id, ''), coalesce(ou.name, ''), coalesce(cu.name, u.name, ''),
			f.drive = 'user' AND f.drive_owner <> $1 AND EXISTS (SELECT 1 FROM drive_shares sh WHERE sh.file_id = f.id AND `+mine+`),
			o.opened_at
		FROM files f LEFT JOIN doc_opens o ON o.user_id = $1 AND o.file_id = f.id LEFT JOIN storages st ON st.id = f.storage_id LEFT JOIN users u ON u.id = f.uploaded_by
			LEFT JOIN users cu ON cu.id = f.changed_by `+lockJoin+`
			LEFT JOIN drive_folders p ON p.id = f.folder_id LEFT JOIN projects pr ON f.drive = 'project' AND pr.id = f.drive_owner
			LEFT JOIN users ou ON f.drive = 'user' AND ou.id = f.drive_owner
			LEFT JOIN LATERAL (WITH RECURSIVE up AS (
					SELECT f.folder_id AS id, 1 AS depth
					UNION ALL SELECT d.parent_id, up.depth + 1 FROM drive_folders d JOIN up ON d.id = up.id WHERE d.parent_id IS NOT NULL AND up.depth < 50)
				SELECT up.id::text AS id FROM up JOIN drive_shares sh ON sh.folder_id = up.id WHERE `+mine+`
				ORDER BY up.depth DESC LIMIT 1) root ON f.drive = 'user' AND f.drive_owner <> $1 AND f.folder_id IS NOT NULL
		WHERE f.drive IS NOT NULL AND f.deleted_at IS NULL AND `+live+`
			AND lower(substring(f.name from '\.([^.]+)$')) = ANY($2) AND (f.name ILIKE $3 OR `+contentMatch("6")+`)
			AND ($4 = '' OR ($4 = 'recent' AND o.opened_at IS NOT NULL)
				OR ($4 <> 'recent' AND ($4 = 'mine') = (f.uploaded_by IS NOT DISTINCT FROM $1)))
			AND ((f.drive = 'user' AND (f.drive_owner = $1 OR root.id IS NOT NULL
					OR EXISTS (SELECT 1 FROM drive_shares sh WHERE sh.file_id = f.id AND `+mine+`)))
				OR (f.drive = 'company' AND NOT EXISTS (SELECT 1 FROM users WHERE id = $1 AND guest))
				OR (f.drive = 'project' AND f.drive_owner IN (SELECT project_id FROM project_members WHERE user_id = $1)))
			AND (f.drive = 'user' OR `+fmt.Sprintf(visibleSQL, "f.folder_id", "$7")+`)
		ORDER BY CASE WHEN $4 = 'recent' THEN o.opened_at END DESC, f.created_at DESC LIMIT $5`, userID, exts, like, f.Scope, max, tsQuery(f.Q), viewer)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Document, error) {
		var d Document
		x := &d.File
		var root string
		var file bool
		err := row.Scan(&x.ID, &x.Name, &x.ContentType, &x.StorageName, &x.Uploader, &x.UploaderID, &x.Size, &x.CreatedAt,
			&x.FolderID, &x.Where, &x.Locker, &x.LockerID, &x.LockedAt, &x.Editors,
			&d.Drive, &d.DriveOwner, &d.ProjectKey, &d.ProjectName, &root, &d.Owner, &d.Changer, &file, &d.Opened)
		switch {
		case root != "":
			d.Base = "/disk/sh/" + root
		case file: // shared by itself: no folder of it to open
			d.Base, d.FolderID, d.Where, d.Single = "/disk/sf/"+d.ID, "", "", true
		case d.Drive == "company":
			d.Base = "/disk/company"
		case d.Drive == "project":
			d.Base = "/disk/p/" + d.ProjectKey
		default:
			d.Base, d.Owner = "/disk/my", ""
		}
		return d, err
	})
}

// Opened notes that the user opened the document in the editor: it is first in their «Недавние».
func (s *Store) Opened(ctx context.Context, userID, fileID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO doc_opens (user_id, file_id) VALUES ($1, $2)
		ON CONFLICT (user_id, file_id) DO UPDATE SET opened_at = now()`, userID, fileID)
	return err
}
