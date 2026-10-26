package kb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/tasks"
)

// «Корзина» of the knowledge base: a deleted page goes to the trash with its subpages, a deleted base
// with all its pages — the rows stay, only «deleted_at» hides them (the kb_pages and kb_spaces views). A page comes
// back to its place (to the top of the base if the page above it is in the trash too), a base — as it was. Pages
// are restored or deleted for good by those who write in the base, bases — by those who manage them; what is kept
// longer than the company setting of the trash goes for good by itself.

// TrashItem is a deleted base (Page "") or page — the one the user deleted; Pages counts the pages that went with it
// (a page's subpages, a base's pages).
type TrashItem struct {
	SpaceID, SpaceName string
	PageID, Title      string
	DeletedBy          string
	DeletedAt          time.Time
	Pages              int
	PurgeAt            *time.Time // when it goes for good (nil — kept)
}

// subtreeSQL selects the live page $1 and its live subpages of any depth.
const subtreeSQL = `WITH RECURSIVE sub AS (SELECT id, 0 AS depth FROM kb_pages WHERE id = $1
	UNION ALL SELECT g.id, sub.depth + 1 FROM kb_pages g JOIN sub ON g.parent_id = sub.id WHERE sub.depth < 50) SELECT id FROM sub`

// DeleteSpace moves a space with its pages to the trash (its creator, project managers, administrators).
func (s *Store) DeleteSpace(ctx context.Context, userID, id string) error {
	if _, err := s.managed(ctx, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE kb_spaces_all SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, id, userID)
	return err
}

// DeletePage moves a page with the pages under it to the trash.
func (s *Store) DeletePage(ctx context.Context, userID, id string) (spaceID string, err error) {
	p, sp, err := s.Page(ctx, userID, id)
	if err != nil {
		return "", err
	}
	if !sp.CanEdit {
		return "", ErrForbidden
	}
	_ = s.tell(ctx, userID, sp.ID, id, "kb_page_trashed", p.Title) // while the page is there to find who watches it
	_, err = s.db.Exec(ctx, `UPDATE kb_pages_all SET deleted_at = now(), deleted_by = $2, deleted_root = $1
		WHERE id IN (`+subtreeSQL+`)`, id, userID)
	return sp.ID, err
}

// Trash lists what the user may restore: the deleted bases they manage and the deleted pages of the bases they
// write in, the latest first. days is the company setting of the trash (0 — kept until deleted by hand).
func (s *Store) Trash(ctx context.Context, userID string, days int) ([]TrashItem, error) {
	spaces, err := s.deletedSpaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	var managed []string
	for _, sp := range spaces {
		if sp.CanManage {
			managed = append(managed, sp.ID)
		}
	}
	live, err := s.Spaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	var editable []string
	for _, sp := range live {
		if sp.CanEdit {
			editable = append(editable, sp.ID)
		}
	}
	rows, err := s.db.Query(ctx, `SELECT s.id::text, s.name, '', s.name, coalesce(u.name, ''), s.deleted_at,
			(SELECT count(*) FROM kb_pages g WHERE g.space_id = s.id)::int
		FROM kb_spaces_all s LEFT JOIN users u ON u.id = s.deleted_by WHERE s.id::text = ANY($1) AND s.deleted_at IS NOT NULL
		UNION ALL
		SELECT s.id::text, s.name, g.id::text, g.title, coalesce(u.name, ''), g.deleted_at,
			(SELECT count(*) FROM kb_pages_all c WHERE c.deleted_root = g.id AND c.deleted_at IS NOT NULL AND c.id <> g.id)::int
		FROM kb_pages_all g JOIN kb_spaces s ON s.id = g.space_id LEFT JOIN users u ON u.id = g.deleted_by
		WHERE s.id::text = ANY($2) AND g.deleted_at IS NOT NULL AND g.deleted_root = g.id
		ORDER BY 6 DESC LIMIT 500`, managed, editable)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (TrashItem, error) {
		var it TrashItem
		err := r.Scan(&it.SpaceID, &it.SpaceName, &it.PageID, &it.Title, &it.DeletedBy, &it.DeletedAt, &it.Pages)
		return it, err
	})
	if err != nil {
		return nil, err
	}
	if days > 0 {
		for i := range list {
			at := list[i].DeletedAt.AddDate(0, 0, days)
			list[i].PurgeAt = &at
		}
	}
	return list, nil
}

// deletedSpaces are the spaces in the trash with the user's rights in them, as if they were live.
func (s *Store) deletedSpaces(ctx context.Context, userID string) ([]Space, error) {
	sql := strings.Replace(spaceSQL, "FROM kb_spaces s", "FROM kb_spaces_all s", 1)
	return scanSpaces(s.db.Query(ctx, sql+` AND s.deleted_at IS NOT NULL`, userID, tasks.IsAdmin(ctx)))
}

// deletedSpace is one space in the trash the user manages.
func (s *Store) deletedSpace(ctx context.Context, userID, id string) error {
	list, err := s.deletedSpaces(ctx, userID)
	if err != nil {
		return err
	}
	for _, sp := range list {
		if sp.ID == id {
			if !sp.CanManage {
				return ErrForbidden
			}
			return nil
		}
	}
	return ErrNotFound
}

// deletedPage checks that the page is one the user deleted (not a subpage that went with it) in a live space they
// write in.
func (s *Store) deletedPage(ctx context.Context, userID, id string) error {
	var spaceID string
	err := s.db.QueryRow(ctx, `SELECT space_id::text FROM kb_pages_all WHERE id::text = $1 AND deleted_at IS NOT NULL AND deleted_root = id`,
		id).Scan(&spaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	sp, err := s.Space(ctx, userID, spaceID)
	if err == nil && !sp.CanEdit {
		err = ErrForbidden
	}
	return err
}

// RestoreSpace brings a deleted space back with its pages.
func (s *Store) RestoreSpace(ctx context.Context, userID, id string) error {
	if err := s.deletedSpace(ctx, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE kb_spaces_all SET deleted_at = NULL, deleted_by = NULL WHERE id = $1`, id)
	return err
}

// RestorePage brings a deleted page back with its subpages; a page whose parent is in the trash comes to the top.
func (s *Store) RestorePage(ctx context.Context, userID, id string) (spaceID string, err error) {
	if err := s.deletedPage(ctx, userID, id); err != nil {
		return "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE kb_pages_all SET deleted_at = NULL, deleted_by = NULL, deleted_root = NULL
		WHERE deleted_root = $1 AND deleted_at IS NOT NULL`, id); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `UPDATE kb_pages_all g SET parent_id = CASE WHEN EXISTS (SELECT 1 FROM kb_pages_all p
			WHERE p.id = g.parent_id AND p.deleted_at IS NOT NULL) THEN NULL ELSE g.parent_id END
		WHERE g.id = $1 RETURNING g.space_id::text`, id).Scan(&spaceID); err != nil {
		return "", err
	}
	return spaceID, tx.Commit(ctx)
}

// PurgeSpace deletes a space in the trash for good, with its pages and their pictures and files.
func (s *Store) PurgeSpace(ctx context.Context, userID, id string) error {
	if err := s.deletedSpace(ctx, userID, id); err != nil {
		return err
	}
	return purgeSpace(ctx, s.db, id)
}

// PurgePage deletes a page in the trash for good, with its subpages.
func (s *Store) PurgePage(ctx context.Context, userID, id string) error {
	if err := s.deletedPage(ctx, userID, id); err != nil {
		return err
	}
	return purgePage(ctx, s.db, id)
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func purgeSpace(ctx context.Context, db execer, id string) error {
	if _, err := db.Exec(ctx, `DELETE FROM files WHERE kb_space_id = $1`, id); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `DELETE FROM kb_spaces_all WHERE id = $1`, id)
	return err
}

// purgePage deletes the pages that went to the trash with the page; a page under them deleted on its own earlier
// keeps its own place in the trash (it is lifted to the top, not cascaded).
func purgePage(ctx context.Context, db execer, id string) error {
	if _, err := db.Exec(ctx, `UPDATE kb_pages_all SET parent_id = NULL WHERE deleted_root IS DISTINCT FROM $1
		AND parent_id IN (SELECT id FROM kb_pages_all WHERE deleted_root = $1 AND deleted_at IS NOT NULL)`, id); err != nil {
		return err
	}
	_, err := db.Exec(ctx, `DELETE FROM kb_pages_all WHERE deleted_root = $1 AND deleted_at IS NOT NULL`, id)
	return err
}

// PurgeExpired deletes for good the pages and spaces that went to the trash before the time.
func (s *Store) PurgeExpired(ctx context.Context, before time.Time) (int, error) {
	rows, err := s.db.Query(ctx, `SELECT 'p', id::text FROM kb_pages_all WHERE deleted_at < $1 AND deleted_root = id
		UNION ALL SELECT 's', id::text FROM kb_spaces_all WHERE deleted_at < $1`, before)
	if err != nil {
		return 0, err
	}
	type item struct{ Kind, ID string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[item])
	if err != nil {
		return 0, err
	}
	for _, it := range list {
		purge := purgePage
		if it.Kind == "s" {
			purge = purgeSpace
		}
		if err := purge(ctx, s.db, it.ID); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}
