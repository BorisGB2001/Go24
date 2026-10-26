package drive

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Sharing is «Общий доступ» of a file of a drive, as a file's page has it: who may see it besides the drive's
// own people, and by what.
type Sharing struct {
	Shares  []Share   // the file shared by itself (a file of «Мой диск»)
	Link    *Link     // the file's public link
	Folders []Reached // the folders above it, from the top, that open it: shares, a public link, «Права доступа»
}

// Reached is a folder above a file that opens it to someone.
type Reached struct {
	ID, Name string
	Shares   []Share // of «Мой диск»: each opens all the folder holds
	Link     *Link
	Rights   []Right // of the company's drive or a project's: only the nearest folder with rights of its own decides
}

// Sharing is «Общий доступ» of the file; whether the user may read it is the caller's to know.
func (s *Store) Sharing(ctx context.Context, fileID string) (Sharing, error) {
	var sh Sharing
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT p.id, p.parent_id, p.name, 1 AS depth FROM files f JOIN drive_folders p ON p.id = f.folder_id WHERE f.id::text = $1
			UNION ALL SELECT p.id, p.parent_id, p.name, up.depth + 1 FROM drive_folders p JOIN up ON p.id = up.parent_id WHERE up.depth < 50)
		SELECT id::text, name FROM up ORDER BY depth DESC`, fileID)
	if err != nil {
		return sh, err
	}
	trail, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Reached, error) {
		var f Reached
		return f, row.Scan(&f.ID, &f.Name)
	})
	if err != nil {
		return sh, err
	}
	ids := []string{fileID}
	for _, f := range trail {
		ids = append(ids, f.ID)
	}
	shares, err := s.Shares(ctx, ids)
	if err != nil {
		return sh, err
	}
	links, err := s.Links(ctx, ids)
	if err != nil {
		return sh, err
	}
	rights, err := s.FolderRights(ctx, ids[1:])
	if err != nil {
		return sh, err
	}
	sh.Shares = shares[fileID]
	if l, ok := links[fileID]; ok {
		sh.Link = &l
	}
	ruled := false // the folders above the nearest with rights of its own do not decide
	for i := len(trail) - 1; i >= 0; i-- {
		f := &trail[i]
		f.Shares = shares[f.ID]
		if l, ok := links[f.ID]; ok {
			f.Link = &l
		}
		if !ruled && len(rights[f.ID]) > 0 {
			f.Rights, ruled = rights[f.ID], true
		}
	}
	sh.Folders = slices.DeleteFunc(trail, func(f Reached) bool { return len(f.Shares) == 0 && f.Link == nil && len(f.Rights) == 0 })
	return sh, nil
}

// Ruled: the folders' «Права доступа» decide who of the drive sees the file (else every one of it does).
func (sh Sharing) Ruled() bool {
	return slices.ContainsFunc(sh.Folders, func(f Reached) bool { return len(f.Rights) > 0 })
}
