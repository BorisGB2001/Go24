package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// RC talks to `rclone rcd` (remote control API) to manage user-connected storages and move files.
//
// Access to the rc API equals shell access as the rclone user, so: rcd is reachable only from go24
// (internal network, basic auth), and go24 never passes user input as an fs name — fs names are always
// "<remote>:" with the remote name taken from the database.
type RC struct {
	base, user, pass string
	hc               *http.Client
}

func NewRC(baseURL, user, pass string) *RC {
	return &RC{base: strings.TrimRight(baseURL, "/"), user: user, pass: pass, hc: &http.Client{Timeout: 2 * time.Minute}}
}

// RCError is an error reported by rclone itself (bad credentials, unreachable host…).
type RCError struct{ Msg string }

func (e *RCError) Error() string { return e.Msg }

func (c *RC) call(ctx context.Context, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)
	return c.do(req, out)
}

func (c *RC) do(req *http.Request, out any) error {
	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return &RCError{Msg: e.Error}
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// Ping asks rclone whether it answers («Состояние системы»).
func (c *RC) Ping(ctx context.Context) error {
	return c.call(ctx, "rc/noop", map[string]any{}, nil)
}

// CreateRemote adds a remote to rclone's config; secrets are obscured by rclone.
func (c *RC) CreateRemote(ctx context.Context, name, typ string, params map[string]string) error {
	return c.call(ctx, "config/create", map[string]any{
		"name": name, "type": typ, "parameters": params,
		"opt": map[string]any{"obscure": true, "nonInteractive": true},
	}, nil)
}

func (c *RC) DeleteRemote(ctx context.Context, name string) error {
	return c.call(ctx, "config/delete", map[string]any{"name": name}, nil)
}

// Check lists the root of the storage to prove that the connection and credentials work.
func (c *RC) Check(ctx context.Context, fs string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.call(ctx, "operations/list", map[string]any{"fs": fs, "remote": "", "opt": map[string]any{"dirsOnly": true}}, nil)
}

// Mkdir creates dir (and parents) on the remote; for S3 the first segment is the bucket.
func (c *RC) Mkdir(ctx context.Context, remote, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.call(ctx, "operations/mkdir", map[string]any{"fs": remote + ":", "remote": dir}, nil)
}

// Upload streams one file into dir on fs.
func (c *RC) Upload(ctx context.Context, fs, dir, name string, r io.Reader) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	q := url.Values{"fs": {fs}, "remote": {dir}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/operations/uploadfile?"+q.Encode(), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetBasicAuth(c.user, c.pass)
	return c.do(req, nil)
}

// Open reads a file via rcd's --rc-serve: GET /[fs]/path.
func (c *RC) Open(ctx context.Context, fs, p string) (io.ReadCloser, error) {
	u := c.base + "/[" + escapeSegments(fs) + "]/" + escapeSegments(strings.TrimPrefix(path.Clean("/"+p), "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	res, err := (&http.Client{}).Do(req) // no overall timeout: large downloads stream
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, &RCError{Msg: fmt.Sprintf("open %s: %s", p, res.Status)}
	}
	return res.Body, nil
}

func (c *RC) Delete(ctx context.Context, fs, p string) error {
	return c.call(ctx, "operations/deletefile", map[string]any{"fs": fs, "remote": p}, nil)
}

// Purge removes the folder p of fs with all it holds.
func (c *RC) Purge(ctx context.Context, fs, p string) error {
	return c.call(ctx, "operations/purge", map[string]any{"fs": fs, "remote": p}, nil)
}

// CopyDir copies the folder srcFs into dstFs (both rclone fs strings: a path of rcd's own disk or "remote:path") as a
// job of rcd, waiting for it: a big copy outlasts any one request.
func (c *RC) CopyDir(ctx context.Context, srcFs, dstFs string) error {
	var job struct {
		JobID int64 `json:"jobid"`
	}
	if err := c.call(ctx, "sync/copy", map[string]any{"srcFs": srcFs, "dstFs": dstFs, "createEmptySrcDirs": true, "_async": true}, &job); err != nil {
		return err
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = c.call(context.WithoutCancel(ctx), "job/stop", map[string]any{"jobid": job.JobID}, nil)
			return ctx.Err()
		case <-t.C:
		}
		var st struct {
			Finished bool   `json:"finished"`
			Success  bool   `json:"success"`
			Error    string `json:"error"`
		}
		if err := c.call(ctx, "job/status", map[string]any{"jobid": job.JobID}, &st); err != nil {
			return err
		}
		if st.Finished {
			if !st.Success {
				return &RCError{Msg: st.Error}
			}
			return nil
		}
	}
}

// escapeSegments URL-escapes every path segment and keeps the "/" separators (":" is legal in a segment).
func escapeSegments(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
