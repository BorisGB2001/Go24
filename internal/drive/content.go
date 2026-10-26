package drive

import (
	"context"
	"html"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// Поиск по содержимому: the text of a file of a drive is read out in the background (IndexDue) and the
// search of the drive and of «Документы» finds a file by a word of it, with the place it was found (Snippets).

// MaxContent bounds the characters of a file's text kept for the search.
const MaxContent = 200000

// Unindexed is a file whose current version is not read for the search yet.
type Unindexed struct {
	ID, Name, ContentType, Key string
	StorageID                  *string
	Size                       int64
}

// contentOn: the search by the text of files is on («Настройки → Диск»).
const contentOn = `NOT EXISTS (SELECT 1 FROM drive_limits WHERE kind = 'no_content' AND bytes = 1)`

// Unindexed is up to max files of the drives whose text is to be read, oldest first; none while the search by the
// text is off.
func (s *Store) Unindexed(ctx context.Context, max int) ([]Unindexed, error) {
	if off, err := s.off(ctx, offContent); err != nil || off {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT f.id, f.name, f.content_type, f.object_key, f.storage_id::text, f.size FROM files f
		WHERE f.drive IS NOT NULL AND f.content_key IS DISTINCT FROM f.object_key ORDER BY f.created_at LIMIT $1`, max)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Unindexed])
}

// SetContent keeps the text of a file's version key ("" — it has none); a newer version since is left to be read.
func (s *Store) SetContent(ctx context.Context, id, key, text string) error {
	if r := []rune(text); len(r) > MaxContent {
		text = string(r[:MaxContent])
	}
	_, err := s.db.Exec(ctx, `UPDATE files SET content_key = $2, content_text = nullif($3, '') WHERE id::text = $1 AND object_key = $2`,
		id, key, strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", " "), ""))
	return err
}

// tsQuery is the words of q as a prefix query of to_tsquery: «догов пост» — 'догов':* & 'пост':* ("" — no words).
func tsQuery(q string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(words) < 8 {
			words = append(words, "'"+w+"':*")
		}
	}
	return strings.Join(words, " & ")
}

// contentMatch is the condition of a file f whose text has the words of the query in the parameter $N.
func contentMatch(n string) string {
	return `($` + n + ` <> '' AND ` + contentOn + ` AND f.content_tsv @@ to_tsquery('russian', $` + n + `))`
}

// Snippets is, of the files with the ids, those found by their text rather than their name: the words of q in
// their place, as HTML with the words in <mark>.
func (s *Store) Snippets(ctx context.Context, ids []string, q string) (map[string]string, error) {
	tq := tsQuery(q)
	if tq == "" || len(ids) == 0 {
		return nil, nil
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.TrimSpace(q)) + "%"
	rows, err := s.db.Query(ctx, `SELECT f.id::text, ts_headline('russian', translate(f.content_text, '<>', '  '), to_tsquery('russian', $2),
			'StartSel=' || chr(1) || ', StopSel=' || chr(2) || ', MaxWords=24, MinWords=10, MaxFragments=1')
		FROM files f WHERE f.id::text = ANY($1) AND f.name NOT ILIKE $3 AND `+contentMatch("2"), ids, tq, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, h string
		if err := rows.Scan(&id, &h); err != nil {
			return nil, err
		}
		h = strings.Join(strings.Fields(html.EscapeString(h)), " ")
		out[id] = strings.NewReplacer("\x01", "<mark>", "\x02", "</mark>").Replace(h)
	}
	return out, rows.Err()
}
