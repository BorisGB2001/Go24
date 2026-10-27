package drive

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Thumb is the kept thumbnail of the file's state objectKey: found is false when none is kept for that state, data
// is nil when none can be made of it (tried again after a day: the document server may have been away).
func (s *Store) Thumb(ctx context.Context, fileID, objectKey string) (data []byte, found bool, err error) {
	err = s.db.QueryRow(ctx, `SELECT data FROM file_thumbs WHERE file_id::text = $1 AND object_key = $2
		AND (data IS NOT NULL OR made_at > now() - interval '1 day')`, fileID, objectKey).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// SetThumb keeps the thumbnail of the file's state objectKey (nil — none can be made), in place of a former state's.
func (s *Store) SetThumb(ctx context.Context, fileID, objectKey string, data []byte) error {
	_, err := s.db.Exec(ctx, `INSERT INTO file_thumbs (file_id, object_key, data) VALUES ($1, $2, $3)
		ON CONFLICT (file_id) DO UPDATE SET object_key = EXCLUDED.object_key, data = EXCLUDED.data, made_at = now()`, fileID, objectKey, data)
	return err
}
