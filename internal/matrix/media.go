package matrix

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Upload puts a file into the media repository as the token's owner and returns its mxc:// address.
func (c *Client) Upload(ctx context.Context, token, name, contentType string, r io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/_matrix/media/v3/upload?filename="+url.QueryEscape(name), r)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("matrix upload: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		_ = json.Unmarshal(data, e)
		return "", e
	}
	var out struct {
		URI string `json:"content_uri"`
	}
	if err := json.Unmarshal(data, &out); err != nil || !strings.HasPrefix(out.URI, "mxc://") {
		return "", fmt.Errorf("matrix upload: no content_uri")
	}
	return out.URI, nil
}

// Download opens a file of the media repository (authenticated media, as the token's owner); the caller closes
// the body. Only mxc:// addresses of this server are fetched.
func (c *Client) Download(ctx context.Context, token, mxc string) (*http.Response, error) {
	server, id, ok := strings.Cut(strings.TrimPrefix(mxc, "mxc://"), "/")
	if !strings.HasPrefix(mxc, "mxc://") || !ok || server == "" || id == "" || strings.Contains(id, "/") {
		return nil, &Error{Status: http.StatusNotFound, Code: "M_NOT_FOUND"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"/_matrix/client/v1/media/download/"+esc(server)+"/"+esc(id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("matrix download: %w", err)
	}
	if res.StatusCode >= 300 {
		defer res.Body.Close()
		e := &Error{Status: res.StatusCode}
		_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(e)
		return nil, e
	}
	return res, nil
}
