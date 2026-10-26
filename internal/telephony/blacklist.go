package telephony

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Blocked is a number of the black list («Чёрный список» of the telephony): its calls on the external lines
// hear «busy», ring nobody and make no lead.
type Blocked struct {
	ID, Number, Comment string
	ByName              string // who added it ("" — gone)
	At                  time.Time
}

// BlackKey is how numbers of the black list are matched: the last 10 digits (+7…, 7… and 8… are one number); "" — no
// digits.
func BlackKey(number string) string {
	var b strings.Builder
	for _, r := range number {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	k := b.String()
	if len(k) > 10 {
		k = k[len(k)-10:]
	}
	return k
}

// Blacklist is the black list, the newest first.
func (s *Store) Blacklist(ctx context.Context) ([]Blocked, error) {
	rows, err := s.db.Query(ctx, `SELECT b.id::text, b.number, b.comment, coalesce(nullif(u.name, ''), u.email, ''), b.created_at
		FROM telephony_blacklist b LEFT JOIN users u ON u.id = b.created_by ORDER BY b.created_at DESC, b.number`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Blocked, error) {
		var b Blocked
		return b, r.Scan(&b.ID, &b.Number, &b.Comment, &b.ByName, &b.At)
	})
}

// BlackKeys are the keys of the black list (BlackKey).
func (s *Store) BlackKeys(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT key FROM telephony_blacklist`)
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out, err
}

// Block puts a number on the black list (one already there gets the new comment).
func (s *Store) Block(ctx context.Context, userID, number, comment string) error {
	number = Dialable(number)
	key := BlackKey(number)
	if len(key) < 3 {
		return i18n.E("err.black_number")
	}
	comment = strings.TrimSpace(comment)
	if len([]rune(comment)) > 255 {
		comment = string([]rune(comment)[:255])
	}
	_, err := s.db.Exec(ctx, `INSERT INTO telephony_blacklist (key, number, comment, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (key) DO UPDATE SET comment = CASE WHEN EXCLUDED.comment <> '' THEN EXCLUDED.comment ELSE telephony_blacklist.comment END`,
		key, number, comment, userID)
	return err
}

// Unblock takes a number off the black list.
func (s *Store) Unblock(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM telephony_blacklist WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// RenderBlacklist is the dialplan context of the black list: an extension per key, which the external lines' contexts
// look for (RenderTrunks); sorted, so the same list is the same file.
func RenderBlacklist(keys map[string]bool) string {
	var d strings.Builder
	d.WriteString("\n; the black list (Settings → Telephony → Black list)\n[go24-black]\n")
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		fmt.Fprintf(&d, "exten => %s,1,Return()\n", k)
	}
	return d.String()
}
