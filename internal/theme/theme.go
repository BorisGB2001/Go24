// Package theme keeps the look each employee picks, as «Тема оформления»: light, dark or as the system
// has it, and the background of the portal — one of go24's or a picture of their own. An employee who picked none
// gets the company's theme, which an administrator sets («Применить для всех»); with none either — go24's look.
package theme

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const (
	prefKey    = "theme" // user_prefs
	defaultKey = "theme" // company_settings
	// MaxImage is the size limit of a picture of one's own.
	MaxImage = 5 << 20
	// Custom is the background of a picture of one's own.
	Custom = "custom"
)

// Modes: "" — as the system has it (the browser's setting), "light", "dark".
var Modes = []string{"", "light", "dark"}

// Backgrounds are go24's backgrounds in the order of the picker ("" — none, the plain one); their colors are in
// tokens.css (data-bg), each with a light and a dark variant.
var Backgrounds = []string{"", "sky", "ocean", "mint", "forest", "sand", "sunset", "rose", "lavender", "aurora", "graphite"}

// Theme is a look as picked. Image is the storage key of the picture of one's own (Bg = Custom), ImageType its type.
type Theme struct {
	Mode      string `json:"mode,omitempty"`
	Bg        string `json:"bg,omitempty"`
	Image     string `json:"image,omitempty"`
	ImageType string `json:"image_type,omitempty"`
}

// Check validates a theme as sent: a known mode and background, a picture only with Bg = Custom.
func Check(t Theme) (Theme, error) {
	if !slices.Contains(Modes, t.Mode) {
		return t, i18n.E("err.theme")
	}
	if t.Bg == Custom {
		if t.Image == "" {
			return t, i18n.E("err.theme_image")
		}
		return t, nil
	}
	if !slices.Contains(Backgrounds, t.Bg) {
		return t, i18n.E("err.theme")
	}
	t.Image, t.ImageType = "", ""
	return t, nil
}

// Store reads and writes the themes.
type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Get gives the employee's theme: their own (own = true), else the company's, else go24's (the zero Theme).
func (s *Store) Get(ctx context.Context, userID string) (t Theme, own bool, err error) {
	t, own, err = s.Own(ctx, userID)
	if err != nil || own {
		return t, own, err
	}
	t, _, err = s.Default(ctx)
	return t, false, err
}

// Own is the employee's own theme (ok = false — they picked none).
func (s *Store) Own(ctx context.Context, userID string) (t Theme, ok bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return t, true, json.Unmarshal(raw, &t)
}

// Save keeps the employee's own theme.
func (s *Store) Save(ctx context.Context, userID string, t Theme) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefKey, raw)
	return err
}

// Reset drops the employee's own theme: the company's (or go24's) is back.
func (s *Store) Reset(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefKey)
	return err
}

// Default is the company's theme for those with none of their own (set = false — go24's look).
func (s *Store) Default(ctx context.Context) (t Theme, set bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, defaultKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return t, true, json.Unmarshal(raw, &t)
}

// SaveDefault sets the company's theme (an administrator's «Применить для всех»).
func (s *Store) SaveDefault(ctx context.Context, t Theme, by string) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, defaultKey, raw, by)
	return err
}

// ResetDefault drops the company's theme: go24's look is back for those with none of their own.
func (s *Store) ResetDefault(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM company_settings WHERE key = $1`, defaultKey)
	return err
}
