package drive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"

	"go24/internal/i18n"
)

// «Доски»: a board is a file of the drive named *.board — an Excalidraw scene (JSON), drawn in the browser.
const (
	BoardExt  = ".board"
	BoardType = "application/vnd.excalidraw+json"
)

// ErrBoardChanged: someone saved the board after the state the editor started from.
var ErrBoardChanged = i18n.E("err.board_changed")

// IsBoard reports whether the file is a board.
func IsBoard(name string) bool { return strings.EqualFold(path.Ext(name), BoardExt) }

// BoardStamp names a stored state of a board (by its object key): a save says which state it changes.
func BoardStamp(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8])
}

// SaveBoard makes the bytes stored under v.Key the board's current state, if the board is still at the state
// stamp — otherwise ErrBoardChanged, and the editor merges the newer state first. The autosaves of one author
// within ten minutes are one version: the state they replace is not kept among the versions.
func (s *Store) SaveBoard(ctx context.Context, userID string, d Drive, id, stamp string, v NewVersion) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.changeFile(ctx, tx, userID, d, id); err != nil {
		return err
	}
	var key string
	var same bool
	if err := tx.QueryRow(ctx, `SELECT object_key, coalesce(changed_by, uploaded_by)::text = $2 AND created_at > now() - interval '10 minutes'
		FROM files WHERE id::text = $1 FOR UPDATE`, id, userID).Scan(&key, &same); err != nil {
		return err
	}
	if BoardStamp(key) != stamp {
		return ErrBoardChanged
	}
	if !same {
		if err := s.keepVersion(ctx, tx, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE files SET object_key = $2, storage_id = $3, size = $4, content_type = $5,
		changed_by = $6, created_at = now() WHERE id::text = $1`, id, v.Key, nullable(v.StorageID), v.Size, v.ContentType, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
