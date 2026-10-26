package drive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// The documents of «Общий диск» business processes are about Диск: a template of files starts on a
// document added to the drive or changed there (the migration's trigger keeps drive_events), or by hand from the
// file's card.

// BPFile is a document of «Общий диск»: its name and who uploaded it; ErrNotFound — it is not on the drive any more
// (deleted, moved off it).
func (s *Store) BPFile(ctx context.Context, id string) (name, uploader string, err error) {
	err = s.db.QueryRow(ctx, `SELECT name, coalesce(uploaded_by::text, '') FROM files
		WHERE id::text = $1 AND drive = 'company' AND deleted_at IS NULL`, id).Scan(&name, &uploader)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return name, uploader, err
}

// BPRight: the user may do op on a document of «Общий диск» — "read" it (it lies in a folder open to them), "write"
// (change it: its uploader or one who manages the drive).
func (s *Store) BPRight(ctx context.Context, userID, op, id string) error {
	if _, _, err := s.BPFile(ctx, id); err != nil {
		return err
	}
	d, err := s.Open(ctx, userID, "company")
	if err != nil {
		return err
	}
	if _, err := s.FileOn(ctx, d, id); err != nil {
		return err
	}
	if op == "read" {
		return nil
	}
	if ok, err := s.MayChange(ctx, userID, d, id); err != nil || !ok {
		return orForbidden(err)
	}
	return nil
}
