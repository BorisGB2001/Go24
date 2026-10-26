// Package apikeys issues and verifies personal go24 API keys.
//
// A token looks like go24_<key_id>_<secret>: key_id (12 hex) finds the row, secret (64 hex, 256 bits)
// is checked against its SHA-256. The secret is shown once at creation and never stored.
package apikeys

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
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const tokenPrefix = "go24_"

var ErrInvalid = i18n.E("err.key_invalid")

// Scopes a key can be limited to; names are locale keys scope.<scope>.
var AllScopes = []string{"projects:read", "projects:write", "tasks:read", "tasks:write", "im:read", "im:write", "crm:read", "crm:write", "bp:read", "bp:write", "mail:read", "mail:write", "collabs:read", "collabs:write", "calendar:read", "calendar:write", "disk:read", "disk:write", "feed:read", "feed:write", "documents:read", "documents:write", "settings:read", "settings:write", "bi:read", "openlines:read", "openlines:write", "kb:read", "kb:write", "booking:read", "booking:write", "company:read", "company:write", "telephony:read", "telephony:write", "placement", "imbot"}

type Key struct {
	ID         string
	KeyID      string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	LastUsedAt *time.Time
	LastUsedIP string
}

// Active reports whether the key can still be used.
func (k Key) Active() bool {
	return k.RevokedAt == nil && (k.ExpiresAt == nil || k.ExpiresAt.After(time.Now()))
}

// Principal is the caller authenticated by a key.
type Principal struct {
	UserID string
	KeyID  string
	Scopes []string
}

func (p Principal) Can(scope string) bool { return slices.Contains(p.Scopes, scope) }

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Create issues a new key and returns the full token (shown to the user once).
func (s *Store) Create(ctx context.Context, userID, name string, scopes []string, ttl time.Duration) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", i18n.E("err.key_name_required")
	}
	var clean []string
	for _, sc := range scopes {
		if slices.Contains(AllScopes, sc) && !slices.Contains(clean, sc) {
			clean = append(clean, sc)
		}
	}
	if len(clean) == 0 {
		return "", i18n.E("err.key_scopes_required")
	}
	var expires *time.Time
	if ttl > 0 {
		t := time.Now().Add(ttl)
		expires = &t
	}
	keyID, secret := randomHex(6), randomHex(32)
	hash := sha256.Sum256([]byte(secret))
	if _, err := s.db.Exec(ctx, `INSERT INTO api_keys (user_id, key_id, name, secret_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)`, userID, keyID, name, hash[:], clean, expires); err != nil {
		return "", err
	}
	return tokenPrefix + keyID + "_" + secret, nil
}

// ParseToken splits a token into key_id and secret.
func ParseToken(token string) (keyID, secret string, ok bool) {
	rest, found := strings.CutPrefix(token, tokenPrefix)
	if !found {
		return "", "", false
	}
	keyID, secret, found = strings.Cut(rest, "_")
	if !found || len(keyID) != 12 || len(secret) != 64 || !isHex(keyID) || !isHex(secret) {
		return "", "", false
	}
	return keyID, secret, true
}

// Authenticate checks the token and records its use.
func (s *Store) Authenticate(ctx context.Context, token, ip string) (Principal, error) {
	keyID, secret, ok := ParseToken(token)
	if !ok {
		return Principal{}, ErrInvalid
	}
	var (
		p                  = Principal{KeyID: keyID}
		hash               []byte
		expires, revokedAt *time.Time
	)
	err := s.db.QueryRow(ctx, `SELECT user_id, secret_hash, scopes, expires_at, revoked_at FROM api_keys WHERE key_id = $1`,
		keyID).Scan(&p.UserID, &hash, &p.Scopes, &expires, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrInvalid
	}
	if err != nil {
		return Principal{}, err
	}
	sum := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(sum[:], hash) != 1 || revokedAt != nil || (expires != nil && expires.Before(time.Now())) {
		return Principal{}, ErrInvalid
	}
	_, _ = s.db.Exec(ctx, `UPDATE api_keys SET last_used_at = now(), last_used_ip = $2 WHERE key_id = $1`, keyID, ip)
	return p, nil
}

func (s *Store) List(ctx context.Context, userID string) ([]Key, error) {
	rows, err := s.db.Query(ctx, `SELECT id, key_id, name, scopes, created_at, expires_at, revoked_at, last_used_at, last_used_ip
		FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Key])
}

// Revoke disables the user's own key.
func (s *Store) Revoke(ctx context.Context, userID, id string) error {
	_, err := s.db.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, id, userID)
	return err
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}
