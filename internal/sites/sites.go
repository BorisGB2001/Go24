// Package sites is «Сайты»: sites made of pages, a page made of ready blocks (a cover, a text, a picture,
// advantages in columns, contacts, …) that are added from a library, edited, moved, hidden and removed; a published
// site with a published page opens to anyone at /site/{slug}/{page}.
package sites

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

var (
	ErrNotFound  = i18n.E("err.not_found")
	ErrForbidden = i18n.E("err.forbidden")
)

type Store struct{ db *pgxpool.Pool }

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Site is one site.
type Site struct {
	ID, Name, Slug, Description string
	Published                   bool
	CreatedBy, Author           string
	Created, Updated            time.Time
	Pages                       int
	HomeID                      string // its home page
	// the header and the footer: blocks on every page, above and below its own ones
	Header, Footer []Block
	Settings
	// «Права доступа»: the role of every employee; Role — the viewer's
	Everyone, Role Role
}

// Page is one page of a site; Blocks are filled by Page only.
type Page struct {
	ID, SiteID, Slug, Title string
	Home, Published         bool
	Editor                  string
	Updated                 time.Time
	Blocks                  []Block
	SEO
}

// employee: «Сайты» are the company's — every employee sees and edits them; guests of collabs do not.
func employee(ctx context.Context) error {
	if tasks.IsGuestCtx(ctx) {
		return ErrForbidden
	}
	return nil
}

const siteSQL = `SELECT s.id, s.name, s.slug, s.description, s.published, coalesce(s.created_by::text, ''), coalesce(u.name, ''),
		s.created_at, s.updated_at, (SELECT count(*) FROM site_pages p WHERE p.site_id = s.id)::int,
		coalesce((SELECT p.id::text FROM site_pages p WHERE p.site_id = s.id AND p.is_home), ''),
		s.header, s.footer, coalesce(s.page404::text, ''), s.favicon, s.metrika, s.analytics, s.accent, s.font, coalesce(s.domain, ''),
		s.everyone
	FROM sites s LEFT JOIN users u ON u.id = s.created_by`

func scanSites(rows pgx.Rows, err error) ([]Site, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Site, error) {
		var s Site
		var header, footer []byte
		err := r.Scan(&s.ID, &s.Name, &s.Slug, &s.Description, &s.Published, &s.CreatedBy, &s.Author, &s.Created, &s.Updated, &s.Pages, &s.HomeID,
			&header, &footer, &s.Page404, &s.Favicon, &s.Metrika, &s.Analytics, &s.Accent, &s.Font, &s.Domain,
			&s.Everyone)
		if err != nil {
			return s, err
		}
		return s, errors.Join(json.Unmarshal(header, &s.Header), json.Unmarshal(footer, &s.Footer))
	})
}

// Sites are the sites the viewer may see, the latest changed first.
func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	return s.visible(ctx, siteSQL+` ORDER BY s.updated_at DESC`)
}

// Site is one site the viewer may see.
func (s *Store) Site(ctx context.Context, id string) (Site, error) {
	list, err := s.visible(ctx, siteSQL+` WHERE s.id::text = $1`, id)
	if err != nil {
		return Site{}, err
	}
	if len(list) == 0 {
		return Site{}, ErrNotFound
	}
	return list[0], nil
}

// visible are the sites of the query the viewer may see, with their role on each.
func (s *Store) visible(ctx context.Context, query string, args ...any) ([]Site, error) {
	if err := employee(ctx); err != nil {
		return nil, err
	}
	list, err := scanSites(s.db.Query(ctx, query, args...))
	if err != nil || len(list) == 0 {
		return list, err
	}
	who := actor(ctx, "")
	var ids []string
	if len(list) == 1 {
		ids = []string{list[0].ID}
	}
	g, err := s.grants(ctx, who, ids...)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, x := range list {
		if x.Role = roleOf(ctx, who, x, g); x.Role > RoleNone {
			out = append(out, x)
		}
	}
	return out, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return "", i18n.E("err.site_name")
	}
	return name, nil
}

// slugOf is the address the user gave, or one made of the name; it must be free (unique reports a taken one).
func slugOf(slug, name, fallback string) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		if slug = Slugify(name); slug == "" {
			slug = fallback
		}
	}
	if !ValidSlug(slug) {
		return "", i18n.E("err.site_slug")
	}
	return slug, nil
}

// taken turns a broken uniqueness of an address into its message.
func taken(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return i18n.E("err.site_slug_taken")
	}
	return err
}

// NewPage is a page of a new site.
type NewPage struct {
	Slug, Title string
	Blocks      []Block
}

// CreateSite makes a site with its home page (named homeTitle) holding the given blocks.
func (s *Store) CreateSite(ctx context.Context, userID, name, slug, homeTitle string, blocks []Block) (string, error) {
	return s.CreateSiteFrom(ctx, userID, name, slug, []NewPage{{Title: homeTitle, Blocks: blocks}})
}

// CreateSiteFrom makes a site with its pages (a template of the gallery): the first one is its home page.
func (s *Store) CreateSiteFrom(ctx context.Context, userID, name, slug string, pages []NewPage) (string, error) {
	if err := employee(ctx); err != nil {
		return "", err
	}
	name, err := cleanName(name)
	if err != nil {
		return "", err
	}
	if slug, err = slugOf(slug, name, "site"); err != nil {
		return "", err
	}
	var id string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO sites (name, slug, created_by) VALUES ($1, $2, $3) RETURNING id`, name, slug, userID).Scan(&id); err != nil {
			return taken(err)
		}
		for i, p := range pages {
			raw, err := json.Marshal(normalize(p.Blocks))
			if err != nil {
				return err
			}
			if i == 0 || !ValidSlug(p.Slug) {
				p.Slug = "index"
				if i > 0 {
					p.Slug = "page-" + strconv.Itoa(i)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO site_pages (site_id, slug, title, blocks, is_home, sort, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $7)`, id, p.Slug, p.Title, raw, i == 0, i, userID); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// freeSlug is base, or base-2, base-3, … — the first address no row of the query (with $1 — the address) has.
func freeSlug(ctx context.Context, tx pgx.Tx, base, query string, args ...any) (string, error) {
	for n := 1; ; n++ {
		slug := base
		if n > 1 {
			suffix := "-" + strconv.Itoa(n)
			slug = strings.TrimRight(base[:min(len(base), 63-len(suffix))], "-") + suffix
		}
		var busy bool
		if err := tx.QueryRow(ctx, query, append([]any{slug}, args...)...).Scan(&busy); err != nil {
			return "", err
		}
		if !busy {
			return slug, nil
		}
	}
}

const pageSlugBusy = `SELECT EXISTS (SELECT 1 FROM site_pages WHERE slug = $1 AND site_id = $2)`

// CopySite makes a copy of a site with all its pages under the name, at a free address made of the site's one;
// the copy is not published.
func (s *Store) CopySite(ctx context.Context, userID, id, name string) (string, error) {
	x, err := s.Site(ctx, id)
	if err != nil {
		return "", err
	}
	if name, err = cleanName(name); err != nil {
		return "", err
	}
	var nid string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		slug, err := freeSlug(ctx, tx, strings.TrimRight(x.Slug[:min(len(x.Slug), 58)], "-")+"-copy",
			`SELECT EXISTS (SELECT 1 FROM sites WHERE slug = $1)`)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO sites (name, slug, description, created_by, header, footer, favicon, metrika, analytics, accent, font)
			SELECT $2, $3, description, $4, header, footer, favicon, metrika, analytics, accent, font FROM sites WHERE id = $1 RETURNING id`,
			x.ID, name, slug, userID).Scan(&nid); err != nil {
			return taken(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO site_pages (site_id, slug, title, blocks, is_home, sort, created_by, updated_by,
				seo_title, seo_description, seo_keywords, noindex)
			SELECT $2, slug, title, blocks, is_home, sort, $3, $3, seo_title, seo_description, seo_keywords, noindex
			FROM site_pages WHERE site_id = $1`, x.ID, nid, userID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sites c SET page404 = (SELECT n.id FROM site_pages o JOIN site_pages n ON n.site_id = c.id AND n.slug = o.slug
				WHERE o.id = (SELECT page404 FROM sites WHERE id = $1)) WHERE c.id = $2`, x.ID, nid); err != nil {
			return err
		}
		return relink(ctx, tx, nid, x.Slug, slug)
	})
	return nid, err
}

// CopyPage copies a page, as «title», to the end of a site (its own or another one) at a free address; the copy is
// neither the home page nor published.
func (s *Store) CopyPage(ctx context.Context, userID, siteID, id, toSite, title string) (string, error) {
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return "", err
	}
	to, err := s.need(ctx, userID, toSite, RoleEdit)
	if err != nil {
		return "", err
	}
	if title, err = cleanName(title); err != nil {
		return "", err
	}
	raw, err := json.Marshal(p.Blocks)
	if err != nil {
		return "", err
	}
	var nid string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		base := p.Slug
		if p.Home && to.ID != p.SiteID {
			base = Slugify(p.Title)
		}
		if !ValidSlug(base) || base == "index" {
			base = "page"
		}
		slug, err := freeSlug(ctx, tx, base, pageSlugBusy, to.ID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO site_pages (site_id, slug, title, blocks, sort, created_by, updated_by)
			SELECT $1, $2, $3, $4, coalesce((SELECT max(sort) + 1 FROM site_pages WHERE site_id = $1), 0), $5, $5 RETURNING id`,
			to.ID, slug, title, raw, userID).Scan(&nid); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id = $1`, to.ID)
		return err
	})
	return nid, err
}

// MovePage moves a page to the end of another site, at a free address there; it keeps being published when that
// site is. The home page stays (make another one the home page first).
func (s *Store) MovePage(ctx context.Context, userID, siteID, id, toSite string) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return err
	}
	to, err := s.need(ctx, userID, toSite, RoleEdit)
	if err != nil {
		return err
	}
	if to.ID == p.SiteID {
		return nil
	}
	if p.Home {
		return i18n.E("err.site_home_move")
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		slug, err := freeSlug(ctx, tx, p.Slug, pageSlugBusy, to.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE site_pages SET site_id = $2, slug = $3, published = published AND $4, updated_by = $5,
			updated_at = now(), sort = coalesce((SELECT max(sort) + 1 FROM site_pages WHERE site_id = $2), 0) WHERE id = $1`,
			p.ID, to.ID, slug, to.Published, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id IN ($1, $2)`, p.SiteID, to.ID)
		return err
	})
}

// UpdateSite renames a site and changes its address and description.
func (s *Store) UpdateSite(ctx context.Context, id, name, slug, description string) error {
	if err := employee(ctx); err != nil {
		return err
	}
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	if slug, err = slugOf(slug, name, "site"); err != nil {
		return err
	}
	x, err := s.need(ctx, "", id, RoleFull)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sites SET name = $2, slug = $3, description = $4, updated_at = now() WHERE id = $1`,
			x.ID, name, slug, strings.TrimSpace(description)); err != nil {
			return taken(err)
		}
		return relink(ctx, tx, x.ID, x.Slug, slug)
	})
}

// relink points the links of a site's pages to itself (/site/<from>, /site/<from>/<page>) at its new address.
func relink(ctx context.Context, tx pgx.Tx, siteID, from, to string) error {
	if from == to {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE site_pages SET blocks = regexp_replace(blocks::text, '/site/' || $2 || '(?=[/"#?\\])', '/site/' || $3, 'g')::jsonb
		WHERE site_id = $1 AND strpos(blocks::text, '/site/' || $2) > 0`, siteID, from, to)
	if err != nil {
		return err
	}
	for _, area := range []string{AreaHeader, AreaFooter} {
		if _, err := tx.Exec(ctx, `UPDATE sites SET `+area+` = regexp_replace(`+area+`::text, '/site/' || $2 || '(?=[/"#?\\])', '/site/' || $3, 'g')::jsonb
			WHERE id = $1 AND strpos(`+area+`::text, '/site/' || $2) > 0`, siteID, from, to); err != nil {
			return err
		}
	}
	domains.forget()
	return nil
}

// DeleteSite moves a site with its pages to the trash (full access: its creator, an administrator, …); it stops
// opening at once, its pictures other sites show keep opening.
func (s *Store) DeleteSite(ctx context.Context, userID, id string) error {
	x, err := s.need(ctx, userID, id, RoleFull)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := handOver(ctx, tx, x.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE sites SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, x.ID, nullID(userID))
		return err
	})
	domains.forget()
	return err
}

// PublishSite publishes a site with all its pages, or takes it off (its pages keep their marks).
func (s *Store) PublishSite(ctx context.Context, id string, on bool) error {
	if _, err := s.need(ctx, "", id, RolePublish); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE sites SET published = $2, updated_at = now() WHERE id::text = $1`, id, on)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		domains.forget()
		if on {
			_, err = tx.Exec(ctx, `UPDATE site_pages SET published = true WHERE site_id::text = $1`, id)
		}
		return err
	})
}

// ---------- pages ----------

const pageSQL = `SELECT p.id, p.site_id, p.slug, p.title, p.is_home, p.published, coalesce(u.name, ''), p.updated_at, p.blocks,
		p.seo_title, p.seo_description, p.seo_keywords, p.noindex
	FROM site_pages p LEFT JOIN users u ON u.id = p.updated_by`

func scanPages(rows pgx.Rows, err error) ([]Page, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Page, error) {
		var p Page
		var raw []byte
		if err := r.Scan(&p.ID, &p.SiteID, &p.Slug, &p.Title, &p.Home, &p.Published, &p.Editor, &p.Updated, &raw,
			&p.SEO.Title, &p.SEO.Description, &p.SEO.Keywords, &p.SEO.NoIndex); err != nil {
			return p, err
		}
		return p, json.Unmarshal(raw, &p.Blocks)
	})
}

// Pages are a site's pages: the home page first, then in their order.
func (s *Store) Pages(ctx context.Context, siteID string) ([]Page, error) {
	if _, err := s.Site(ctx, siteID); err != nil {
		return nil, err
	}
	return scanPages(s.db.Query(ctx, pageSQL+` WHERE p.site_id::text = $1 ORDER BY p.is_home DESC, p.sort, p.created_at`, siteID))
}

// Page is one page of a site.
func (s *Store) Page(ctx context.Context, siteID, id string) (Page, error) {
	if _, err := s.Site(ctx, siteID); err != nil {
		return Page{}, err
	}
	list, err := scanPages(s.db.Query(ctx, pageSQL+` WHERE p.site_id::text = $1 AND p.id::text = $2`, siteID, id))
	if err != nil {
		return Page{}, err
	}
	if len(list) == 0 {
		return Page{}, ErrNotFound
	}
	return list[0], nil
}

// CreatePage adds a page to a site, last; a page of a published site is published at once — it can be
// taken off by itself.
func (s *Store) CreatePage(ctx context.Context, userID, siteID, title, slug string, blocks []Block) (string, error) {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return "", err
	}
	title, err := cleanName(title)
	if err != nil {
		return "", err
	}
	if slug, err = slugOf(slug, title, "page"); err != nil {
		return "", err
	}
	raw, err := json.Marshal(normalize(blocks))
	if err != nil {
		return "", err
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO site_pages (site_id, slug, title, blocks, published, sort, created_by, updated_by)
		SELECT s.id, $2, $3, $4, s.published, coalesce((SELECT max(sort) + 1 FROM site_pages WHERE site_id = s.id), 0), $5, $5
		FROM sites s WHERE s.id = $1 RETURNING id`, siteID, slug, title, raw, userID).Scan(&id)
	if err != nil {
		return "", taken(err)
	}
	_, err = s.db.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id = $1`, siteID)
	return id, err
}

// UpdatePage renames a page and changes its address.
func (s *Store) UpdatePage(ctx context.Context, userID, siteID, id, title, slug string) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	title, err := cleanName(title)
	if err != nil {
		return err
	}
	if slug, err = slugOf(slug, title, "page"); err != nil {
		return err
	}
	tag, err := s.db.Exec(ctx, `UPDATE site_pages SET title = $3, slug = $4, updated_by = $5, updated_at = now()
		WHERE site_id::text = $1 AND id::text = $2`, siteID, id, title, slug, userID)
	if err != nil {
		return taken(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePage moves a page to the trash; the home page stays (make another one the home page first).
func (s *Store) DeletePage(ctx context.Context, userID, siteID, id string) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return err
	}
	if p.Home {
		return i18n.E("err.site_home_delete")
	}
	_, err = s.db.Exec(ctx, `UPDATE site_pages SET deleted_at = now(), deleted_by = $2 WHERE id = $1`, p.ID, nullID(userID))
	return err
}

// SetHome makes a page the home page of its site.
func (s *Store) SetHome(ctx context.Context, siteID, id string) error {
	if _, err := s.need(ctx, "", siteID, RoleEdit); err != nil {
		return err
	}
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_pages SET is_home = false WHERE site_id = $1 AND is_home`, p.SiteID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE site_pages SET is_home = true WHERE id = $1`, p.ID)
		return err
	})
}

// PublishPage publishes one page or takes it off; publishing a page of an unpublished site publishes the site too,
// (its other pages stay as they are).
func (s *Store) PublishPage(ctx context.Context, siteID, id string, on bool) error {
	if _, err := s.need(ctx, "", siteID, RolePublish); err != nil {
		return err
	}
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE site_pages SET published = $2 WHERE id = $1`, p.ID, on); err != nil {
			return err
		}
		if !on {
			return nil
		}
		domains.forget()
		_, err := tx.Exec(ctx, `UPDATE sites SET published = true, updated_at = now() WHERE id = $1 AND NOT published`, p.SiteID)
		return err
	})
}

// ---------- blocks ----------

// The areas of a site an editor changes as if they were pages: its header and its footer.
const (
	AreaHeader = "header"
	AreaFooter = "footer"
)

// IsArea: the header or the footer of a site.
func IsArea(page string) bool { return page == AreaHeader || page == AreaFooter }

// loadBlocks reads the blocks of a page or an area of a site, locking its row; the page's site is checked.
func loadBlocks(ctx context.Context, tx pgx.Tx, siteID, pageID string) ([]Block, error) {
	var raw []byte
	var err error
	if IsArea(pageID) {
		err = tx.QueryRow(ctx, `SELECT `+pageID+` FROM sites WHERE id::text = $1 FOR UPDATE`, siteID).Scan(&raw)
	} else {
		err = tx.QueryRow(ctx, `SELECT blocks FROM site_pages WHERE site_id::text = $1 AND id::text = $2 FOR UPDATE`, siteID, pageID).Scan(&raw)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var blocks []Block
	return blocks, json.Unmarshal(raw, &blocks)
}

// saveBlocks writes them back, marking who changed the page and when the site changed, and keeps the change in the
// history: act was done to a block of the type typ.
func saveBlocks(ctx context.Context, tx pgx.Tx, userID, siteID, pageID, act, typ string, blocks []Block) error {
	raw, err := json.Marshal(normalize(blocks))
	if err != nil {
		return err
	}
	if err := keepInitial(ctx, tx, siteID, pageID); err != nil {
		return err
	}
	if IsArea(pageID) {
		_, err = tx.Exec(ctx, `UPDATE sites SET `+pageID+` = $2, updated_at = now() WHERE id::text = $1`, siteID, raw)
	} else if _, err = tx.Exec(ctx, `UPDATE site_pages SET blocks = $2, updated_by = $3, updated_at = now() WHERE id::text = $1`, pageID, raw, userID); err == nil {
		_, err = tx.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id::text = $1`, siteID)
	}
	if err != nil {
		return err
	}
	return record(ctx, tx, userID, siteID, pageID, act, typ, raw)
}

// editBlocks changes the blocks of a page or an area (pageID "header", "footer") under a lock of its row, so two
// editors do not lose each other's change; change tells the type of the block it changed, for the history.
func (s *Store) editBlocks(ctx context.Context, userID, siteID, pageID, act string, change func([]Block) ([]Block, string, error)) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		blocks, err := loadBlocks(ctx, tx, siteID, pageID)
		if err != nil {
			return err
		}
		blocks, typ, err := change(blocks)
		if err != nil {
			return err
		}
		return saveBlocks(ctx, tx, userID, siteID, pageID, act, typ, blocks)
	})
}

// MoveBlock moves a block between a page and an area of its site: into the header — last, into the footer — first,
// from the header into the page — first, from the footer — last.
func (s *Store) MoveBlock(ctx context.Context, userID, siteID, from, blockID, to string) error {
	if _, err := s.need(ctx, userID, siteID, RoleEdit); err != nil {
		return err
	}
	if from == to || IsArea(from) == IsArea(to) {
		return ErrNotFound
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		src, err := loadBlocks(ctx, tx, siteID, from)
		if err != nil {
			return err
		}
		dst, err := loadBlocks(ctx, tx, siteID, to)
		if err != nil {
			return err
		}
		i := find(src, blockID)
		if i < 0 {
			return ErrNotFound
		}
		b := src[i]
		src = append(src[:i], src[i+1:]...)
		if to == AreaFooter || from == AreaHeader {
			dst = append([]Block{b}, dst...)
		} else {
			dst = append(dst, b)
		}
		if err := saveBlocks(ctx, tx, userID, siteID, from, "moveout", b.Type, src); err != nil {
			return err
		}
		return saveBlocks(ctx, tx, userID, siteID, to, "movein", b.Type, dst)
	})
}

func find(blocks []Block, id string) int {
	for i, b := range blocks {
		if b.ID == id {
			return i
		}
	}
	return -1
}

// AddBlock puts a new block of the type at position at (0 — first; past the end — last) and returns its id.
func (s *Store) AddBlock(ctx context.Context, userID, siteID, pageID, typ string, at int, data map[string]string) (string, error) {
	if _, ok := TypeOf(typ); !ok {
		return "", i18n.E("err.site_block")
	}
	b := Block{ID: newID(), Type: typ, Data: data}
	err := s.editBlocks(ctx, userID, siteID, pageID, "add", func(list []Block) ([]Block, string, error) {
		at = max(0, min(at, len(list)))
		return append(list[:at], append([]Block{b}, list[at:]...)...), typ, nil
	})
	return b.ID, err
}

// SaveBlock replaces the content of a block with the fields of its type that were sent.
func (s *Store) SaveBlock(ctx context.Context, userID, siteID, pageID, blockID string, data map[string]string) error {
	return s.editBlocks(ctx, userID, siteID, pageID, "save", func(list []Block) ([]Block, string, error) {
		i := find(list, blockID)
		if i < 0 {
			return nil, "", ErrNotFound
		}
		list[i].Data = data
		return list, list[i].Type, nil
	})
}

// BlockAction: "up", "down", "hide" (hides or shows it) or "delete".
func (s *Store) BlockAction(ctx context.Context, userID, siteID, pageID, blockID, act string) error {
	if act == "hide" { // the history tells hiding from showing
		if blocks, err := s.blocksOf(ctx, siteID, pageID); err == nil {
			if i := find(blocks, blockID); i >= 0 && blocks[i].Hidden {
				act = "show"
			}
		}
	}
	return s.editBlocks(ctx, userID, siteID, pageID, act, func(list []Block) ([]Block, string, error) {
		i := find(list, blockID)
		if i < 0 {
			return nil, "", ErrNotFound
		}
		typ := list[i].Type
		switch act {
		case "up":
			if i > 0 {
				list[i-1], list[i] = list[i], list[i-1]
			}
		case "down":
			if i < len(list)-1 {
				list[i+1], list[i] = list[i], list[i+1]
			}
		case "hide", "show":
			list[i].Hidden = act == "hide"
		case "delete":
			list = append(list[:i], list[i+1:]...)
		default:
			return nil, "", ErrNotFound
		}
		return list, typ, nil
	})
}

// ---------- the public side ----------

// Public is a published page of a published site for a visitor: pageSlug "" — its home page. Hidden blocks are left
// out.
func (s *Store) Public(ctx context.Context, siteSlug, pageSlug string) (Site, Page, []Page, error) {
	list, err := scanSites(s.db.Query(ctx, siteSQL+` WHERE s.slug = $1 AND s.published`, siteSlug))
	if err != nil || len(list) == 0 {
		return Site{}, Page{}, nil, errors.Join(err, ErrNotFound)
	}
	site := list[0]
	pages, err := scanPages(s.db.Query(ctx, pageSQL+` WHERE p.site_id = $1 AND p.published ORDER BY p.is_home DESC, p.sort, p.created_at`, site.ID))
	if err != nil {
		return site, Page{}, nil, err
	}
	for _, p := range pages {
		if pageSlug == p.Slug || (pageSlug == "" && p.Home) {
			p.Blocks = site.Compose(p.Blocks)
			return site, p, pages, nil
		}
	}
	return site, Page{}, pages, ErrNotFound
}

// Compose are the blocks a visitor sees on a page of the site: the header, the page's own ones, the footer.
func (x Site) Compose(page []Block) []Block {
	return append(append(Visible(x.Header), Visible(page)...), Visible(x.Footer)...)
}

// Visible are the blocks a visitor sees.
func Visible(list []Block) []Block {
	out := make([]Block, 0, len(list))
	for _, b := range list {
		if !b.Hidden {
			out = append(out, b)
		}
	}
	return out
}

// PublicFile reports whether a file is a picture of a site (open to everyone; not of a site in the trash).
func (s *Store) PublicFile(ctx context.Context, fileID string) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files f JOIN sites s ON s.id = f.site_id WHERE f.id::text = $1)`, fileID).Scan(&ok)
	return err == nil && ok
}
