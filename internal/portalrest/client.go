// Package portalrest reads a portal through an incoming webhook (REST), for the import into go24.
// It only reads: every method it calls is a *.get / *.list method.
package portalrest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client calls one portal. The webhook URL is a secret (it carries the token): it is never logged or printed.
type Client struct {
	base string
	http *http.Client
	last time.Time
	// Gap between calls: the portal allows about 2 requests per second per webhook.
	Gap time.Duration
}

func New(webhook string) (*Client, error) {
	webhook = strings.TrimSpace(webhook)
	if !strings.HasPrefix(webhook, "https://") || !strings.Contains(webhook, "/rest/") {
		return nil, fmt.Errorf("webhook: expected https://<portal>/rest/<user>/<token>/")
	}
	return &Client{base: strings.TrimRight(webhook, "/") + "/", http: &http.Client{Timeout: 60 * time.Second}, Gap: 550 * time.Millisecond}, nil
}

// Host is the portal address (safe to show).
func (c *Client) Host() string {
	h := strings.TrimPrefix(c.base, "https://")
	h, _, _ = strings.Cut(h, "/")
	return h
}

type response struct {
	Result    json.RawMessage `json:"result"`
	Next      *int            `json:"next"`
	Total     int             `json:"total"`
	Error     string          `json:"error"`
	ErrorDesc string          `json:"error_description"`
}

// Call runs one method. Errors name the method and the portal error code, never the URL.
func (c *Client) Call(ctx context.Context, method string, params any) (*response, error) {
	if wait := c.Gap - time.Since(c.last); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.last = time.Now()
	body, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+method+".json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: bad request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: portal unreachable", method) // the error text may contain the URL
	}
	defer res.Body.Close()
	var out response
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%s: HTTP %d, not JSON", method, res.StatusCode)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("%s: %s: %s", method, out.Error, out.ErrorDesc)
	}
	return &out, nil
}

// List pages through a list method (start/next) and decodes every item with each.
func List[T any](ctx context.Context, c *Client, method string, params map[string]any, each func(T) error) error {
	start := 0
	for {
		p := map[string]any{}
		for k, v := range params {
			p[k] = v
		}
		p["start"] = start
		res, err := c.Call(ctx, method, p)
		if err != nil {
			return err
		}
		var items []T
		if err := json.Unmarshal(res.Result, &items); err != nil {
			return fmt.Errorf("%s: unexpected result: %w", method, err)
		}
		for _, it := range items {
			if err := each(it); err != nil {
				return err
			}
		}
		if res.Next == nil {
			return nil
		}
		start = *res.Next
	}
}

// Batch runs up to 50 read calls at once ("name" → "method?query"), the way the portal recommends for many small
// calls; it returns each call's result by name (calls that failed are left out).
func (c *Client) Batch(ctx context.Context, cmds map[string]string) (map[string]json.RawMessage, error) {
	res, err := c.Call(ctx, "batch", map[string]any{"halt": 0, "cmd": cmds})
	if err != nil {
		return nil, err
	}
	var out struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(res.Result, &out); err != nil {
		// an empty result comes as [] instead of {}
		return map[string]json.RawMessage{}, nil
	}
	return out.Result, nil
}
