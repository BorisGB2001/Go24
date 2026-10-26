package calls

import (
	"bytes"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- External services of calls: Zoom, Яндекс Телемост, VK Звонки (by their API), Telegram, Max (links) ----------

// Services in the order they are offered.
var Services = []string{"zoom", "telemost", "vk", "telegram", "max"}

// APIServices make a meeting by their API with the company's keys; the others only by an employee's own link.
var APIServices = map[string]bool{"zoom": true, "telemost": true, "vk": true}

// ServiceConfig is a service as the administrators set it. Secret is never shown back: HasSecret tells it is set.
type ServiceConfig struct {
	Service   string
	Enabled   bool
	AccountID string // Zoom: the account of the Server-to-Server OAuth app
	ClientID  string // Zoom: its client
	GroupID   string // VK: the community the calls are made by ("" — the token's user)
	HasSecret bool
	secret    string
}

// Ready: the service may make a meeting by its API.
func (c ServiceConfig) Ready() bool {
	if !c.Enabled || !APIServices[c.Service] || c.secret == "" {
		return false
	}
	return c.Service != "zoom" || c.AccountID != "" && c.ClientID != ""
}

// Available is a service an employee may make a meeting with: by the API, or by their own link.
type Available struct {
	Service string
	API     bool
	Link    string
}

// ErrNoService: the service is off, or it has neither keys nor the employee's own link.
var ErrNoService = errors.New("calls: the service is not available")

// External keeps the services' settings and makes meetings. The API addresses are fields for tests.
type External struct {
	DB                                     *pgxpool.Pool
	HTTP                                   *http.Client
	ZoomAPI, ZoomOAuth, TelemostAPI, VKAPI string
	key                                    []byte

	mu   sync.Mutex
	zoom struct {
		token   string
		expires time.Time
		account string
	}
}

// NewExternal: secret is go24's own secret (the session secret); the key of the services' secrets is derived from it.
func NewExternal(db *pgxpool.Pool, secret string) *External {
	k := sha256.Sum256([]byte("go24 call services\x00" + secret))
	return &External{DB: db, key: k[:], ZoomAPI: "https://api.zoom.us/v2", ZoomOAuth: "https://zoom.us/oauth/token",
		TelemostAPI: "https://cloud-api.yandex.net/v1/telemost-api", VKAPI: "https://api.vk.com/method"}
}

func (x *External) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(x.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (x *External) seal(plain string) ([]byte, error) {
	g, err := x.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(plain), nil), nil
}

func (x *External) open(sealed []byte) (string, error) {
	g, err := x.gcm()
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("calls: bad secret")
	}
	plain, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
	return string(plain), err
}

// Configs are all the services, those never set off.
func (x *External) Configs(ctx context.Context) ([]ServiceConfig, error) {
	rows, err := x.DB.Query(ctx, `SELECT service, enabled, config, secret FROM call_services`)
	if err != nil {
		return nil, err
	}
	set := map[string]ServiceConfig{}
	for rows.Next() {
		var c ServiceConfig
		var raw []byte
		var sealed []byte
		if err := rows.Scan(&c.Service, &c.Enabled, &raw, &sealed); err != nil {
			rows.Close()
			return nil, err
		}
		var conf map[string]string
		_ = json.Unmarshal(raw, &conf)
		c.AccountID, c.ClientID, c.GroupID = conf["account_id"], conf["client_id"], conf["group_id"]
		if len(sealed) > 0 {
			if c.secret, err = x.open(sealed); err == nil {
				c.HasSecret = c.secret != ""
			}
		}
		set[c.Service] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]ServiceConfig, 0, len(Services))
	for _, s := range Services {
		c, ok := set[s]
		if !ok {
			c.Service = s
		}
		out = append(out, c)
	}
	return out, nil
}

func (x *External) config(ctx context.Context, service string) (ServiceConfig, error) {
	all, err := x.Configs(ctx)
	if err != nil {
		return ServiceConfig{}, err
	}
	for _, c := range all {
		if c.Service == service {
			return c, nil
		}
	}
	return ServiceConfig{}, ErrNoService
}

// SaveConfig sets a service; secret "" keeps the one set (clear — removes it).
func (x *External) SaveConfig(ctx context.Context, userID string, c ServiceConfig, secret string, clear bool) error {
	if !slices.Contains(Services, c.Service) {
		return ErrNoService
	}
	conf, _ := json.Marshal(map[string]string{"account_id": strings.TrimSpace(c.AccountID), "client_id": strings.TrimSpace(c.ClientID),
		"group_id": strings.TrimSpace(c.GroupID)})
	var sealed []byte
	if secret = strings.TrimSpace(secret); secret != "" {
		var err error
		if sealed, err = x.seal(secret); err != nil {
			return err
		}
	}
	_, err := x.DB.Exec(ctx, `INSERT INTO call_services (service, enabled, config, secret, updated_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (service) DO UPDATE SET enabled = EXCLUDED.enabled, config = EXCLUDED.config, updated_by = EXCLUDED.updated_by,
			updated_at = now(), secret = CASE WHEN $6 THEN NULL ELSE coalesce(EXCLUDED.secret, call_services.secret) END`,
		c.Service, c.Enabled, conf, sealed, userID, clear)
	if err == nil && c.Service == "zoom" {
		x.mu.Lock()
		x.zoom.token = "" // other keys: another token
		x.mu.Unlock()
	}
	return err
}

// Links are the employee's own links by service.
func (x *External) Links(ctx context.Context, userID string) (map[string]string, error) {
	rows, err := x.DB.Query(ctx, `SELECT service, url FROM user_call_links WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		var s, u string
		if err := rows.Scan(&s, &u); err != nil {
			rows.Close()
			return nil, err
		}
		out[s] = u
	}
	rows.Close()
	return out, rows.Err()
}

// ErrBadLink: a link of a service is an https address (or http).
var ErrBadLink = errors.New("calls: a link is an http(s) address")

// SetLink keeps the employee's own link of the service ("" — removes it).
func (x *External) SetLink(ctx context.Context, userID, service, link string) error {
	if !slices.Contains(Services, service) {
		return ErrNoService
	}
	link = strings.TrimSpace(link)
	if link == "" {
		_, err := x.DB.Exec(ctx, `DELETE FROM user_call_links WHERE user_id = $1 AND service = $2`, userID, service)
		return err
	}
	if u, err := url.Parse(link); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || len(link) > 1000 {
		return ErrBadLink
	}
	_, err := x.DB.Exec(ctx, `INSERT INTO user_call_links (user_id, service, url) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, service) DO UPDATE SET url = EXCLUDED.url, updated_at = now()`, userID, service, link)
	return err
}

// Available are the services the employee may make a meeting with now, in the order of Services.
func (x *External) Available(ctx context.Context, userID string) ([]Available, error) {
	all, err := x.Configs(ctx)
	if err != nil {
		return nil, err
	}
	links, err := x.Links(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []Available
	for _, c := range all {
		if !c.Enabled {
			continue
		}
		if a := (Available{Service: c.Service, API: c.Ready(), Link: links[c.Service]}); a.API || a.Link != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// Meeting is a meeting of an external service.
type Meeting struct {
	Service, URL, ID string
	Own              bool // the employee's own link, not a new meeting
}

// Meet makes a meeting of the service for the employee (email — theirs, the host in Zoom, the co-host in Телемост):
// by its API when the company has the keys, otherwise their own link.
func (x *External) Meet(ctx context.Context, userID, email, service, title string) (Meeting, error) {
	c, err := x.config(ctx, service)
	if err != nil {
		return Meeting{}, err
	}
	if !c.Enabled {
		return Meeting{}, ErrNoService
	}
	if c.Ready() {
		var m Meeting
		switch service {
		case "zoom":
			m, err = x.zoomMeeting(ctx, c, email, title)
		case "telemost":
			m, err = x.telemostMeeting(ctx, c, email)
		case "vk":
			m, err = x.vkMeeting(ctx, c)
		}
		m.Service = service
		return m, err
	}
	links, err := x.Links(ctx, userID)
	if err != nil {
		return Meeting{}, err
	}
	if links[service] == "" {
		return Meeting{}, ErrNoService
	}
	return Meeting{Service: service, URL: links[service], Own: true}, nil
}

func (x *External) client() *http.Client {
	if x.HTTP != nil {
		return x.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// call sends a request and reads the JSON answer; a status other than 2xx is an error with what the service said.
func (x *External) call(req *http.Request, out any) (int, error) {
	res, err := x.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode/100 != 2 {
		return res.StatusCode, fmt.Errorf("%s: %s %s", req.URL.Host, res.Status, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	return res.StatusCode, json.Unmarshal(raw, out)
}

// zoomToken is the token of the company's Server-to-Server OAuth app (an hour long; kept until shortly before).
func (x *External) zoomToken(ctx context.Context, c ServiceConfig) (string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.zoom.token != "" && x.zoom.account == c.AccountID && time.Now().Before(x.zoom.expires) {
		return x.zoom.token, nil
	}
	q := url.Values{"grant_type": {"account_credentials"}, "account_id": {c.AccountID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.ZoomOAuth+"?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.ClientID, c.secret)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if _, err := x.call(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("zoom: no access token")
	}
	x.zoom.token, x.zoom.account = out.AccessToken, c.AccountID
	x.zoom.expires = time.Now().Add(time.Duration(max(out.ExpiresIn-120, 60)) * time.Second)
	return out.AccessToken, nil
}

// zoomMeeting makes an instant meeting hosted by the employee when the Zoom account has them, else by its owner.
func (x *External) zoomMeeting(ctx context.Context, c ServiceConfig, email, title string) (Meeting, error) {
	tok, err := x.zoomToken(ctx, c)
	if err != nil {
		return Meeting{}, err
	}
	body, _ := json.Marshal(map[string]any{"topic": title, "type": 1, "settings": map[string]any{"join_before_host": true}})
	hosts := []string{"me"}
	if email != "" {
		hosts = []string{email, "me"}
	}
	var last error
	for _, host := range hosts {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.ZoomAPI+"/users/"+url.PathEscape(host)+"/meetings", bytes.NewReader(body))
		if err != nil {
			return Meeting{}, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		var out struct {
			ID      json.Number `json:"id"`
			JoinURL string      `json:"join_url"`
		}
		status, err := x.call(req, &out)
		if err == nil {
			return Meeting{URL: out.JoinURL, ID: out.ID.String()}, nil
		}
		last = err
		if status != http.StatusNotFound && status != http.StatusBadRequest { // not «no such user»: no use asking as the owner
			break
		}
	}
	return Meeting{}, last
}

// telemostMeeting makes a meeting of Телемост anyone with the link joins, the employee its co-host.
func (x *External) telemostMeeting(ctx context.Context, c ServiceConfig, email string) (Meeting, error) {
	in := map[string]any{"waiting_room_level": "PUBLIC"}
	if email != "" {
		in["cohosts"] = []map[string]string{{"email": email}}
	}
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.TelemostAPI+"/conferences", bytes.NewReader(body))
	if err != nil {
		return Meeting{}, err
	}
	req.Header.Set("Authorization", "OAuth "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		ID      string `json:"id"`
		JoinURL string `json:"join_url"`
	}
	if _, err := x.call(req, &out); err != nil {
		return Meeting{}, err
	}
	return Meeting{URL: out.JoinURL, ID: out.ID}, nil
}

// vkVersion is the version of VK's API go24 speaks.
const vkVersion = "5.199"

// vkMeeting starts a call of VK Звонки (by the community when one is set); anyone with the link joins.
func (x *External) vkMeeting(ctx context.Context, c ServiceConfig) (Meeting, error) {
	q := url.Values{"access_token": {c.secret}, "v": {vkVersion}}
	if c.GroupID != "" {
		id, err := strconv.ParseInt(strings.TrimPrefix(c.GroupID, "-"), 10, 64)
		if err != nil {
			return Meeting{}, fmt.Errorf("vk: bad community %q", c.GroupID)
		}
		q.Set("group_id", strconv.FormatInt(-id, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, x.VKAPI+"/calls.start", strings.NewReader(q.Encode()))
	if err != nil {
		return Meeting{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		Response struct {
			JoinLink string      `json:"join_link"`
			CallID   json.Number `json:"call_id"`
		} `json:"response"`
		Error *struct {
			Code int    `json:"error_code"`
			Msg  string `json:"error_msg"`
		} `json:"error"`
	}
	if _, err := x.call(req, &out); err != nil {
		return Meeting{}, err
	}
	if out.Error != nil {
		return Meeting{}, fmt.Errorf("vk: %d %s", out.Error.Code, out.Error.Msg)
	}
	if out.Response.JoinLink == "" {
		return Meeting{}, errors.New("vk: no join link")
	}
	return Meeting{URL: out.Response.JoinLink, ID: out.Response.CallID.String()}, nil
}
