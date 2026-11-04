package sign

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// BlockKinds are the blocks the sender puts on the pages of the document: the signer's handwritten
// signature, their name, the day they signed, the company's stamp, a text of the sender's and a field the signer fills
// in (its caption — the block's text).
var BlockKinds = []string{"signature", "name", "date", "stamp", "text", "field"}

// BlocksMax: no more blocks than this on a document.
const BlocksMax = 100

var (
	ErrBlocks       = i18n.E("sign.err.blocks")
	ErrBlocksSigned = i18n.E("sign.err.blocks_signed")
	ErrFields       = i18n.E("sign.err.fields")
)

// Block is a block on a page of the document: of a signer (PartyID), from page 1, the box — in fractions of the
// page's size from its top left corner.
type Block struct {
	ID, PartyID, Kind string
	Page              int
	X, Y, W, H        float64
	Text              string // of a text block; the caption of a field
	Required          bool   // a field: the signer cannot sign leaving it empty
	Value             string // a field: what the signer has filled in
}

// Valid: a known kind, the box within the page.
func (b Block) Valid() bool {
	return slices.Contains(BlockKinds, b.Kind) && b.Page >= 1 && b.X >= 0 && b.Y >= 0 && b.W > 0 && b.H > 0 &&
		b.X < 1 && b.Y < 1 && b.X+b.W <= 1.0001 && b.Y+b.H <= 1.0001 && len([]rune(b.Text)) <= 200 &&
		(b.Kind != "field" || strings.TrimSpace(b.Text) != "")
}

// Blocks are the blocks of the document, page by page.
func (s *Store) Blocks(ctx context.Context, docID string) ([]Block, error) {
	rows, err := s.db.Query(ctx, `SELECT id, party_id, kind, page, x, y, w, h, text, required, value FROM sign_blocks
		WHERE doc_id::text = $1 ORDER BY page, y, x`, docID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Block, error) {
		var b Block
		var x, y, w, h float32
		err := r.Scan(&b.ID, &b.PartyID, &b.Kind, &b.Page, &x, &y, &w, &h, &b.Text, &b.Required, &b.Value)
		b.X, b.Y, b.W, b.H = float64(x), float64(y), float64(w), float64(h)
		return b, err
	})
}

// SetBlocks puts the blocks on the document instead of those it had — while it is being signed and none has
// signed it yet (an approval does not count); a block is of a signer of the document, not of one who approves.
func (s *Store) SetBlocks(ctx context.Context, docID string, blocks []Block) error {
	if len(blocks) > BlocksMax {
		return ErrBlocks
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM sign_docs WHERE id::text = $1 FOR UPDATE`, docID).Scan(&status); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	var signed bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sign_parties WHERE doc_id::text = $1 AND status = 'signed' AND role = '')`,
		docID).Scan(&signed); err != nil {
		return err
	}
	if status != "signing" || signed {
		return ErrBlocksSigned
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM sign_parties WHERE doc_id::text = $1 AND role = ''`, docID)
	if err != nil {
		return err
	}
	signers, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, b := range blocks {
		if !b.Valid() || !slices.Contains(signers, b.PartyID) {
			return ErrBlocks
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM sign_blocks WHERE doc_id::text = $1`, docID); err != nil {
		return err
	}
	for _, b := range blocks {
		if b.Kind != "text" && b.Kind != "field" {
			b.Text = ""
		}
		b.Required = b.Required && b.Kind == "field"
		if _, err := tx.Exec(ctx, `INSERT INTO sign_blocks (doc_id, party_id, kind, page, x, y, w, h, text, required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, docID, b.PartyID, b.Kind, b.Page, b.X, b.Y, min(b.W, 1-b.X), min(b.H, 1-b.Y),
			strings.TrimSpace(b.Text), b.Required); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Fields are the fields of the signer on the document, page by page.
func Fields(blocks []Block, partyID string) []Block {
	var out []Block
	for _, b := range blocks {
		if b.Kind == "field" && b.PartyID == partyID {
			out = append(out, b)
		}
	}
	return out
}

// FillFields keeps what the signer has filled in their fields (by the blocks' ids; a field not given — empty), before
// they sign: a required one left empty — ErrFields, nothing is kept.
func (s *Store) FillFields(ctx context.Context, docID, partyID string, values map[string]string) error {
	blocks, err := s.Blocks(ctx, docID)
	if err != nil {
		return err
	}
	fields := Fields(blocks, partyID)
	for _, f := range fields {
		v := strings.TrimSpace(values[f.ID])
		if len([]rune(v)) > 200 || f.Required && v == "" {
			return ErrFields
		}
	}
	if len(fields) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, f := range fields {
		if _, err := tx.Exec(ctx, `UPDATE sign_blocks SET value = $2 WHERE id::text = $1`, f.ID, strings.TrimSpace(values[f.ID])); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
