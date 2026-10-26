// Package matrix talks to the go24 Matrix server (Synapse): the client-server API as the go24 bot, and the
// Synapse admin API for provisioning employees. The bot is a server admin; its token is a secret (never logged).
package matrix

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	Base       string // http://synapse:8008
	ServerName string // go24.lan
	Token      string
	HTTP       *http.Client
}

func New(base, serverName, token string) *Client {
	if base == "" || serverName == "" || token == "" {
		return nil
	}
	return &Client{Base: strings.TrimRight(base, "/"), ServerName: serverName, Token: token, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// Error is a Matrix error answer (errcode M_FORBIDDEN, M_NOT_FOUND…).
type Error struct {
	Status     int
	Code       string `json:"errcode"`
	Message    string `json:"error"`
	RetryAfter int    `json:"retry_after_ms"` // M_LIMIT_EXCEEDED
}

func (e *Error) Error() string {
	return fmt.Sprintf("matrix: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func IsCode(err error, code string) bool {
	e, ok := err.(*Error)
	return ok && e.Code == code
}

// Do calls the API with the bot token (or token, when not empty: an employee's puppet token).
func (c *Client) Do(ctx context.Context, method, path string, body, out any, token ...string) error {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return err
		}
	}
	tok := c.Token
	if len(token) > 0 && token[0] != "" {
		tok = token[0]
	}
	// Synapse limits how fast one account may write (429 M_LIMIT_EXCEEDED with retry_after_ms): wait and repeat,
	// a few times, rather than failing a message someone just wrote.
	for attempt := 0; ; attempt++ {
		var rd io.Reader
		if b != nil {
			rd = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rd)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("matrix %s %s: %w", method, strings.SplitN(path, "?", 2)[0], err)
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		res.Body.Close()
		if res.StatusCode >= 300 {
			e := &Error{Status: res.StatusCode}
			_ = json.Unmarshal(data, e)
			if res.StatusCode == http.StatusTooManyRequests && attempt < 5 {
				wait := time.Duration(min(max(e.RetryAfter, 200), 10000)) * time.Millisecond
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
				continue
			}
			return e
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
}

// NoRatelimit lifts Synapse's message rate limit for an account (the go24 bot relays everyone's notifications).
func (c *Client) NoRatelimit(ctx context.Context, mxid string) error {
	return c.Do(ctx, http.MethodPost, "/_synapse/admin/v1/users/"+esc(mxid)+"/override_ratelimit",
		map[string]int{"messages_per_second": 0, "burst_count": 0}, nil)
}

// WhoAmI is the account of the token.
func (c *Client) WhoAmI(ctx context.Context) (string, error) {
	var out struct {
		UserID string `json:"user_id"`
	}
	err := c.Do(ctx, http.MethodGet, "/_matrix/client/v3/account/whoami", nil, &out)
	return out.UserID, err
}

// UserID builds @localpart:server.
func (c *Client) UserID(localpart string) string { return "@" + localpart + ":" + c.ServerName }

func esc(s string) string { return url.PathEscape(s) }

// RegisterAdmin creates the bot account with the shared registration secret and returns its access token.
// Used once at installation (cmd/go24-matrix-bot).
func RegisterAdmin(ctx context.Context, base, secret, localpart, displayName string) (string, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	var nonce struct {
		Nonce string `json:"nonce"`
	}
	res, err := hc.Get(strings.TrimRight(base, "/") + "/_synapse/admin/v1/register")
	if err != nil {
		return "", err
	}
	err = json.NewDecoder(res.Body).Decode(&nonce)
	res.Body.Close()
	if err != nil || nonce.Nonce == "" {
		return "", fmt.Errorf("matrix: no registration nonce (is registration_shared_secret set?)")
	}
	pw := randomHex(24) // password login is disabled on the server; the password is never used
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(nonce.Nonce + "\x00" + localpart + "\x00" + pw + "\x00admin"))
	body, _ := json.Marshal(map[string]any{"nonce": nonce.Nonce, "username": localpart, "displayname": displayName,
		"password": pw, "admin": true, "mac": hex.EncodeToString(mac.Sum(nil))})
	res, err = hc.Post(strings.TrimRight(base, "/")+"/_synapse/admin/v1/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		Token string `json:"access_token"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode >= 300 || out.Token == "" {
		return "", fmt.Errorf("matrix: register %s: HTTP %d %s", localpart, res.StatusCode, out.Error)
	}
	return out.Token, nil
}
