package sign

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Checking a document: anyone with the «Код проверки» of the signing sheet, or with the file itself, learns
// that the document was signed in go24, by whom and when.

// NormCode is the verify code as kept: upper case, with no dashes, spaces or other signs; "" — not a code.
func NormCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '\t':
		default:
			return ""
		}
	}
	if b.Len() < 8 || b.Len() > 32 {
		return ""
	}
	return b.String()
}

// ShowCode is the verify code as people read it: in fours, XXXX-XXXX-XXXX.
func ShowCode(code string) string {
	var b strings.Builder
	for i, r := range code {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// ByCode is the document of a verify code.
func (s *Store) ByCode(ctx context.Context, code string) (Doc, error) {
	code = NormCode(code)
	if code == "" {
		return Doc{}, ErrNotFound
	}
	return s.byQuery(ctx, `SELECT id::text FROM sign_docs WHERE verify_code = $1`, code)
}

// ByHash is the document whose file sent for signing or signed copy has the SHA-256; signed tells which of them it is.
// Of several documents of the same file the last signed one is taken.
func (s *Store) ByHash(ctx context.Context, hash string) (d Doc, signed bool, err error) {
	if hash == "" {
		return Doc{}, false, ErrNotFound
	}
	d, err = s.byQuery(ctx, `SELECT id::text FROM sign_docs WHERE result_hash = $1 OR hash = $1
		ORDER BY result_hash = $1 DESC, status = 'done' DESC, created_at DESC LIMIT 1`, hash)
	return d, err == nil && d.ResultHash == hash, err
}

func (s *Store) byQuery(ctx context.Context, q, arg string) (Doc, error) {
	var id string
	err := s.db.QueryRow(ctx, q, arg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Doc{}, ErrNotFound
	}
	if err != nil {
		return Doc{}, err
	}
	return s.Get(ctx, id)
}
