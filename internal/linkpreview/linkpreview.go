// Package linkpreview makes the card under a chat message with a link, as the portal messenger does: the title,
// the description and the picture of the page from its Open Graph (og:*, twitter:*) or <title>. go24 fetches the page
// itself, from addresses of the internet only (never its own network), and keeps the cards in memory for a while.
package linkpreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

// Preview is the card of one page; an empty Title means the page has nothing to show.
type Preview struct {
	URL   string `json:"url"`
	Site  string `json:"site"`
	Title string `json:"title"`
	Text  string `json:"text,omitempty"`
	Image string `json:"image,omitempty"` // an absolute http(s) address of the picture
}

const (
	pageMax  = 512 << 10 // the head of a page is enough for its meta tags
	imageMax = 5 << 20
	keep     = 6 * time.Hour
	cacheMax = 2000
)

// Fetcher fetches pages and pictures; the zero value is not ready, use New.
type Fetcher struct {
	Client *http.Client
	mu     sync.Mutex
	cache  map[string]cached
	images map[string]time.Time // pictures of the cards given out: only they go through Image
}

type cached struct {
	p   Preview
	err error
	at  time.Time
}

// New: a fetcher that reaches addresses of the internet only.
func New() *Fetcher {
	return &Fetcher{Client: &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, Control: PublicOnly}).DialContext,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second,
	}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || (req.URL.Scheme != "http" && req.URL.Scheme != "https") {
			return http.ErrUseLastResponse
		}
		return nil
	}}, cache: map[string]cached{}, images: map[string]time.Time{}}
}

// PublicOnly is the dialer's check: the address being dialed (after DNS and on each redirect) must be of the internet.
func PublicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip) {
		return fmt.Errorf("%s is not an address of the internet", host)
	}
	return nil
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

var ErrBadURL = errors.New("linkpreview: not an http(s) link")

// Check: an absolute http(s) link with a host.
func Check(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, ErrBadURL
	}
	u.Fragment = ""
	return u, nil
}

// Get returns the card of the page, from the cache when it was fetched lately (failures are kept too, so a dead
// site is not asked again on every message).
func (f *Fetcher) Get(ctx context.Context, raw string) (Preview, error) {
	u, err := Check(raw)
	if err != nil {
		return Preview{}, err
	}
	key := u.String()
	f.mu.Lock()
	c, ok := f.cache[key]
	f.mu.Unlock()
	if ok && time.Since(c.at) < keep {
		return c.p, c.err
	}
	p, err := f.fetch(ctx, u)
	f.mu.Lock()
	if len(f.cache) >= cacheMax {
		for k, v := range f.cache {
			if time.Since(v.at) >= keep || len(f.cache) >= cacheMax {
				delete(f.cache, k)
			}
		}
	}
	f.cache[key] = cached{p, err, time.Now()}
	if p.Image != "" {
		if len(f.images) >= cacheMax {
			clear(f.images)
		}
		f.images[p.Image] = time.Now()
	}
	f.mu.Unlock()
	return p, err
}

func (f *Fetcher) fetch(ctx context.Context, u *url.URL) (Preview, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return Preview{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; go24-linkpreview)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := f.Client.Do(req)
	if err != nil {
		return Preview{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Preview{}, fmt.Errorf("linkpreview: %s", resp.Status)
	}
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if ct != "text/html" && ct != "application/xhtml+xml" {
		return Preview{}, fmt.Errorf("linkpreview: %s is not a page", ct)
	}
	body, err := charset.NewReader(io.LimitReader(resp.Body, pageMax), resp.Header.Get("Content-Type"))
	if err != nil {
		return Preview{}, err
	}
	return Parse(body, resp.Request.URL), nil
}

// Parse reads the card from the page; base is its address, for relative pictures.
func Parse(r io.Reader, base *url.URL) Preview {
	meta := map[string]string{}
	var title strings.Builder
	z := html.NewTokenizer(r)
	inTitle := false
head:
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		t := z.Token()
		switch {
		case tt == html.StartTagToken && t.Data == "title":
			inTitle = true
		case tt == html.EndTagToken && t.Data == "title":
			inTitle = false
		case tt == html.TextToken && inTitle:
			title.WriteString(t.Data)
		case (tt == html.StartTagToken || tt == html.SelfClosingTagToken) && t.Data == "meta":
			var k, v string
			for _, a := range t.Attr {
				switch a.Key {
				case "property", "name":
					k = strings.ToLower(a.Val)
				case "content":
					v = a.Val
				}
			}
			if k != "" && meta[k] == "" {
				meta[k] = strings.TrimSpace(v)
			}
		case tt == html.StartTagToken && t.Data == "body":
			break head // the meta tags are in the head
		}
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := meta[k]; v != "" {
				return v
			}
		}
		return ""
	}
	p := Preview{URL: base.String(), Site: first("og:site_name"),
		Title: clip(first("og:title", "twitter:title"), 200), Text: clip(first("og:description", "twitter:description", "description"), 300)}
	if p.Title == "" {
		p.Title = clip(strings.TrimSpace(title.String()), 200)
	}
	if p.Site == "" {
		p.Site = strings.TrimPrefix(base.Hostname(), "www.")
	}
	if img := first("og:image:secure_url", "og:image", "og:image:url", "twitter:image", "twitter:image:src"); img != "" {
		if iu, err := base.Parse(img); err == nil && (iu.Scheme == "http" || iu.Scheme == "https") {
			p.Image = iu.String()
		}
	}
	return p
}

// clip squeezes the spaces and cuts the text to n letters.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

var ErrNotCard = errors.New("linkpreview: not a picture of a card")

// Image fetches the picture of a card for the browser, so it never goes to the site itself; only pictures of the
// cards given out lately, so go24 is no open proxy.
func (f *Fetcher) Image(ctx context.Context, raw string) ([]byte, string, error) {
	u, err := Check(raw)
	if err != nil {
		return nil, "", err
	}
	f.mu.Lock()
	at, ok := f.images[raw]
	f.mu.Unlock()
	if !ok || time.Since(at) >= keep {
		return nil, "", ErrNotCard
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; go24-linkpreview)")
	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	ct, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(ct, "image/") || ct == "image/svg+xml" {
		return nil, "", fmt.Errorf("linkpreview: not a picture (%s, %s)", resp.Status, ct)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, imageMax+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > imageMax {
		return nil, "", errors.New("linkpreview: the picture is too big")
	}
	return b, ct, nil
}
