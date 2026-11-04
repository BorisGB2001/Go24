package company

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// «Интересы» of the profile: tags an employee puts on themselves (football, photography…); a tag leads to the
// colleagues with the same interest, the popular ones are offered while typing.

// MaxInterests a person may have; MaxInterestLen — runes of one.
const (
	MaxInterests   = 30
	MaxInterestLen = 50
)

// Interest is a tag with the number of employees who have it.
type Interest struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// NormInterests cleans typed interests: the «#» and spaces around go, inner spaces are collapsed, the same interest in
// another case is kept once (the first spelling), too long ones are cut, at most MaxInterests.
func NormInterests(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.Join(strings.Fields(strings.TrimLeft(strings.TrimSpace(t), "#")), " ")
		t = strings.TrimFunc(t, func(r rune) bool { return unicode.IsPunct(r) && r != '+' && r != '#' || unicode.IsSpace(r) })
		if utf8.RuneCountInString(t) > MaxInterestLen {
			t = strings.TrimSpace(string([]rune(t)[:MaxInterestLen]))
		}
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
		if len(out) == MaxInterests {
			break
		}
	}
	return out
}

// SplitInterests: interests typed in one line, separated by commas or as hashtags («#кино #футбол»; «C#» stays whole).
func SplitInterests(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		start := 0
		for i, r := range part {
			if r == '#' && i > 0 && unicode.IsSpace(rune(part[i-1])) {
				out = append(out, part[start:i])
				start = i
			}
		}
		out = append(out, part[start:])
	}
	return out
}

// Interests of a person, alphabetically.
func (s *Store) Interests(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT tag FROM user_interests WHERE user_id::text = $1 ORDER BY lower(tag)`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// SetInterests replaces a person's interests with the given ones (cleaned by NormInterests).
func (s *Store) SetInterests(ctx context.Context, userID string, tags []string) error {
	tags = NormInterests(tags)
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id::text = $1)`, userID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM user_interests WHERE user_id::text = $1`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO user_interests (user_id, tag) SELECT $1::uuid, unnest($2::text[])`, userID, tags)
		return err
	})
}

// PopularInterests are the interests of the active employees, the most common first; prefix ("" — any) narrows them
// to the ones starting with it or having a word starting with it. A tag spelled differently counts as one — the most
// common spelling is shown.
func (s *Store) PopularInterests(ctx context.Context, prefix string, limit int) ([]Interest, error) {
	if limit <= 0 {
		limit = 20
	}
	p := strings.ToLower(strings.TrimLeft(strings.TrimSpace(prefix), "#"))
	p = strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(p)
	rows, err := s.db.Query(ctx, `SELECT mode() WITHIN GROUP (ORDER BY i.tag), count(*)::int FROM user_interests i
		JOIN users u ON u.id = i.user_id AND u.active AND NOT u.guest
		WHERE $1 = '' OR lower(i.tag) LIKE $1 || '%' OR lower(i.tag) LIKE '% ' || $1 || '%'
		GROUP BY lower(i.tag) ORDER BY count(*) DESC, lower(i.tag) LIMIT $2`, p, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Interest])
}

// fillInterests: the interests of each person.
func (s *Store) fillInterests(ctx context.Context, list []Profile, idx map[string]int, ids []string) error {
	rows, err := s.db.Query(ctx, `SELECT user_id::text, tag FROM user_interests WHERE user_id = ANY($1::uuid[]) ORDER BY lower(tag)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var uid, tag string
		if err := rows.Scan(&uid, &tag); err != nil {
			return err
		}
		list[idx[uid]].Interests = append(list[idx[uid]].Interests, tag)
	}
	return rows.Err()
}
