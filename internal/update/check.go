package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Repo is where the releases of go24 are published.
const Repo = "BorisGB2001/Go24"

// CheckEvery is how often go24 looks for a new release.
const CheckEvery = 6 * time.Hour

// Errors of the page's actions (their texts are locale keys).
var (
	ErrDev        = errors.New("update.err.dev")
	ErrBusy       = errors.New("update.err.busy")
	ErrNotNewer   = errors.New("update.err.not_newer")
	ErrNoRelease  = errors.New("update.err.no_release")
	ErrNoRollback = errors.New("update.err.no_rollback")
)

// Client is go24's side: it checks GitHub and hands the jobs to go24-updater through the updates' folder.
type Client struct {
	Dir     string       // the updates' folder (/updates)
	Current string       // the running version
	Off     bool         // GO24_UPDATE=off: a development server, updates come from git
	Key     string       // the releases' public key (PublicKey; tests set their own)
	API     string       // https://api.github.com (tests set their own)
	HTTP    *http.Client // nil — a client with a timeout
}

// Enabled says whether this installation may update itself by the button: not a development build.
func (c *Client) Enabled() bool { return !c.Off && !Dev(c.Current) && c.Dir != "" }

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// Latest is the latest release as last checked (nil — never checked).
func (c *Client) Latest() (*Release, error) {
	var r Release
	ok, err := readJSON(c.Dir, LatestFile, &r)
	if !ok || err != nil {
		return nil, err
	}
	return &r, nil
}

// Check reads the latest release from GitHub and keeps it in latest.json; a failure is kept there too (with the
// release seen before).
func (c *Client) Check(ctx context.Context) (*Release, error) {
	prev, _ := c.Latest()
	r, err := c.fetchLatest(ctx)
	if err != nil {
		if prev == nil {
			prev = &Release{}
		}
		prev.CheckedAt, prev.Error = time.Now(), err.Error()
		_ = writeJSON(c.Dir, LatestFile, prev)
		return prev, err
	}
	r.CheckedAt = time.Now()
	return r, writeJSON(c.Dir, LatestFile, r)
}

func (c *Client) fetchLatest(ctx context.Context) (*Release, error) {
	api := c.API
	if api == "" {
		api = "https://api.github.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"/repos/"+Repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "go24-updater")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("no releases")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: %s", resp.Status)
	}
	var gh struct {
		Tag       string    `json:"tag_name"`
		Body      string    `json:"body"`
		URL       string    `json:"html_url"`
		Published time.Time `json:"published_at"`
		Assets    []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&gh); err != nil {
		return nil, err
	}
	v, ok := ParseVersion(gh.Tag)
	if !ok {
		return nil, fmt.Errorf("release tag %q is not a version", gh.Tag)
	}
	r := &Release{Version: v.String(), Notes: gh.Body, URL: gh.URL, PublishedAt: gh.Published}
	name := "go24-" + r.Version + ".tar.gz"
	for _, a := range gh.Assets {
		switch a.Name {
		case name:
			r.Archive = a.URL
		case name + ".sig":
			r.Signature = a.URL
		}
	}
	return r, nil
}

// Loop checks GitHub at start and every CheckEvery, until ctx is done.
func (c *Client) Loop(ctx context.Context, onErr func(error)) {
	if !c.Enabled() {
		return
	}
	for t := time.NewTimer(time.Minute); ; t.Reset(CheckEvery) {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if _, err := c.Check(ctx); err != nil && ctx.Err() == nil && onErr != nil {
			onErr(err)
		}
	}
}

// Busy says whether go24-updater has a job: asked and not taken yet, or under way.
func (c *Client) Busy() bool {
	if _, err := os.Stat(filepath.Join(c.Dir, RequestFile)); err == nil {
		return true
	}
	st, _ := ReadStatus(c.Dir)
	return st.State == Installing
}

// Update fetches the latest release's archive and signature, checks them and asks go24-updater to install it.
func (c *Client) Update(ctx context.Context, by string) (Request, error) {
	switch {
	case !c.Enabled():
		return Request{}, ErrDev
	case c.Busy():
		return Request{}, ErrBusy
	}
	r, err := c.Latest()
	if err != nil {
		return Request{}, err
	}
	if r == nil || r.Version == "" || r.Archive == "" || r.Signature == "" {
		return Request{}, ErrNoRelease
	}
	if !Newer(r.Version, c.Current) {
		return Request{}, ErrNotNewer
	}
	data, err := c.download(ctx, r.Archive, 1<<30)
	if err != nil {
		return Request{}, err
	}
	sig, err := c.download(ctx, r.Signature, 4096)
	if err != nil {
		return Request{}, err
	}
	if err := Verify(c.Key, data, sig); err != nil {
		return Request{}, err
	}
	dir := filepath.Join(c.Dir, r.Version)
	if err := os.RemoveAll(dir); err != nil {
		return Request{}, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Request{}, err
	}
	name := "go24-" + r.Version + ".tar.gz"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return Request{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, name+".sig"), sig, 0o644); err != nil {
		return Request{}, err
	}
	req := Request{ID: newID(), Kind: "update", Version: r.Version, By: by, At: time.Now()}
	return req, writeJSON(c.Dir, RequestFile, req)
}

// Rollback asks go24-updater to put back the version kept before the last update, with its database.
func (c *Client) Rollback(by string) (Request, error) {
	switch {
	case !c.Enabled():
		return Request{}, ErrDev
	case c.Busy():
		return Request{}, ErrBusy
	}
	m, err := ReadRollback(c.Dir)
	if err != nil {
		return Request{}, err
	}
	if m == nil {
		return Request{}, ErrNoRollback
	}
	req := Request{ID: newID(), Kind: "rollback", Version: m.Version, By: by, At: time.Now()}
	return req, writeJSON(c.Dir, RequestFile, req)
}

func (c *Client) download(ctx context.Context, url string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "go24-updater")
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	var b bytes.Buffer
	if n, err := io.Copy(&b, io.LimitReader(resp.Body, max+1)); err != nil {
		return nil, err
	} else if n > max {
		return nil, fmt.Errorf("%s: too big", url)
	}
	return b.Bytes(), nil
}

// Announce returns the finished status the administrators have not been told of yet and marks it told (ok false —
// nothing new). The mark is a file of the updates' folder: a database put back by a rollback does not lose it.
func (c *Client) Announce() (Status, bool, error) {
	st, err := ReadStatus(c.Dir)
	if err != nil || !st.Done() || st.ID == "" {
		return st, false, err
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, AnnouncedFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return st, false, err
	}
	if strings.TrimSpace(string(b)) == st.ID {
		return st, false, nil
	}
	return st, true, os.WriteFile(filepath.Join(c.Dir, AnnouncedFile), []byte(st.ID+"\n"), 0o644)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}
