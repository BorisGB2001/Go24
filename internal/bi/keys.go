package bi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// ---------- Keys for external BI, as «Подключение внешних BI» ----------

const keyPrefix = "go24bi_"

var ErrKeyInvalid = i18n.E("err.key_invalid")

// Key is a key of external BI: it reads Datasets (none — all) with the rights of CreatedBy.
type Key struct {
	ID         string
	KeyID      string
	Name       string
	Datasets   []string
	CreatedBy  string
	AuthorName string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	Uses       int64
}

// Reads: the key may read the data set.
func (k Key) Reads(ds string) bool { return len(k.Datasets) == 0 || slices.Contains(k.Datasets, ds) }

// CreateKey issues a key and returns its token (shown once).
func (s *Store) CreateKey(ctx context.Context, by, name string, datasets []string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", i18n.E("err.key_name_required")
	}
	clean := []string{}
	for _, d := range datasets {
		if ds, err := s.Dataset(ctx, d); err != nil {
			return "", err
		} else if ds != nil && !slices.Contains(clean, d) {
			clean = append(clean, d)
		}
	}
	keyID, secret := randomHex(6), randomHex(32)
	hash := sha256.Sum256([]byte(secret))
	if _, err := s.db.Exec(ctx, `INSERT INTO bi_keys (key_id, name, secret_hash, datasets, created_by) VALUES ($1, $2, $3, $4, $5)`,
		keyID, name, hash[:], clean, by); err != nil {
		return "", err
	}
	return keyPrefix + keyID + "_" + secret, nil
}

// Keys are all the keys, the newest first.
func (s *Store) Keys(ctx context.Context) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT k.id, k.key_id, k.name, k.datasets, k.created_by, u.name, k.created_at, k.revoked_at,
		k.last_used_at, k.last_used_ip, k.uses FROM bi_keys k JOIN users u ON u.id = k.created_by ORDER BY k.created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Key])
}

// RevokeKey disables a key; DeleteKey removes it.
func (s *Store) RevokeKey(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE bi_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	return err
}

func (s *Store) DeleteKey(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM bi_keys WHERE id = $1`, id)
	return err
}

// Authenticate checks a token and records its use; the caller checks that the author may still read with it.
func (s *Store) Authenticate(ctx context.Context, token, ip string) (Key, error) {
	rest, ok := strings.CutPrefix(token, keyPrefix)
	keyID, secret, cut := strings.Cut(rest, "_")
	if !ok || !cut || len(keyID) != 12 || len(secret) != 64 {
		return Key{}, ErrKeyInvalid
	}
	var (
		k    Key
		hash []byte
	)
	err := s.db.QueryRow(ctx, `SELECT id, key_id, name, datasets, created_by, revoked_at, secret_hash FROM bi_keys WHERE key_id = $1`,
		keyID).Scan(&k.ID, &k.KeyID, &k.Name, &k.Datasets, &k.CreatedBy, &k.RevokedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, ErrKeyInvalid
	}
	if err != nil {
		return Key{}, err
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 || k.RevokedAt != nil {
		return Key{}, ErrKeyInvalid
	}
	_, _ = s.db.Exec(ctx, `UPDATE bi_keys SET last_used_at = now(), last_used_ip = $2, uses = uses + 1 WHERE id = $1`, k.ID, ip)
	return k, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
