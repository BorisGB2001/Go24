// Package apps is «Приложения → Разработчикам → Локальное приложение»: an administrator registers an
// application, it opens as a page of go24 in a frame or works through REST only, and acts for an employee with OAuth 2.0
// tokens — the access token an hour, the refresh token 28 days. Tokens look like go24at_<token_id>_<secret> and
// go24rt_<token_id>_<secret>; only SHA-256 of the secrets is stored.
package apps

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/hooks"
	"go24/internal/i18n"
)

const (
	AccessTTL  = time.Hour
	RefreshTTL = 28 * 24 * time.Hour
	CodeTTL    = time.Minute

	accessPrefix  = "go24at_"
	refreshPrefix = "go24rt_"
)

var (
	ErrInvalid = apikeys.ErrInvalid
	ErrClient  = i18n.E("err.app_client")
)

// App is a local application.
type App struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	HandlerURL       string     `json:"handler_url"`
	InstallURL       string     `json:"install_url"`
	MenuTitle        string     `json:"menu_title"`
	APIOnly          bool       `json:"api_only"`
	Scopes           []string   `json:"scopes"`
	ClientID         string     `json:"client_id"`
	ClientSecret     string     `json:"-"`
	ApplicationToken string     `json:"-"`
	InstalledAt      *time.Time `json:"installed_at"`
	InstallStatus    *int       `json:"-"`
	InstallError     string     `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	Users            int        `json:"-"` // employees with a live refresh token
}

// Installed: the application was installed and can be opened.
func (a App) Installed() bool { return a.InstalledAt != nil }

// Title is what the menu and the list call it.
func (a App) Title() string {
	if a.MenuTitle != "" {
		return a.MenuTitle
	}
	return a.Name
}

// Tokens are what an application gets for an employee.
type Tokens struct {
	Access, Refresh string
	Expires         time.Time
	UserID          string
}

type Store struct {
	db     *pgxpool.Pool
	client *http.Client
	wake   chan struct{} // events queued in this process (a chat bot's): deliver them now
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db, client: &http.Client{Timeout: 15 * time.Second}, wake: make(chan struct{}, 1)}
}

// Wake asks the worker to deliver the queued events now, not on its next round. Never blocks.
func (s *Store) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Woken is where Wake is heard.
func (s *Store) Woken() <-chan struct{} { return s.wake }

func checkURL(raw string, required bool) error {
	if raw == "" && !required {
		return nil
	}
	if hooks.CheckURL(raw) != nil {
		return i18n.E("err.app_url")
	}
	return nil
}

// Save creates (ID "": with a new code, key and application token) or changes an application (administrators;
// checked by the web layer). An application with a page needs the handler's address.
func (s *Store) Save(ctx context.Context, userID string, a App) (string, error) {
	a.Name, a.MenuTitle = strings.TrimSpace(a.Name), strings.TrimSpace(a.MenuTitle)
	a.HandlerURL, a.InstallURL = strings.TrimSpace(a.HandlerURL), strings.TrimSpace(a.InstallURL)
	if a.Name == "" || len([]rune(a.Name)) > 100 || len([]rune(a.MenuTitle)) > 100 {
		return "", i18n.E("err.app_name")
	}
	if err := checkURL(a.HandlerURL, !a.APIOnly); err != nil {
		return "", err
	}
	if err := checkURL(a.InstallURL, false); err != nil {
		return "", err
	}
	var scopes []string
	for _, sc := range a.Scopes {
		if slices.Contains(apikeys.AllScopes, sc) && !slices.Contains(scopes, sc) {
			scopes = append(scopes, sc)
		}
	}
	if len(scopes) == 0 {
		return "", i18n.E("err.key_scopes_required")
	}
	if a.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO apps (name, handler_url, install_url, menu_title, api_only, scopes, client_id,
			client_secret, application_token, created_by) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			a.Name, a.HandlerURL, a.InstallURL, a.MenuTitle, a.APIOnly, scopes, "local."+randomHex(8), randomHex(32),
			randomHex(16), userID).Scan(&a.ID)
		return a.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE apps SET name = $2, handler_url = $3, install_url = $4, menu_title = $5, api_only = $6,
		scopes = $7 WHERE id::text = $1`, a.ID, a.Name, a.HandlerURL, a.InstallURL, a.MenuTitle, a.APIOnly, scopes)
	if err == nil && tag.RowsAffected() == 0 {
		err = i18n.E("err.not_found")
	}
	return a.ID, err
}

const appCols = `a.id, a.name, a.handler_url, a.install_url, a.menu_title, a.api_only, a.scopes, a.client_id, a.client_secret,
	a.application_token, a.installed_at, a.install_status, a.install_error, a.created_at,
	(SELECT count(DISTINCT t.user_id) FROM app_tokens t WHERE t.app_id = a.id AND t.refresh_expires > now())::int`

func (s *Store) list(ctx context.Context, where string, args ...any) ([]App, error) {
	rows, err := s.db.Query(ctx, `SELECT `+appCols+` FROM apps a `+where+` ORDER BY a.created_at`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[App])
}

// List is every application (for «Настройки → Приложения»).
func (s *Store) List(ctx context.Context) ([]App, error) { return s.list(ctx, "") }

// appFor: the application a is for the employee $1 («Кому доступно») — nobody is chosen, or they are, or a
// department of theirs (with its sub-departments).
const appFor = `(NOT EXISTS (SELECT 1 FROM app_access x WHERE x.app_id = a.id)
	OR EXISTS (SELECT 1 FROM app_access x WHERE x.app_id = a.id AND (x.user_id = $1 OR x.department_id IN (
		WITH RECURSIVE dep AS (SELECT department_id AS id FROM user_departments WHERE user_id = $1
			UNION SELECT d.parent_id FROM departments d JOIN dep ON d.id = dep.id WHERE d.parent_id IS NOT NULL)
		SELECT id FROM dep))))`

// forToken is appFor for the employee of the token t.
var forToken = strings.ReplaceAll(appFor, "$1", "t.user_id")

// Installed are the applications the employee opens: installed ones with a page that are for them.
func (s *Store) Installed(ctx context.Context, userID string) ([]App, error) {
	return s.list(ctx, "WHERE a.installed_at IS NOT NULL AND NOT a.api_only AND "+appFor, userID)
}

// MayUse: the application is for the employee («Кому доступно»).
func (s *Store) MayUse(ctx context.Context, id, userID string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM apps a WHERE a.id::text = $2 AND `+appFor+`)`, userID, id).Scan(&ok)
	return ok, err
}

// Ref is one of «Кому доступно»: an employee, or a department («dept:ID»).
type Ref struct{ ID, Name string }

// Access is «Кому доступно» of every application, by application.
func (s *Store) Access(ctx context.Context) (map[string][]Ref, error) {
	rows, err := s.db.Query(ctx, `SELECT x.app_id::text, coalesce(x.user_id::text, 'dept:' || x.department_id::text), coalesce(u.name, d.name, '')
		FROM app_access x LEFT JOIN users u ON u.id = x.user_id LEFT JOIN departments d ON d.id = x.department_id
		ORDER BY x.department_id IS NULL, coalesce(d.name, u.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	access := map[string][]Ref{}
	for rows.Next() {
		var app string
		var r Ref
		if err := rows.Scan(&app, &r.ID, &r.Name); err != nil {
			return nil, err
		}
		access[app] = append(access[app], r)
	}
	return access, rows.Err()
}

// SetAccess chooses whom the application is for: people and departments («dept:ID»); none — everybody. Those left
// out lose its tokens at once.
func (s *Store) SetAccess(ctx context.Context, id string, to []string) error {
	var users, depts []string
	for _, v := range to {
		if d, ok := strings.CutPrefix(v, "dept:"); ok {
			depts = append(depts, d)
		} else if v != "" {
			users = append(users, v)
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var app string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM apps WHERE id::text = $1 FOR UPDATE`, id).Scan(&app); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return i18n.E("err.not_found")
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_access WHERE app_id = $1`, app); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app_access (app_id, user_id)
		SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, app, users); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app_access (app_id, department_id)
		SELECT $1::uuid, d.id FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, app, depts); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_tokens t USING apps a WHERE t.app_id = a.id AND a.id = $1 AND NOT `+
		forToken, app); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) one(ctx context.Context, where string, arg string) (App, error) {
	list, err := s.list(ctx, where, arg)
	if err != nil {
		return App{}, err
	}
	if len(list) == 0 {
		return App{}, i18n.E("err.not_found")
	}
	return list[0], nil
}

func (s *Store) Get(ctx context.Context, id string) (App, error) {
	return s.one(ctx, "WHERE a.id::text = $1", id)
}

// Client finds the application by its code and checks its key.
func (s *Store) Client(ctx context.Context, clientID, secret string) (App, error) {
	a, err := s.one(ctx, "WHERE a.client_id = $1", clientID)
	if err != nil || subtle.ConstantTimeCompare([]byte(a.ClientSecret), []byte(secret)) != 1 {
		return App{}, ErrClient
	}
	return a, nil
}

// ByClientID finds the application by its code alone (/oauth/authorize, where there is no key).
func (s *Store) ByClientID(ctx context.Context, clientID string) (App, error) {
	a, err := s.one(ctx, "WHERE a.client_id = $1", clientID)
	if err != nil {
		return App{}, ErrClient
	}
	return a, nil
}

// Delete removes the application with all its tokens and places.
func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM apps WHERE id::text = $1`, id)
	return err
}

// NewSecret gives the application a new key; the old one stops working at once, with every token issued to it.
func (s *Store) NewSecret(ctx context.Context, id string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM app_tokens WHERE app_id::text = $1`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE apps SET client_secret = $2 WHERE id::text = $1`, id, randomHex(32))
	return err
}

// Issue gives an employee new tokens of the application (opening its page, installing it).
func (s *Store) Issue(ctx context.Context, appID, userID string) (Tokens, error) {
	return s.issue(ctx, appID, userID, RefreshTTL)
}

// issue gives tokens whose refresh lives ttl (an event's — as long as the access: the employee did not open anything).
func (s *Store) issue(ctx context.Context, appID, userID string, ttl time.Duration) (Tokens, error) {
	// tokens whose refresh is long gone are cleaned up on the way
	if _, err := s.db.Exec(ctx, `DELETE FROM app_tokens WHERE refresh_expires < now() - interval '1 day'`); err != nil {
		return Tokens{}, err
	}
	id, access, refresh := randomHex(6), randomHex(32), randomHex(32)
	t := Tokens{Access: accessPrefix + id + "_" + access, Refresh: refreshPrefix + id + "_" + refresh,
		Expires: time.Now().Add(AccessTTL).Truncate(time.Second), UserID: userID}
	_, err := s.db.Exec(ctx, `INSERT INTO app_tokens (token_id, app_id, user_id, access_hash, refresh_hash, access_expires, refresh_expires)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, appID, userID, hash(access), hash(refresh), t.Expires, time.Now().Add(ttl))
	return t, err
}

// Refresh exchanges a refresh token of the application for new tokens (the old ones stop working).
func (s *Store) Refresh(ctx context.Context, a App, token string) (Tokens, error) {
	id, secret, ok := parse(token, refreshPrefix)
	if !ok {
		return Tokens{}, ErrInvalid
	}
	var (
		userID string
		h      []byte
		active bool
	)
	err := s.db.QueryRow(ctx, `SELECT t.user_id, t.refresh_hash, t.refresh_expires > now() AND u.active AND `+forToken+`
		FROM app_tokens t JOIN apps a ON a.id = t.app_id JOIN users u ON u.id = t.user_id WHERE t.token_id = $1 AND t.app_id = $2`, id, a.ID).Scan(&userID, &h, &active)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!active || subtle.ConstantTimeCompare(hash(secret), h) != 1) {
		return Tokens{}, ErrInvalid
	}
	if err != nil {
		return Tokens{}, err
	}
	access, refresh := randomHex(32), randomHex(32)
	t := Tokens{Access: accessPrefix + id + "_" + access, Refresh: refreshPrefix + id + "_" + refresh,
		Expires: time.Now().Add(AccessTTL).Truncate(time.Second), UserID: userID}
	_, err = s.db.Exec(ctx, `UPDATE app_tokens SET access_hash = $2, refresh_hash = $3, access_expires = $4, refresh_expires = $5
		WHERE token_id = $1`, id, hash(access), hash(refresh), t.Expires, time.Now().Add(RefreshTTL))
	return t, err
}

// NewCode is an authorization code of /oauth/authorize for the employee, valid a minute.
func (s *Store) NewCode(ctx context.Context, appID, userID string) (string, error) {
	code := randomHex(16)
	_, err := s.db.Exec(ctx, `INSERT INTO app_codes (code_hash, app_id, user_id, expires_at) VALUES ($1, $2, $3, $4)`,
		hash(code), appID, userID, time.Now().Add(CodeTTL))
	return code, err
}

// Exchange turns an authorization code into tokens, once.
func (s *Store) Exchange(ctx context.Context, a App, code string) (Tokens, error) {
	if _, err := s.db.Exec(ctx, `DELETE FROM app_codes WHERE expires_at < now()`); err != nil {
		return Tokens{}, err
	}
	var userID string
	err := s.db.QueryRow(ctx, `DELETE FROM app_codes WHERE code_hash = $1 AND app_id = $2 AND expires_at >= now() RETURNING user_id`,
		hash(code), a.ID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Tokens{}, ErrInvalid
	}
	if err != nil {
		return Tokens{}, err
	}
	return s.Issue(ctx, a.ID, userID)
}

// IsToken: the token is an application's access token (the REST layer asks this store, not the keys').
func IsToken(token string) bool { return strings.HasPrefix(token, accessPrefix) }

// Authenticate checks an access token: the employee acts in REST with the sections of the application, while it is
// for them («Кому доступно»).
func (s *Store) Authenticate(ctx context.Context, token, ip string) (apikeys.Principal, error) {
	id, secret, ok := parse(token, accessPrefix)
	if !ok {
		return apikeys.Principal{}, ErrInvalid
	}
	var (
		p      apikeys.Principal
		h      []byte
		live   bool
		client string
	)
	err := s.db.QueryRow(ctx, `SELECT t.user_id, t.access_hash, t.access_expires > now() AND u.active AND a.installed_at IS NOT NULL
		AND `+forToken+`, a.scopes, a.client_id FROM app_tokens t JOIN apps a ON a.id = t.app_id JOIN users u ON u.id = t.user_id
		WHERE t.token_id = $1`, id).Scan(&p.UserID, &h, &live, &p.Scopes, &client)
	if errors.Is(err, pgx.ErrNoRows) {
		return apikeys.Principal{}, ErrInvalid
	}
	if err != nil {
		return apikeys.Principal{}, err
	}
	if !live || subtle.ConstantTimeCompare(hash(secret), h) != 1 {
		return apikeys.Principal{}, ErrInvalid
	}
	p.KeyID = "app:" + client
	_, _ = s.db.Exec(ctx, `UPDATE app_tokens SET last_used_at = now(), last_used_ip = $2 WHERE token_id = $1`, id, ip)
	return p, nil
}

// Auth is the «auth» block go24 gives an application with its tokens: in the answer of /oauth/token
// and in the events.
func Auth(a App, t Tokens, base string) map[string]any {
	u, _ := url.Parse(base)
	host := ""
	if u != nil {
		host = u.Host
	}
	return map[string]any{
		"access_token": t.Access, "refresh_token": t.Refresh, "expires": t.Expires.Unix(),
		"expires_in": int(time.Until(t.Expires).Round(time.Second).Seconds()), "scope": strings.Join(a.Scopes, ","),
		"domain": host, "client_endpoint": strings.TrimRight(base, "/") + "/api/v1/",
		"server_endpoint": strings.TrimRight(base, "/") + "/oauth/", "member_id": MemberID(base), "user_id": t.UserID,
		"status": "L", "application_token": a.ApplicationToken,
	}
}

// MemberID is the go24's own identifier for applications (member_id): the same for one address.
func MemberID(base string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(base, "/")))
	return hex.EncodeToString(sum[:16])
}

// Install sends ONAPPINSTALL with the installer's tokens to the install handler (or, without one, the page's
// handler) and marks the application installed when the handler answers 2xx; an application with neither is
// installed at once.
func (s *Store) Install(ctx context.Context, a App, userID, base, lang string) error {
	target := a.InstallURL
	if target == "" {
		target = a.HandlerURL
	}
	status, msg := 0, ""
	if target != "" {
		t, err := s.Issue(ctx, a.ID, userID)
		if err != nil {
			return err
		}
		body, _ := json.Marshal(map[string]any{"event": "ONAPPINSTALL", "event_handler_id": 0, "ts": time.Now().Unix(),
			"data": map[string]any{"VERSION": 1, "LANGUAGE_ID": lang}, "auth": Auth(a, t, base)})
		status, err = s.post(ctx, target, a.ClientSecret, "ONAPPINSTALL", body)
		switch {
		case err != nil:
			msg = err.Error()
		case status >= 300:
			msg = "HTTP " + strconv.Itoa(status)
		}
	}
	var st *int
	if status > 0 {
		st = &status
	}
	if _, err := s.db.Exec(ctx, `UPDATE apps SET install_status = $2, install_error = left($3, 300),
		installed_at = CASE WHEN $3 = '' THEN coalesce(installed_at, now()) ELSE installed_at END WHERE id = $1`, a.ID, st, msg); err != nil {
		return err
	}
	if msg != "" {
		return i18n.E("err.app_install", msg)
	}
	return nil
}

// Uninstall takes the application off: its tokens stop working, employees no longer see it, its places are taken
// off the cards, ONAPPUNINSTALL goes to its handlers and its subscriptions to events are dropped.
func (s *Store) Uninstall(ctx context.Context, id, base, lang string) error {
	if a, err := s.Get(ctx, id); err == nil && a.Installed() {
		s.uninstalled(ctx, a, base, lang)
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM app_events WHERE app_id::text = $1`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM app_tokens WHERE app_id::text = $1`, id); err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM app_placements WHERE app_id::text = $1`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `UPDATE apps SET installed_at = NULL, install_status = NULL, install_error = '' WHERE id::text = $1`, id)
	return err
}

func (s *Store) post(ctx context.Context, target, secret, event string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "go24-app")
	req.Header.Set("X-Go24-Event", event)
	req.Header.Set("X-Go24-Signature", hooks.Sign(secret, body))
	res, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	return res.StatusCode, nil
}

func parse(token, prefix string) (id, secret string, ok bool) {
	rest, found := strings.CutPrefix(token, prefix)
	if !found {
		return "", "", false
	}
	id, secret, found = strings.Cut(rest, "_")
	if !found || len(id) != 12 || len(secret) != 64 || !isHex(id) || !isHex(secret) {
		return "", "", false
	}
	return id, secret, true
}

func hash(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
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
