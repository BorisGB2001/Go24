package sites

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"

	"go24/internal/i18n"
)

// Settings are the settings of a site: Page404 — the page shown for a missing address (its id; "" — the
// plain «not found»), Favicon — a link to the picture of the browser's tab, Metrika and Analytics — the ids of the
// counters of Яндекс Метрика and Google Analytics, Accent — the colour of links and buttons, Font — one of Fonts,
// Domain — its own domain, where it opens at the root.
type Settings struct {
	Page404, Favicon, Metrika, Analytics, Accent, Font, Domain string
}

// SEO is what a page tells search engines: its title (the page's title when empty), description (the site's when
// empty), keywords; NoIndex — do not index it.
type SEO struct {
	Title, Description, Keywords string
	NoIndex                      bool
}

// Fonts are the fonts of a site: "" is the system one; the titles are site.font.<key>.
var Fonts = []string{"", "serif", "sans", "rounded", "mono"}

var (
	metrikaRe   = regexp.MustCompile(`^[0-9]{1,12}$`)
	analyticsRe = regexp.MustCompile(`^(G|UA|GT|AW)-[A-Z0-9-]{4,24}$`)
	domainRe    = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{1,62}$`)
)

// CleanDomain is a domain as it is kept: lower case, without a scheme, a path and a port; ok false — not a domain.
func CleanDomain(d string) (string, bool) {
	d = strings.ToLower(strings.TrimSpace(d))
	for _, p := range []string{"https://", "http://"} {
		d = strings.TrimPrefix(d, p)
	}
	d, _, _ = strings.Cut(d, "/")
	d, _, _ = strings.Cut(d, ":")
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", true
	}
	return d, len(d) <= 253 && domainRe.MatchString(d)
}

// SaveSettings changes the settings of a site; a domain taken by another site is refused.
func (s *Store) SaveSettings(ctx context.Context, id string, set Settings) error {
	x, err := s.need(ctx, "", id, RoleFull)
	if err != nil {
		return err
	}
	if set.Page404 != "" {
		if _, err := s.Page(ctx, x.ID, set.Page404); err != nil {
			return err
		}
	}
	set.Favicon = SafeURL(set.Favicon)
	set.Metrika = strings.TrimSpace(set.Metrika)
	set.Analytics = strings.ToUpper(strings.TrimSpace(set.Analytics))
	if set.Metrika != "" && !metrikaRe.MatchString(set.Metrika) {
		return i18n.E("err.site_metrika")
	}
	if set.Analytics != "" && !analyticsRe.MatchString(set.Analytics) {
		return i18n.E("err.site_analytics")
	}
	if !colorRe.MatchString(set.Accent) {
		set.Accent = ""
	}
	if !validFont(set.Font) {
		set.Font = ""
	}
	var ok bool
	if set.Domain, ok = CleanDomain(set.Domain); !ok {
		return i18n.E("err.site_domain")
	}
	_, err = s.db.Exec(ctx, `UPDATE sites SET page404 = nullif($2, '')::uuid, favicon = $3, metrika = $4, analytics = $5, accent = $6,
		font = $7, domain = nullif($8, ''), updated_at = now() WHERE id = $1`,
		x.ID, set.Page404, set.Favicon, set.Metrika, set.Analytics, set.Accent, set.Font, set.Domain)
	if err != nil {
		if taken(err) != err {
			return i18n.E("err.site_domain_taken")
		}
		return err
	}
	domains.forget()
	return nil
}

func validFont(f string) bool {
	for _, x := range Fonts {
		if f == x {
			return true
		}
	}
	return false
}

// SavePageSEO changes what a page tells search engines.
func (s *Store) SavePageSEO(ctx context.Context, userID, siteID, id string, seo SEO) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	line := func(v string, n int) string {
		v = strings.Join(strings.Fields(v), " ")
		if r := []rune(v); len(r) > n {
			v = string(r[:n])
		}
		return v
	}
	tag, err := s.db.Exec(ctx, `UPDATE site_pages SET seo_title = $3, seo_description = $4, seo_keywords = $5, noindex = $6, updated_by = $7,
		updated_at = now() WHERE site_id::text = $1 AND id::text = $2`,
		siteID, id, line(seo.Title, 200), line(seo.Description, 500), line(seo.Keywords, 500), seo.NoIndex, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// domainCache: the address of the site of each own domain, read once a minute (and again after a change), so a
// request to go24 does not ask the database whether its host is a site.
type domainCache struct {
	mu    sync.Mutex
	at    time.Time
	slugs map[string]string
}

var domains = &domainCache{}

func (c *domainCache) forget() {
	c.mu.Lock()
	c.at = time.Time{}
	c.mu.Unlock()
}

// ByDomain is the address of the published site with its own domain host ("" — none).
func (s *Store) ByDomain(ctx context.Context, host string) string {
	domains.mu.Lock()
	defer domains.mu.Unlock()
	if time.Since(domains.at) > time.Minute {
		rows, err := s.db.Query(ctx, `SELECT domain, slug FROM sites WHERE domain IS NOT NULL AND published`)
		if err != nil {
			return ""
		}
		m := map[string]string{}
		for rows.Next() {
			var d, slug string
			if rows.Scan(&d, &slug) == nil {
				m[d] = slug
			}
		}
		rows.Close()
		domains.slugs, domains.at = m, time.Now()
	}
	return domains.slugs[host]
}
