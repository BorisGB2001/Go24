// Package sites is «Сайты»: sites made of pages, a page made of ready blocks (a cover, a text, a picture,
// advantages in columns, contacts, …) that are added from a library, edited, moved, hidden and removed; a published
// site with a published page opens to anyone at /site/{slug}/{page}.
package sites

import (
	"context"
	"encoding/json"
	"errors"
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
}

// Page is one page of a site; Blocks are filled by Page only.
type Page struct {
	ID, SiteID, Slug, Title string
	Home, Published         bool
	Editor                  string
	Updated                 time.Time
	Blocks                  []Block
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
		coalesce((SELECT p.id::text FROM site_pages p WHERE p.site_id = s.id AND p.is_home), '')
	FROM sites s LEFT JOIN users u ON u.id = s.created_by`

func scanSites(rows pgx.Rows, err error) ([]Site, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Site, error) {
		var s Site
		err := r.Scan(&s.ID, &s.Name, &s.Slug, &s.Description, &s.Published, &s.CreatedBy, &s.Author, &s.Created, &s.Updated, &s.Pages, &s.HomeID)
		return s, err
	})
}

// Sites are all the sites, the latest changed first.
func (s *Store) Sites(ctx context.Context) ([]Site, error) {
	if err := employee(ctx); err != nil {
		return nil, err
	}
	return scanSites(s.db.Query(ctx, siteSQL+` ORDER BY s.updated_at DESC`))
}

// Site is one site.
func (s *Store) Site(ctx context.Context, id string) (Site, error) {
	if err := employee(ctx); err != nil {
		return Site{}, err
	}
	list, err := scanSites(s.db.Query(ctx, siteSQL+` WHERE s.id::text = $1`, id))
	if err != nil {
		return Site{}, err
	}
	if len(list) == 0 {
		return Site{}, ErrNotFound
	}
	return list[0], nil
}

// CanDelete: the site's creator or an administrator.
func (x Site) CanDelete(ctx context.Context, userID string) bool {
	return x.CreatedBy == userID || tasks.IsAdmin(ctx)
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

// CreateSite makes a site with its home page (named homeTitle) holding the given blocks.
func (s *Store) CreateSite(ctx context.Context, userID, name, slug, homeTitle string, blocks []Block) (string, error) {
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
	raw, err := json.Marshal(normalize(blocks))
	if err != nil {
		return "", err
	}
	var id string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO sites (name, slug, created_by) VALUES ($1, $2, $3) RETURNING id`, name, slug, userID).Scan(&id); err != nil {
			return taken(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO site_pages (site_id, slug, title, blocks, is_home, created_by, updated_by)
			VALUES ($1, 'index', $2, $3, true, $4, $4)`, id, homeTitle, raw, userID)
		return err
	})
	return id, err
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
	tag, err := s.db.Exec(ctx, `UPDATE sites SET name = $2, slug = $3, description = $4, updated_at = now() WHERE id::text = $1`,
		id, name, slug, strings.TrimSpace(description))
	if err != nil {
		return taken(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSite removes a site with its pages (its creator or an administrator).
func (s *Store) DeleteSite(ctx context.Context, userID, id string) error {
	x, err := s.Site(ctx, id)
	if err != nil {
		return err
	}
	if !x.CanDelete(ctx, userID) {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `DELETE FROM sites WHERE id = $1`, x.ID)
	return err
}

// PublishSite publishes a site with all its pages, or takes it off (its pages keep their marks).
func (s *Store) PublishSite(ctx context.Context, id string, on bool) error {
	if err := employee(ctx); err != nil {
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
		if on {
			_, err = tx.Exec(ctx, `UPDATE site_pages SET published = true WHERE site_id::text = $1`, id)
		}
		return err
	})
}

// ---------- pages ----------

const pageSQL = `SELECT p.id, p.site_id, p.slug, p.title, p.is_home, p.published, coalesce(u.name, ''), p.updated_at, p.blocks
	FROM site_pages p LEFT JOIN users u ON u.id = p.updated_by`

func scanPages(rows pgx.Rows, err error) ([]Page, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Page, error) {
		var p Page
		var raw []byte
		if err := r.Scan(&p.ID, &p.SiteID, &p.Slug, &p.Title, &p.Home, &p.Published, &p.Editor, &p.Updated, &raw); err != nil {
			return p, err
		}
		return p, json.Unmarshal(raw, &p.Blocks)
	})
}

// Pages are a site's pages: the home page first, then in their order.
func (s *Store) Pages(ctx context.Context, siteID string) ([]Page, error) {
	if err := employee(ctx); err != nil {
		return nil, err
	}
	return scanPages(s.db.Query(ctx, pageSQL+` WHERE p.site_id::text = $1 ORDER BY p.is_home DESC, p.sort, p.created_at`, siteID))
}

// Page is one page of a site.
func (s *Store) Page(ctx context.Context, siteID, id string) (Page, error) {
	if err := employee(ctx); err != nil {
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
	if _, err := s.Site(ctx, siteID); err != nil {
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
	if err := employee(ctx); err != nil {
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

// DeletePage removes a page; the home page stays (make another one the home page first).
func (s *Store) DeletePage(ctx context.Context, siteID, id string) error {
	p, err := s.Page(ctx, siteID, id)
	if err != nil {
		return err
	}
	if p.Home {
		return i18n.E("err.site_home_delete")
	}
	_, err = s.db.Exec(ctx, `DELETE FROM site_pages WHERE id = $1`, p.ID)
	return err
}

// SetHome makes a page the home page of its site.
func (s *Store) SetHome(ctx context.Context, siteID, id string) error {
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
		_, err := tx.Exec(ctx, `UPDATE sites SET published = true, updated_at = now() WHERE id = $1 AND NOT published`, p.SiteID)
		return err
	})
}

// ---------- blocks ----------

// editBlocks changes a page's blocks under a lock of its row, so two editors do not lose each other's change.
func (s *Store) editBlocks(ctx context.Context, userID, siteID, pageID string, change func([]Block) ([]Block, error)) error {
	if err := employee(ctx); err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT blocks FROM site_pages WHERE site_id::text = $1 AND id::text = $2 FOR UPDATE`, siteID, pageID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var blocks []Block
		if err := json.Unmarshal(raw, &blocks); err != nil {
			return err
		}
		if blocks, err = change(blocks); err != nil {
			return err
		}
		if raw, err = json.Marshal(normalize(blocks)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE site_pages SET blocks = $2, updated_by = $3, updated_at = now() WHERE id::text = $1`, pageID, raw, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id::text = $1`, siteID)
		return err
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
	err := s.editBlocks(ctx, userID, siteID, pageID, func(list []Block) ([]Block, error) {
		at = max(0, min(at, len(list)))
		return append(list[:at], append([]Block{b}, list[at:]...)...), nil
	})
	return b.ID, err
}

// SaveBlock replaces the content of a block with the fields of its type that were sent.
func (s *Store) SaveBlock(ctx context.Context, userID, siteID, pageID, blockID string, data map[string]string) error {
	return s.editBlocks(ctx, userID, siteID, pageID, func(list []Block) ([]Block, error) {
		i := find(list, blockID)
		if i < 0 {
			return nil, ErrNotFound
		}
		list[i].Data = data
		return list, nil
	})
}

// BlockAction: "up", "down", "hide" (hides or shows it) or "delete".
func (s *Store) BlockAction(ctx context.Context, userID, siteID, pageID, blockID, act string) error {
	return s.editBlocks(ctx, userID, siteID, pageID, func(list []Block) ([]Block, error) {
		i := find(list, blockID)
		if i < 0 {
			return nil, ErrNotFound
		}
		switch act {
		case "up":
			if i > 0 {
				list[i-1], list[i] = list[i], list[i-1]
			}
		case "down":
			if i < len(list)-1 {
				list[i+1], list[i] = list[i], list[i+1]
			}
		case "hide":
			list[i].Hidden = !list[i].Hidden
		case "delete":
			list = append(list[:i], list[i+1:]...)
		default:
			return nil, ErrNotFound
		}
		return list, nil
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
			p.Blocks = Visible(p.Blocks)
			return site, p, pages, nil
		}
	}
	return site, Page{}, pages, ErrNotFound
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

// PublicFile reports whether a file is a picture of a site (open to everyone).
func (s *Store) PublicFile(ctx context.Context, fileID string) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE id::text = $1 AND site_id IS NOT NULL)`, fileID).Scan(&ok)
	return err == nil && ok
}
