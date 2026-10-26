// Package ai keeps the connections of AI providers (Anthropic, OpenAI, Google Gemini, a server compatible with OpenAI
// — Ollama, vLLM, LM Studio…): the company's and personal ones of an employee, as the agents of the sections use them.
//
// A key is sealed with AES-GCM under a key derived from go24's own secret and is never given back: only its last four
// characters are shown. An employee works through the connection they picked, else their own default one, else the
// company's default one.
package ai

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

// Providers in the order of the form; names are locale keys ai.provider.<name>.
var Providers = []string{"anthropic", "openai", "google", "compatible"}

// DefaultURL is the API address of a provider when the connection gives none.
var DefaultURL = map[string]string{
	"anthropic": "https://api.anthropic.com/v1",
	"openai":    "https://api.openai.com/v1",
	"google":    "https://generativelanguage.googleapis.com/v1beta",
}

var (
	ErrNotFound = i18n.E("err.not_found")
	ErrNone     = i18n.E("err.ai_none")
)

// Connection is a connection of a provider (without its key).
type Connection struct {
	ID         string
	Owner      string // "" — the company's
	Name       string
	Provider   string
	BaseURL    string // "" — DefaultURL of the provider
	Model      string // the default model
	KeyTail    string // the last four characters of the key ("" — no key)
	Default    bool
	CheckedAt  *time.Time
	CheckError string
	CreatedAt  time.Time
}

// URL is the API address the connection calls.
func (c Connection) URL() string {
	if c.BaseURL != "" {
		return strings.TrimRight(c.BaseURL, "/")
	}
	return DefaultURL[c.Provider]
}

type Store struct {
	db  *pgxpool.Pool
	key []byte // AES-256 key for the provider keys
	// HTTP calls the providers (a check of a connection)
	HTTP *http.Client
}

// NewStore: secret is go24's own secret (the session secret); the key of the provider keys is derived from it.
func NewStore(db *pgxpool.Pool, secret string) *Store {
	k := sha256.Sum256([]byte("go24 ai keys\x00" + secret))
	return &Store{db: db, key: k[:], HTTP: &http.Client{Timeout: 20 * time.Second}}
}

func (s *Store) seal(plain string) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (s *Store) open(sealed []byte) (string, error) {
	if len(sealed) == 0 {
		return "", nil
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(sealed) < gcm.NonceSize() {
		return "", errors.New("ai: bad key")
	}
	plain, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], nil)
	return string(plain), err
}

const cols = `id, COALESCE(owner_id::text, ''), name, provider, base_url, model, key_tail, is_default, checked_at, check_error, created_at`

func scan(row pgx.Row) (Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.Owner, &c.Name, &c.Provider, &c.BaseURL, &c.Model, &c.KeyTail, &c.Default, &c.CheckedAt, &c.CheckError, &c.CreatedAt)
	return c, err
}

// List: the company's connections, then the user's own.
func (s *Store) List(ctx context.Context, userID string) ([]Connection, error) {
	rows, err := s.db.Query(ctx, `SELECT `+cols+` FROM ai_connections WHERE owner_id IS NULL OR owner_id = $1
		ORDER BY owner_id IS NOT NULL, is_default DESC, created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		c, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get is a connection the user sees: the company's or their own.
func (s *Store) Get(ctx context.Context, userID, id string) (Connection, error) {
	c, err := scan(s.db.QueryRow(ctx, `SELECT `+cols+` FROM ai_connections WHERE id::text = $1 AND (owner_id IS NULL OR owner_id = $2)`, id, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Save adds (no ID) or changes a connection of its owner (c.Owner; "" — the company's, the caller checks the right);
// key "" keeps the key it has. The default one of the owner becomes the only default; the first one is the default.
func (s *Store) Save(ctx context.Context, by string, c Connection, key string) (string, error) {
	c.Name, c.BaseURL, c.Model, key = strings.TrimSpace(c.Name), strings.TrimSpace(c.BaseURL), strings.TrimSpace(c.Model), strings.TrimSpace(key)
	switch {
	case c.Name == "":
		return "", i18n.E("err.ai_name_required")
	case !slices.Contains(Providers, c.Provider):
		return "", i18n.E("err.ai_provider")
	case c.Provider == "compatible" && c.BaseURL == "":
		return "", i18n.E("err.ai_url_required")
	}
	if c.BaseURL != "" {
		if u, err := url.Parse(c.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", i18n.E("err.ai_url_bad")
		}
	}
	var sealed []byte
	tail := ""
	if key != "" {
		var err error
		if sealed, err = s.seal(key); err != nil {
			return "", err
		}
		tail = key[max(0, len(key)-4):]
	}
	owner := &c.Owner
	if c.Owner == "" {
		owner = nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var none bool // the owner has no connection yet: this one is the default
	if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM ai_connections WHERE owner_id IS NOT DISTINCT FROM $1 AND id::text <> $2)`,
		owner, c.ID).Scan(&none); err != nil {
		return "", err
	}
	c.Default = c.Default || none
	if c.ID == "" {
		if key == "" && c.Provider != "compatible" {
			return "", i18n.E("err.ai_key_required")
		}
		err = tx.QueryRow(ctx, `INSERT INTO ai_connections (owner_id, name, provider, base_url, model, key_sealed, key_tail, is_default, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
			owner, c.Name, c.Provider, c.BaseURL, c.Model, sealed, tail, c.Default, by).Scan(&c.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `UPDATE ai_connections SET name = $3, provider = $4, base_url = $5, model = $6, is_default = $7,
			key_sealed = CASE WHEN $8::bytea IS NULL THEN key_sealed ELSE $8 END,
			key_tail = CASE WHEN $8::bytea IS NULL THEN key_tail ELSE $9 END, checked_at = NULL, check_error = ''
			WHERE id::text = $1 AND owner_id IS NOT DISTINCT FROM $2`,
			c.ID, owner, c.Name, c.Provider, c.BaseURL, c.Model, c.Default, sealed, tail)
		if err == nil && tag.RowsAffected() == 0 {
			return "", ErrNotFound
		}
	}
	if err != nil {
		return "", err
	}
	if c.Default {
		if _, err := tx.Exec(ctx, `UPDATE ai_connections SET is_default = false WHERE owner_id IS NOT DISTINCT FROM $1 AND id <> $2`, owner, c.ID); err != nil {
			return "", err
		}
	}
	return c.ID, tx.Commit(ctx)
}

// Delete removes a connection of its owner ("" — the company's); the default one passes to the oldest left.
func (s *Store) Delete(ctx context.Context, owner, id string) error {
	var o *string
	if owner != "" {
		o = &owner
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM ai_connections WHERE id::text = $1 AND owner_id IS NOT DISTINCT FROM $2`, id, o)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	_, err = s.db.Exec(ctx, `UPDATE ai_connections SET is_default = true WHERE id = (SELECT id FROM ai_connections
		WHERE owner_id IS NOT DISTINCT FROM $1 ORDER BY created_at LIMIT 1)
		AND NOT EXISTS (SELECT 1 FROM ai_connections WHERE owner_id IS NOT DISTINCT FROM $1 AND is_default)`, o)
	return err
}

// Choose: the connection the user works through ("" — the default: their own, else the company's).
func (s *Store) Choose(ctx context.Context, userID, id string) error {
	if id == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM ai_choice WHERE user_id = $1`, userID)
		return err
	}
	if _, err := s.Get(ctx, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO ai_choice (user_id, connection_id) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET connection_id = EXCLUDED.connection_id`, userID, id)
	return err
}

// Chosen is the connection the user picked ("" — none).
func (s *Store) Chosen(ctx context.Context, userID string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT connection_id FROM ai_choice WHERE user_id = $1`, userID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// For is the connection the user works through, with its key: the one they picked, else their own default one, else
// the company's default one; ErrNone — there is none.
func (s *Store) For(ctx context.Context, userID string) (Connection, string, error) {
	var sealed []byte
	var c Connection
	err := s.db.QueryRow(ctx, `SELECT `+cols+`, key_sealed FROM ai_connections
		WHERE owner_id IS NULL OR owner_id = $1
		ORDER BY (id = (SELECT connection_id FROM ai_choice WHERE user_id = $1)) IS TRUE DESC, owner_id IS NULL, is_default DESC, created_at
		LIMIT 1`, userID).Scan(&c.ID, &c.Owner, &c.Name, &c.Provider, &c.BaseURL, &c.Model, &c.KeyTail, &c.Default, &c.CheckedAt, &c.CheckError, &c.CreatedAt, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, "", ErrNone
	}
	if err != nil {
		return c, "", err
	}
	key, err := s.open(sealed)
	return c, key, err
}

// Check asks the provider for its models with the connection's key and notes the outcome; the models are returned for
// the choice of the default one.
func (s *Store) Check(ctx context.Context, userID, id string) ([]string, error) {
	c, err := s.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	var sealed []byte
	if err := s.db.QueryRow(ctx, `SELECT key_sealed FROM ai_connections WHERE id = $1`, c.ID).Scan(&sealed); err != nil {
		return nil, err
	}
	key, err := s.open(sealed)
	if err != nil {
		return nil, err
	}
	models, cerr := s.models(ctx, c, key)
	msg := ""
	if cerr != nil {
		msg = cerr.Error()
	}
	if _, err := s.db.Exec(ctx, `UPDATE ai_connections SET checked_at = now(), check_error = $2 WHERE id = $1`, c.ID, msg); err != nil {
		return nil, err
	}
	if cerr != nil {
		return nil, i18n.E("err.ai_check", msg)
	}
	return models, nil
}

// models lists the models of the provider (GET …/models).
func (s *Store) models(ctx context.Context, c Connection, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL()+"/models", nil)
	if err != nil {
		return nil, err
	}
	switch c.Provider {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case "google":
		req.Header.Set("x-goog-api-key", key)
	default:
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error json.RawMessage `json:"error"`
		}
		msg := ""
		if json.Unmarshal(body, &e) == nil && len(e.Error) > 0 {
			var o struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(e.Error, &o) == nil && o.Message != "" {
				msg = ": " + o.Message
			}
		}
		return nil, fmt.Errorf("HTTP %d%s", res.StatusCode, msg)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	var list []string
	for _, m := range out.Data {
		list = append(list, m.ID)
	}
	for _, m := range out.Models {
		list = append(list, strings.TrimPrefix(m.Name, "models/"))
	}
	slices.Sort(list)
	return list, nil
}
