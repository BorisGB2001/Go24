package sites

import (
	"context"
	"errors"

	"time"

	"github.com/jackc/pgx/v5"
)

// «Корзина» of «Сайты»: a deleted site (with its pages) or page waits there for the days of the company
// setting of the trash, then goes for good; until then it comes back as it was — at a free address when another site
// or page has taken its own, without its domain when another site has taken it.

// TrashItem is a site (PageID "") or a page in the trash.
type TrashItem struct {
	SiteID, SiteName, PageID, Title string
	DeletedBy                       string
	Deleted                         time.Time
}

// Trash is what the viewer may bring back: the sites they have full access to and the pages of the live sites they
// may edit, the latest deleted first.
func (s *Store) Trash(ctx context.Context) ([]TrashItem, error) {
	if err := employee(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT s.id::text, s.name, '', s.name, coalesce(u.name, ''), s.deleted_at, coalesce(s.created_by::text, ''), s.everyone
			FROM sites_all s LEFT JOIN users u ON u.id = s.deleted_by WHERE s.deleted_at IS NOT NULL
		UNION ALL
		SELECT s.id::text, s.name, p.id::text, p.title, coalesce(u.name, ''), p.deleted_at, coalesce(s.created_by::text, ''), s.everyone
			FROM site_pages_all p JOIN sites s ON s.id = p.site_id LEFT JOIN users u ON u.id = p.deleted_by WHERE p.deleted_at IS NOT NULL
		ORDER BY 6 DESC`)
	if err != nil {
		return nil, err
	}
	type row struct {
		TrashItem
		site Site
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.SiteID, &x.SiteName, &x.PageID, &x.Title, &x.DeletedBy, &x.Deleted, &x.site.CreatedBy, &x.site.Everyone)
		x.site.ID = x.SiteID
		return x, err
	})
	if err != nil {
		return nil, err
	}
	who := actor(ctx, "")
	g, err := s.grants(ctx, who)
	if err != nil {
		return nil, err
	}
	var out []TrashItem
	for _, x := range list {
		need := RoleFull
		if x.PageID != "" {
			need = RoleEdit
		}
		if roleOf(ctx, who, x.site, g) >= need {
			out = append(out, x.TrashItem)
		}
	}
	return out, nil
}

// trashedSite is a site in the trash the user has full access to.
func (s *Store) trashedSite(ctx context.Context, userID, id string) (Site, error) {
	if err := employee(ctx); err != nil {
		return Site{}, err
	}
	var x Site
	err := s.db.QueryRow(ctx, `SELECT id::text, slug, coalesce(created_by::text, ''), everyone, coalesce(domain, '')
		FROM sites_all WHERE id::text = $1 AND deleted_at IS NOT NULL`, id).Scan(&x.ID, &x.Slug, &x.CreatedBy, &x.Everyone, &x.Domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	who := actor(ctx, userID)
	g, err := s.grants(ctx, who, x.ID)
	if err != nil {
		return x, err
	}
	if roleOf(ctx, who, x, g) < RoleFull {
		return x, ErrForbidden
	}
	return x, nil
}

// trashedPage is a page in the trash of a live site the user may edit.
func (s *Store) trashedPage(ctx context.Context, userID, id string) (Page, error) {
	var p Page
	err := s.db.QueryRow(ctx, `SELECT p.id::text, p.site_id::text, p.slug FROM site_pages_all p JOIN sites s ON s.id = p.site_id
		WHERE p.id::text = $1 AND p.deleted_at IS NOT NULL`, id).Scan(&p.ID, &p.SiteID, &p.Slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	_, err = s.need(ctx, userID, p.SiteID, RoleEdit)
	return p, err
}

// RestoreSite brings a site back from the trash; it returns its address, which may differ when another site took it.
func (s *Store) RestoreSite(ctx context.Context, userID, id string) (string, error) {
	x, err := s.trashedSite(ctx, userID, id)
	if err != nil {
		return "", err
	}
	slug := x.Slug
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if slug, err = freeSlug(ctx, tx, x.Slug, `SELECT EXISTS (SELECT 1 FROM sites WHERE slug = $1)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sites_all SET deleted_at = NULL, deleted_by = NULL, slug = $2, updated_at = now(),
			domain = CASE WHEN EXISTS (SELECT 1 FROM sites WHERE domain = $3) THEN NULL ELSE domain END WHERE id = $1`, x.ID, slug, x.Domain); err != nil {
			return err
		}
		return relink(ctx, tx, x.ID, x.Slug, slug)
	})
	domains.forget()
	return slug, err
}

// RestorePage brings a page back from the trash to its site; it returns the site.
func (s *Store) RestorePage(ctx context.Context, userID, id string) (string, error) {
	p, err := s.trashedPage(ctx, userID, id)
	if err != nil {
		return "", err
	}
	return p.SiteID, pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		slug, err := freeSlug(ctx, tx, p.Slug, pageSlugBusy, p.SiteID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE site_pages_all SET deleted_at = NULL, deleted_by = NULL, slug = $2, updated_by = $3, updated_at = now(),
			sort = coalesce((SELECT max(sort) + 1 FROM site_pages WHERE site_id = $4), 0) WHERE id = $1`, p.ID, slug, nullID(userID), p.SiteID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sites SET updated_at = now() WHERE id = $1`, p.SiteID)
		return err
	})
}

func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// PurgeSite deletes a site in the trash for good.
func (s *Store) PurgeSite(ctx context.Context, userID, id string) error {
	x, err := s.trashedSite(ctx, userID, id)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return purgeSite(ctx, tx, x.ID) })
}

// purgeSite deletes a site for good.
func purgeSite(ctx context.Context, tx pgx.Tx, id string) error {
	if err := handOver(ctx, tx, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM sites_all WHERE id = $1`, id)
	return err
}

// handOver passes the pictures of a site that other live sites show (copies, moved pages) to one of them, so they
// keep opening when it goes.
func handOver(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE files f SET site_id = (SELECT p.site_id FROM site_pages p JOIN sites s ON s.id = p.site_id
			WHERE p.site_id <> $1 AND strpos(p.blocks::text, '/sitefile/' || f.id::text) > 0 LIMIT 1)
		WHERE f.site_id = $1 AND EXISTS (SELECT 1 FROM site_pages p JOIN sites s ON s.id = p.site_id
			WHERE p.site_id <> $1 AND strpos(p.blocks::text, '/sitefile/' || f.id::text) > 0)`, id)
	return err
}

// PurgePage deletes a page in the trash for good.
func (s *Store) PurgePage(ctx context.Context, userID, id string) error {
	p, err := s.trashedPage(ctx, userID, id)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `DELETE FROM site_pages_all WHERE id = $1 AND deleted_at IS NOT NULL`, p.ID)
	return err
}

// PurgeExpired deletes for good the sites and pages that went to the trash before the time; it returns how many.
func (s *Store) PurgeExpired(ctx context.Context, before time.Time) (int, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM site_pages_all WHERE deleted_at < $1`, before)
	if err != nil {
		return 0, err
	}
	n := int(tag.RowsAffected())
	rows, err := s.db.Query(ctx, `SELECT id::text FROM sites_all WHERE deleted_at < $1`, before)
	if err != nil {
		return n, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return n, err
	}
	var errs []error
	for _, id := range ids {
		if err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error { return purgeSite(ctx, tx, id) }); err != nil {
			errs = append(errs, err)
		} else {
			n++
		}
	}
	return n, errors.Join(errs...)
}

// TrashPath is where a trash item opens once it is back: the site or the page in the editor.
func (t TrashItem) TrashPath() string {
	if t.PageID == "" {
		return "/sites/" + t.SiteID
	}
	return "/sites/" + t.SiteID + "/p/" + t.PageID
}
