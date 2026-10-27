// Package portal keeps the general settings of the portal, as «Настройки → Общие»: the company's name and
// logo, the default language and time zone, the format of dates, times and names. They live in company_settings.
package portal

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const (
	keySettings = "portal"
	keyLogo     = "portal_logo"
	// MaxLogo is the largest logo accepted, in bytes.
	MaxLogo = 256 << 10
)

// Date formats («" — as the language says), keyed by their names in the form and the API.
var DateFormats = map[string]string{"dmy": "02.01.2006", "ymd": "2006-01-02", "mdy": "01/02/2006", "dmy_slash": "02/01/2006"}

// Time formats: 24 hours (15:04) or 12 hours (3:04 PM).
var TimeFormats = map[string]string{"24": "15:04", "12": "3:04 PM"}

// DateFormatNames are the date formats in the order the form lists them.
var DateFormatNames = []string{"dmy", "ymd", "mdy", "dmy_slash"}

// Name formats, as «Формат имени»: the parts of the name in their order; «_short» is the initial
// (#NAME_SHORT# — «И.»). The first one is the default.
var NameFormats = map[string][]string{
	"first_last":        {"first", "last"},                       // #NAME# #LAST_NAME#
	"last_first":        {"last", "first"},                       // #LAST_NAME# #NAME#
	"last_first_second": {"last", "first", "second"},             // #LAST_NAME# #NAME# #SECOND_NAME#
	"first_second_last": {"first", "second", "last"},             // #NAME# #SECOND_NAME# #LAST_NAME#
	"last_initials":     {"last", "first_short", "second_short"}, // #LAST_NAME# #NAME_SHORT# #SECOND_NAME_SHORT#
	"initials_last":     {"first_short", "second_short", "last"}, // #NAME_SHORT# #SECOND_NAME_SHORT# #LAST_NAME#
}

// NameFormatNames are the name formats in the order the form lists them.
var NameFormatNames = []string{"first_last", "last_first", "last_first_second", "first_second_last", "last_initials",
	"initials_last"}

// nameFormat is the portal's name format as last loaded or saved, for UserName.
var nameFormat atomic.Value

// FormatName composes an employee's name by the format (the default for an unknown one); empty parts are left out,
// and it is empty when the first and last names both are.
func FormatName(format, first, last, second string) string {
	first, last, second = strings.TrimSpace(first), strings.TrimSpace(last), strings.TrimSpace(second)
	if first == "" && last == "" {
		return ""
	}
	parts, ok := NameFormats[format]
	if !ok {
		parts = NameFormats["first_last"]
	}
	short := func(s string) string {
		if r := []rune(s); len(r) > 0 {
			return string(r[0]) + "."
		}
		return ""
	}
	var out []string
	for _, p := range parts {
		out = append(out, map[string]string{"first": first, "last": last, "second": second,
			"first_short": short(first), "second_short": short(second)}[p])
	}
	return strings.Join(strings.Fields(strings.Join(out, " ")), " ")
}

// UserName composes a name by the portal's format (as Store last read it; the default before that).
func UserName(first, last, second string) string {
	f, _ := nameFormat.Load().(string)
	return FormatName(f, first, last, second)
}

// Rename gives every employee whose first or last name is filled their name by the format; it says how many changed.
func Rename(ctx context.Context, db *pgxpool.Pool, format string) (int, error) {
	rows, err := db.Query(ctx, `SELECT id::text, name, first_name, last_name, second_name FROM users
		WHERE first_name <> '' OR last_name <> ''`)
	if err != nil {
		return 0, err
	}
	type person struct{ id, name, first, last, second string }
	people, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (person, error) {
		var p person
		return p, r.Scan(&p.id, &p.name, &p.first, &p.last, &p.second)
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range people {
		if name := FormatName(format, p.first, p.last, p.second); name != p.name {
			if _, err := db.Exec(ctx, `UPDATE users SET name = $2 WHERE id = $1`, p.id, name); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

// RenameAll gives the employees their names by the format saved in the settings (the importer, which keeps no Store).
func RenameAll(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var set Settings
	var raw []byte
	err := db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, keySettings).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &set); err != nil {
			return 0, err
		}
	}
	return Rename(ctx, db, set.NameFormat)
}

// Settings are the general settings; empty fields mean «as by default» (the go24 name, the browser's language, GO24_TZ,
// the formats of the language, «Имя Фамилия»).
type Settings struct {
	Name       string `json:"name"`
	Lang       string `json:"lang"`
	TZ         string `json:"tz"`
	DateFormat string `json:"date_format"`
	TimeFormat string `json:"time_format"`
	NameFormat string `json:"name_format"`
	HasLogo    bool   `json:"has_logo"` // set by Get, not stored
}

// Title is the portal's name to show: its own or «go24».
func (s Settings) Title() string {
	if s.Name != "" {
		return s.Name
	}
	return "go24"
}

// Format gives the portal's format.date and format.datetime over the language's msg (others are kept as they are).
func (s Settings) Format(key, msg string) string {
	d, t := DateFormats[s.DateFormat], TimeFormats[s.TimeFormat]
	switch {
	case key == "format.date" && d != "":
		return d
	case key != "format.datetime":
		return msg
	case d != "":
		return d + " " + cmp.Or(t, TimeFormats["24"])
	case t != "":
		return strings.Replace(msg, "15:04", t, 1)
	}
	return msg
}

// Store reads and writes the settings; the last read is cached for the pages (Current) and its date formats are
// installed into the bundle of messages.
type Store struct {
	db     *pgxpool.Pool
	bundle *i18n.Bundle
	cur    atomic.Pointer[Settings]
}

func NewStore(db *pgxpool.Pool, bundle *i18n.Bundle) *Store { return &Store{db: db, bundle: bundle} }

// Current is the settings as last loaded or saved (zero before the first load).
func (s *Store) Current() Settings {
	if p := s.cur.Load(); p != nil {
		return *p
	}
	return Settings{}
}

// Get reads the settings and caches them.
func (s *Store) Get(ctx context.Context) (Settings, error) {
	var set Settings
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, keySettings).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return set, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &set); err != nil {
			return set, err
		}
	}
	if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM company_settings WHERE key = $1)`, keyLogo).Scan(&set.HasLogo); err != nil {
		return set, err
	}
	s.cur.Store(&set)
	nameFormat.Store(set.NameFormat)
	if s.bundle != nil {
		if set.DateFormat == "" && set.TimeFormat == "" {
			s.bundle.SetFilter(nil)
		} else {
			s.bundle.SetFilter(set.Format)
		}
	}
	return set, nil
}

// Check validates the settings against the supported languages.
func Check(set Settings, langs func(string) bool) error {
	set.Name = strings.TrimSpace(set.Name)
	switch {
	case len([]rune(set.Name)) > 100:
		return i18n.E("err.portal_name")
	case set.Lang != "" && !langs(set.Lang):
		return i18n.E("err.portal_lang")
	case set.DateFormat != "" && DateFormats[set.DateFormat] == "":
		return i18n.E("err.portal_format")
	case set.TimeFormat != "" && TimeFormats[set.TimeFormat] == "":
		return i18n.E("err.portal_format")
	case set.NameFormat != "" && NameFormats[set.NameFormat] == nil:
		return i18n.E("err.portal_format")
	}
	if set.TZ != "" {
		if _, err := time.LoadLocation(set.TZ); err != nil || set.TZ == "Local" {
			return i18n.E("err.portal_tz")
		}
	}
	return nil
}

// Save stores the settings (the logo is kept) and caches them; a new name format renames the employees.
func (s *Store) Save(ctx context.Context, set Settings, by string) error {
	set.Name = strings.TrimSpace(set.Name)
	set.HasLogo = false
	if set.NameFormat == "first_last" {
		set.NameFormat = ""
	}
	was, err := s.Get(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(set)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		keySettings, raw, by); err != nil {
		return err
	}
	if set.NameFormat != was.NameFormat {
		if _, err := Rename(ctx, s.db, set.NameFormat); err != nil {
			return err
		}
	}
	_, err = s.Get(ctx)
	return err
}

type logo struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

// LogoTypes are the pictures accepted as a logo.
var LogoTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"}

// SaveLogo stores the logo; its type is sniffed (an SVG is recognised by its text).
func (s *Store) SaveLogo(ctx context.Context, data []byte, by string) error {
	if len(data) == 0 || len(data) > MaxLogo {
		return i18n.E("err.portal_logo_size")
	}
	ctype := http.DetectContentType(data)
	if i := strings.IndexByte(ctype, ';'); i > 0 {
		ctype = ctype[:i]
	}
	if head := strings.ToLower(string(data[:min(len(data), 512)])); strings.Contains(head, "<svg") {
		ctype = "image/svg+xml"
	}
	if !slices.Contains(LogoTypes, ctype) {
		return i18n.E("err.portal_logo_type")
	}
	raw, err := json.Marshal(logo{Type: ctype, Data: base64.StdEncoding.EncodeToString(data)})
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		keyLogo, raw, by); err != nil {
		return err
	}
	_, err = s.Get(ctx)
	return err
}

// DeleteLogo removes the logo (the go24 mark is shown again).
func (s *Store) DeleteLogo(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM company_settings WHERE key = $1`, keyLogo); err != nil {
		return err
	}
	_, err := s.Get(ctx)
	return err
}

// Logo gives the logo and its type (nil when there is none).
func (s *Store) Logo(ctx context.Context) ([]byte, string, error) {
	var l logo
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, keyLogo).Scan(&l)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	data, err := base64.StdEncoding.DecodeString(l.Data)
	return data, l.Type, err
}
