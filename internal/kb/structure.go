package kb

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Target is a place a page may be moved to: the top of a space (PageID "") or under a page of it.
type Target struct {
	SpaceID, SpaceName string
	PageID, Title      string
	Depth              int
}

// Targets are the places the page may be moved to: the spaces the user writes in, and their pages but the page itself
// and those under it.
func (s *Store) Targets(ctx context.Context, userID, pageID string) ([]Target, error) {
	spaces, err := s.Spaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []Target
	var walk func(sp Space, list []*Page, depth int)
	walk = func(sp Space, list []*Page, depth int) {
		for _, p := range list {
			if p.ID == pageID {
				continue
			}
			out = append(out, Target{SpaceID: sp.ID, SpaceName: sp.Name, PageID: p.ID, Title: p.Title, Depth: depth})
			walk(sp, p.Children, depth+1)
		}
	}
	for _, sp := range spaces {
		if !sp.CanEdit {
			continue
		}
		tree, err := s.Tree(ctx, sp.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Target{SpaceID: sp.ID, SpaceName: sp.Name})
		walk(sp, tree, 1)
	}
	return out, nil
}

// MovePage puts a page with the pages under it into a space (it may be another one the user writes in), at its top
// (parentID "") or under one of its pages, last there.
func (s *Store) MovePage(ctx context.Context, userID, id, spaceID, parentID string) error {
	_, from, err := s.Page(ctx, userID, id)
	if err != nil {
		return err
	}
	to, err := s.Space(ctx, userID, spaceID)
	if err != nil {
		return err
	}
	if !from.CanEdit || !to.CanEdit {
		return ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var parent *string
	if parentID != "" {
		var inside bool // the new parent is the page or under it
		err := tx.QueryRow(ctx, `WITH RECURSIVE down AS (
				SELECT id, 0 AS depth FROM kb_pages WHERE id = $1
				UNION ALL SELECT g.id, down.depth + 1 FROM kb_pages g JOIN down ON g.parent_id = down.id WHERE down.depth < 50)
			SELECT EXISTS (SELECT 1 FROM down WHERE id::text = $2) FROM kb_pages WHERE id::text = $2 AND space_id = $3`,
			id, parentID, spaceID).Scan(&inside)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if inside {
			return ErrForbidden
		}
		parent = &parentID
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_pages SET parent_id = $2, space_id = $3,
		sort = (SELECT coalesce(max(sort), 0) + 1 FROM kb_pages WHERE space_id = $3) WHERE id = $1`, id, parent, spaceID); err != nil {
		return err
	}
	if from.ID != spaceID { // the pages under it go along
		if _, err := tx.Exec(ctx, `WITH RECURSIVE down AS (
				SELECT id, 0 AS depth FROM kb_pages WHERE parent_id = $1
				UNION ALL SELECT g.id, down.depth + 1 FROM kb_pages g JOIN down ON g.parent_id = down.id WHERE down.depth < 50)
			UPDATE kb_pages SET space_id = $2 WHERE id IN (SELECT id FROM down)`, id, spaceID); err != nil {
			return err
		}
		// stars and watches of the pages move with them
		if _, err := tx.Exec(ctx, `UPDATE kb_marks m SET space_id = $1 FROM kb_pages g WHERE g.id = m.page_id AND g.space_id = $1
			AND m.space_id <> $1`, spaceID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// PlacePage puts a page with the pages under it into its space under parentID ("" — at the top) before the page
// beforeID among the pages there ("" — last): a page dragged in the tree.
func (s *Store) PlacePage(ctx context.Context, userID, id, parentID, beforeID string) error {
	_, sp, err := s.Page(ctx, userID, id)
	if err != nil {
		return err
	}
	if !sp.CanEdit {
		return ErrForbidden
	}
	if beforeID == id {
		beforeID = ""
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var parent *string
	if parentID != "" {
		var inside bool // the new parent is the page or under it
		err := tx.QueryRow(ctx, `SELECT EXISTS (`+subtreeSQL+` WHERE id::text = $2) FROM kb_pages WHERE id::text = $2 AND space_id = $3`,
			id, parentID, sp.ID).Scan(&inside)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if inside {
			return ErrForbidden
		}
		parent = &parentID
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM kb_pages WHERE space_id = $1 AND parent_id::text IS NOT DISTINCT FROM $2 AND id <> $3
		ORDER BY sort, title`, sp.ID, parent, id)
	if err != nil {
		return err
	}
	siblings, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(siblings)+1)
	for _, sib := range siblings {
		if sib == beforeID {
			ids = append(ids, id)
		}
		ids = append(ids, sib)
	}
	if len(ids) == len(siblings) { // no such page there: last
		ids = append(ids, id)
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_pages SET parent_id = $2 WHERE id = $1`, id, parent); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_pages g SET sort = o.n FROM unnest($1::uuid[]) WITH ORDINALITY o(id, n) WHERE g.id = o.id`, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MoveUp puts a page before the one above it among the pages of its parent (up false — after the one below).
func (s *Store) MoveUp(ctx context.Context, userID, id string, up bool) error {
	_, sp, err := s.Page(ctx, userID, id)
	if err != nil {
		return err
	}
	if !sp.CanEdit {
		return ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id::text FROM kb_pages WHERE space_id = $1
		AND parent_id IS NOT DISTINCT FROM (SELECT parent_id FROM kb_pages WHERE id = $2) ORDER BY sort, title`, sp.ID, id)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	i := 0
	for i < len(ids) && ids[i] != id {
		i++
	}
	j := i + 1
	if up {
		j = i - 1
	}
	if j < 0 || j >= len(ids) {
		return nil // already first (last)
	}
	ids[i], ids[j] = ids[j], ids[i]
	if _, err := tx.Exec(ctx, `UPDATE kb_pages g SET sort = o.n FROM unnest($1::uuid[]) WITH ORDINALITY o(id, n) WHERE g.id = o.id`, ids); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// CopyPage makes a copy of a page (its text, without the pages under it) right after it, titled title.
func (s *Store) CopyPage(ctx context.Context, userID, id, title string) (string, error) {
	p, sp, err := s.Page(ctx, userID, id)
	if err != nil {
		return "", err
	}
	if !sp.CanEdit {
		return "", ErrForbidden
	}
	if title, err = checkPage(title, p.Body); err != nil {
		return "", err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var sort int
	if err := tx.QueryRow(ctx, `SELECT sort FROM kb_pages WHERE id = $1`, id).Scan(&sort); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE kb_pages SET sort = sort + 1 WHERE space_id = $1 AND sort > $2`, sp.ID, sort); err != nil {
		return "", err
	}
	var cp string
	err = tx.QueryRow(ctx, `INSERT INTO kb_pages (space_id, parent_id, title, body, sort, created_by, updated_by)
		SELECT space_id, parent_id, $2, body, sort + 1, $3, $3 FROM kb_pages WHERE id = $1 RETURNING id`, id, title, userID).Scan(&cp)
	if err != nil {
		return "", err
	}
	return cp, tx.Commit(ctx)
}

// Path is the way down the tree to the page: its parents from the top and the page itself (nil — not in the tree).
func Path(tree []*Page, id string) []*Page {
	for _, p := range tree {
		if p.ID == id {
			return []*Page{p}
		}
		if down := Path(p.Children, id); down != nil {
			return append([]*Page{p}, down...)
		}
	}
	return nil
}

// Descendants counts the pages under the page in the tree, of any depth.
func Descendants(p *Page) int {
	n := 0
	for _, c := range p.Children {
		n += 1 + Descendants(c)
	}
	return n
}
