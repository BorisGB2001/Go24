package mail

import (
	"context"
	"slices"
)

// MaxFavorites bounds the user's «Избранное».
const MaxFavorites = 50

// Favorites are the refs of the user's «Избранное» (folder ids, "cat:ID", "search:ID", the virtual folders), in order.
// The first time, defaults are put there (Outlook's: Inbox, Sent, Drafts of the first mailbox).
func (s *Store) Favorites(ctx context.Context, userID string, defaults ...string) ([]string, error) {
	list, err := s.favorites(ctx, userID)
	if err != nil || len(list) > 0 {
		return list, err
	}
	var made bool
	if err := s.db.QueryRow(ctx, `SELECT mail_favorites_made FROM users WHERE id::text = $1`, userID).Scan(&made); err != nil || made {
		return list, err
	}
	if len(defaults) == 0 { // no mailbox yet: the defaults wait for one
		return list, nil
	}
	if err := s.SetFavorites(ctx, userID, defaults); err != nil {
		return nil, err
	}
	return s.favorites(ctx, userID)
}

func (s *Store) favorites(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT ref FROM mail_favorites WHERE user_id::text = $1 ORDER BY pos, ref`, userID)
	if err != nil {
		return nil, err
	}
	var list []string
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		list = append(list, ref)
	}
	return list, rows.Err()
}

// SetFavorites replaces the user's «Избранное» with refs in this order (repeats and empty ones dropped).
func (s *Store) SetFavorites(ctx context.Context, userID string, refs []string) error {
	var keep []string
	for _, r := range refs {
		if r != "" && len(r) <= 100 && !slices.Contains(keep, r) && len(keep) < MaxFavorites {
			keep = append(keep, r)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM mail_favorites WHERE user_id::text = $1`, userID); err != nil {
		return err
	}
	for i, r := range keep {
		if _, err := tx.Exec(ctx, `INSERT INTO mail_favorites (user_id, ref, pos) VALUES ($1, $2, $3)`, userID, r, i); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET mail_favorites_made = true WHERE id::text = $1`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Favorite adds ref to the end of the user's «Избранное» (on = true) or takes it out.
func (s *Store) Favorite(ctx context.Context, userID, ref string, on bool) error {
	list, err := s.favorites(ctx, userID)
	if err != nil {
		return err
	}
	list = slices.DeleteFunc(list, func(r string) bool { return r == ref })
	if on {
		list = append(list, ref)
	}
	return s.SetFavorites(ctx, userID, list)
}

// MoveFavorite moves ref one place up (by < 0) or down in the user's «Избранное».
func (s *Store) MoveFavorite(ctx context.Context, userID, ref string, by int) error {
	list, err := s.favorites(ctx, userID)
	if err != nil {
		return err
	}
	i := slices.Index(list, ref)
	if i < 0 {
		return ErrNotFound
	}
	j := i + 1
	if by < 0 {
		j = i - 1
	}
	if j < 0 || j >= len(list) {
		return nil
	}
	list[i], list[j] = list[j], list[i]
	return s.SetFavorites(ctx, userID, list)
}
