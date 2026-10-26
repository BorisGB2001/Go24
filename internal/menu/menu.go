// Package menu keeps the left menu each employee sets up for themselves, as «Настроить меню»: the order
// of the items, the hidden ones, their own links. An employee with no menu of their own gets the company's default,
// which an administrator sets («Применить для всех»); with none either — go24's own order. The first item shown is
// the page opened after signing in. Installed applications with a menu item («Название пункта меню») are items too,
// «app.<id>», for those they are for.
package menu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
)

const (
	prefKey    = "menu" // user_prefs
	defaultKey = "menu" // company_settings
	// MaxCustom is how many links of their own an employee may add.
	MaxCustom = 30
	maxTitle  = 80
	// AppPrefix starts the id of an application's item.
	AppPrefix = "app."
)

// Builtin are go24's own items in go24's order, with the page each opens (a section — its first page).
var Builtin = []struct{ ID, URL string }{
	{"im", "/im"}, {"collabs", "/collabs"}, {"feed", "/feed"}, {"calendar", "/calendar"}, {"crm", "/crm/deals"},
	{"marketing", "/marketing"}, {"store", "/store/docs"}, {"mail", "/mail"}, {"openlines", "/openlines"}, {"bp", "/bp"}, {"kb", "/kb"}, {"sites", "/sites"},
	{"booking", "/booking"}, {"bi", "/bi"}, {"sign", "/sign"}, {"apps", "/apps"}, {"tasks", "/my"}, {"projects", "/projects"}, {"company", "/company"}, {"files", "/disk/my"},
}

// Custom is a link an employee added: inside go24 (/crm/deals?…) or outside (https://…).
type Custom struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	NewTab bool   `json:"new_tab,omitempty"`
}

// IsLocal: the link is a page of go24.
func (l Custom) IsLocal() bool { return strings.HasPrefix(l.URL, "/") }

// Config is a menu as set up: the order of the items (go24's and own), the hidden ones, the own links.
type Config struct {
	Order  []string `json:"order"`
	Hidden []string `json:"hidden,omitempty"`
	Custom []Custom `json:"custom,omitempty"`
}

// Item is one item of the menu to show: go24's (ID), an own link (Custom) or an application's (App: its title and page).
type Item struct {
	ID     string
	Custom *Custom
	App    *Custom
}

// URL is the page the item opens.
func (it Item) URL() string {
	if it.Custom != nil {
		return it.Custom.URL
	}
	if it.App != nil {
		return it.App.URL
	}
	for _, b := range Builtin {
		if b.ID == it.ID {
			return b.URL
		}
	}
	return ""
}

func builtin(id string) bool {
	return slices.ContainsFunc(Builtin, func(b struct{ ID, URL string }) bool { return b.ID == id })
}

// CheckURL: a page of go24 (a path from the root, not «//host») or an http(s) address.
func CheckURL(raw string) bool {
	if strings.HasPrefix(raw, "/") {
		return !strings.HasPrefix(raw, "//") && !strings.HasPrefix(raw, "/\\")
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Check validates a menu sent by the page and cleans it: unknown and repeated items are dropped, own links get ids.
func Check(c Config) (Config, error) {
	var out Config
	if len(c.Custom) > MaxCustom {
		return out, i18n.E("err.menu_many", MaxCustom)
	}
	own := map[string]bool{}
	for _, l := range c.Custom {
		l.Title, l.URL = strings.TrimSpace(l.Title), strings.TrimSpace(l.URL)
		if l.Title == "" || utf8.RuneCountInString(l.Title) > maxTitle {
			return out, i18n.E("err.menu_title", maxTitle)
		}
		if !CheckURL(l.URL) {
			return out, i18n.E("err.menu_url")
		}
		if l.ID == "" || builtin(l.ID) || own[l.ID] || !strings.HasPrefix(l.ID, "c") {
			l.ID = newID()
		}
		own[l.ID] = true
		out.Custom = append(out.Custom, l)
	}
	known := func(id string) bool { return builtin(id) || own[id] || strings.HasPrefix(id, AppPrefix) && len(id) <= 64 }
	seen := map[string]bool{}
	for _, id := range c.Order {
		if known(id) && !seen[id] {
			seen[id] = true
			out.Order = append(out.Order, id)
		}
	}
	for _, l := range out.Custom { // a link sent without its place goes last
		if !seen[l.ID] {
			seen[l.ID] = true
			out.Order = append(out.Order, l.ID)
		}
	}
	for _, id := range c.Hidden {
		if known(id) && !slices.Contains(out.Hidden, id) {
			out.Hidden = append(out.Hidden, id)
		}
	}
	return out, nil
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "c" + hex.EncodeToString(b)
}

// Arrange gives the items to show and the hidden ones in the menu's order. avail are go24's items this employee has
// (the messenger only when it is on); go24's items the menu does not name yet (new in go24) take their place after
// the item that comes before them in go24's order, or go last. apps are the items of the applications for the
// employee (ID «app.<id>»); those the menu does not name yet go last.
func Arrange(c Config, avail []string, apps ...Custom) (shown, hidden []Item) {
	order := slices.Clone(c.Order)
	for i, b := range Builtin {
		if slices.Contains(order, b.ID) {
			continue
		}
		at := len(order) // none of those before it is named: last
		for j := i - 1; j >= 0; j-- {
			if k := slices.Index(order, Builtin[j].ID); k >= 0 {
				at = k + 1
				break
			}
		}
		order = slices.Insert(order, at, b.ID)
	}
	for _, a := range apps {
		if !slices.Contains(order, a.ID) {
			order = append(order, a.ID)
		}
	}
	for _, id := range order {
		it := Item{ID: id}
		if builtin(id) {
			if !slices.Contains(avail, id) {
				continue
			}
		} else if k := slices.IndexFunc(c.Custom, func(l Custom) bool { return l.ID == id }); k >= 0 {
			it.Custom = &c.Custom[k]
		} else if k := slices.IndexFunc(apps, func(l Custom) bool { return l.ID == id }); k >= 0 {
			it.App = &apps[k]
		} else {
			continue
		}
		if slices.Contains(c.Hidden, id) {
			hidden = append(hidden, it)
		} else {
			shown = append(shown, it)
		}
	}
	return shown, hidden
}

// Store reads and writes the menus.
type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Get gives the employee's menu: their own (own = true), else the company's default, else go24's (set = false).
func (s *Store) Get(ctx context.Context, userID string) (c Config, own, set bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefKey).Scan(&raw)
	if err == nil {
		return c, true, true, json.Unmarshal(raw, &c)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return c, false, false, err
	}
	c, set, err = s.Default(ctx)
	return c, false, set, err
}

// Save keeps the employee's own menu.
func (s *Store) Save(ctx context.Context, userID string, c Config) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO user_prefs (user_id, key, value) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, userID, prefKey, raw)
	return err
}

// Reset drops the employee's own menu: the company's default (or go24's) is back.
func (s *Store) Reset(ctx context.Context, userID string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM user_prefs WHERE user_id = $1 AND key = $2`, userID, prefKey)
	return err
}

// Default is the company's menu for those with none of their own (set = false — go24's order).
func (s *Store) Default(ctx context.Context) (c Config, set bool, err error) {
	var raw []byte
	err = s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, defaultKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	return c, true, json.Unmarshal(raw, &c)
}

// SaveDefault sets the company's menu (an administrator's «Применить для всех»): it is shown to everyone who has no
// menu of their own.
func (s *Store) SaveDefault(ctx context.Context, c Config, by string) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, NULLIF($3, '')::uuid)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`, defaultKey, raw, by)
	return err
}

// ResetDefault drops the company's menu: go24's order is back for those with none of their own.
func (s *Store) ResetDefault(ctx context.Context) error {
	_, err := s.db.Exec(ctx, `DELETE FROM company_settings WHERE key = $1`, defaultKey)
	return err
}
