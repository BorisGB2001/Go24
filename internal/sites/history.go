package sites

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// «История изменений» of a page (or of the header or the footer of its site): its blocks after each
// change, what was done to a block of which type, by whom and when; the editor previews any of them and brings it
// back. The latest keepRevisions of each page are kept.

const keepRevisions = 100

// Revision is one state of a page in its history: Act was done (site.hist.<act>) to a block of the type BlockType;
// Blocks are filled by Revision only.
type Revision struct {
	ID             int64
	Act, BlockType string
	Author         string
	At             time.Time
	Blocks         []Block
	Count          int // its blocks
}

// whereRev selects the history of a page or an area ($1 the site, $2 the page or the area).
func whereRev(pageID string) string {
	if IsArea(pageID) {
		return `r.site_id::text = $1 AND r.page_id IS NULL AND r.area = $2`
	}
	return `r.site_id::text = $1 AND r.page_id::text = $2`
}

// keepInitial puts the blocks of a page as they are into its history when it has none yet, so the first change can
// be undone too. It runs before the change, in its transaction.
func keepInitial(ctx context.Context, tx pgx.Tx, siteID, pageID string) error {
	var err error
	if IsArea(pageID) {
		_, err = tx.Exec(ctx, `INSERT INTO site_revisions (site_id, area, act, blocks, created_at)
			SELECT s.id, $2, 'initial', s.`+pageID+`, s.updated_at FROM sites s
			WHERE s.id::text = $1 AND NOT EXISTS (SELECT 1 FROM site_revisions r WHERE `+whereRev(pageID)+`)`, siteID, pageID)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO site_revisions (site_id, page_id, act, blocks, created_by, created_at)
			SELECT p.site_id, p.id, 'initial', p.blocks, p.updated_by, p.updated_at FROM site_pages p
			WHERE p.site_id::text = $1 AND p.id::text = $2 AND NOT EXISTS (SELECT 1 FROM site_revisions r WHERE `+whereRev(pageID)+`)`, siteID, pageID)
	}
	return err
}

// record keeps a new state of a page in its history and forgets the states past keepRevisions.
func record(ctx context.Context, tx pgx.Tx, userID, siteID, pageID, act, typ string, raw []byte) error {
	page, area := any(pageID), ""
	if IsArea(pageID) {
		page, area = nil, pageID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO site_revisions (site_id, page_id, area, act, block_type, blocks, created_by)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, nullif($7, '')::uuid)`, siteID, page, area, act, typ, raw, userID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM site_revisions r WHERE `+whereRev(pageID)+` AND r.id <= (SELECT r.id FROM site_revisions r
		WHERE `+whereRev(pageID)+` ORDER BY r.id DESC OFFSET $3 LIMIT 1)`, siteID, pageID, keepRevisions)
	return err
}

// Revisions are the history of a page or an area ("header", "footer"), the latest first.
func (s *Store) Revisions(ctx context.Context, siteID, pageID string) ([]Revision, error) {
	if _, err := s.Site(ctx, siteID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT r.id, r.act, r.block_type, coalesce(u.name, ''), r.created_at, jsonb_array_length(r.blocks)
		FROM site_revisions r LEFT JOIN users u ON u.id = r.created_by WHERE `+whereRev(pageID)+` ORDER BY r.id DESC`, siteID, pageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Revision, error) {
		var v Revision
		return v, r.Scan(&v.ID, &v.Act, &v.BlockType, &v.Author, &v.At, &v.Count)
	})
}

// Revision is one state of a page or an area with its blocks.
func (s *Store) Revision(ctx context.Context, siteID, pageID string, id int64) (Revision, error) {
	if _, err := s.Site(ctx, siteID); err != nil {
		return Revision{}, err
	}
	var v Revision
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT r.id, r.act, r.block_type, coalesce(u.name, ''), r.created_at, r.blocks
		FROM site_revisions r LEFT JOIN users u ON u.id = r.created_by WHERE `+whereRev(pageID)+` AND r.id = $3`, siteID, pageID, id).
		Scan(&v.ID, &v.Act, &v.BlockType, &v.Author, &v.At, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(raw, &v.Blocks)
	v.Count = len(v.Blocks)
	return v, err
}

// Revert brings a page or an area back to a state of its history; that is a change of its own, so it can be undone.
func (s *Store) Revert(ctx context.Context, userID, siteID, pageID string, id int64) error {
	v, err := s.Revision(ctx, siteID, pageID, id)
	if err != nil {
		return err
	}
	return s.editBlocks(ctx, userID, siteID, pageID, "revert", func([]Block) ([]Block, string, error) {
		return v.Blocks, "", nil
	})
}

// blocksOf are the blocks of a page or an area as they are now.
func (s *Store) blocksOf(ctx context.Context, siteID, pageID string) ([]Block, error) {
	var out []Block
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = loadBlocks(ctx, tx, siteID, pageID)
		return err
	})
	return out, err
}
