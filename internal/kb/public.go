package kb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Public is an outside link of «Публикация»: the whole base, or a page with its subpages (RootID).
type Public struct {
	Token           string
	SpaceID, RootID string
	Name, RootTitle string // the name of the base and the title of the page of the link
	Description     string
}

// PublicLink is a link of the base for its settings.
type PublicLink struct {
	Token, PageID, Title string
}

// mayPublish: a page is published by those who write in the base, the whole base by those who manage it.
func (s *Store) mayPublish(ctx context.Context, userID, spaceID, pageID string) error {
	sp, err := s.Space(ctx, userID, spaceID)
	if err != nil {
		return err
	}
	if pageID == "" && !sp.CanManage || !sp.CanEdit {
		return ErrForbidden
	}
	if pageID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM kb_pages WHERE id::text = $1 AND space_id = $2)`, pageID, spaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

// Publish opens the base (pageID "") or the page by an outside link; an existing link stays as it was.
func (s *Store) Publish(ctx context.Context, userID, spaceID, pageID string) (string, error) {
	if err := s.mayPublish(ctx, userID, spaceID, pageID); err != nil {
		return "", err
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	var token string
	err := s.db.QueryRow(ctx, `INSERT INTO kb_public_links (token, space_id, page_id, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT (space_id, coalesce(page_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET token = kb_public_links.token
		RETURNING token`, hex.EncodeToString(b), spaceID, nullable(pageID), userID).Scan(&token)
	return token, err
}

// Unpublish switches the link off: the old address stops opening.
func (s *Store) Unpublish(ctx context.Context, userID, spaceID, pageID string) error {
	if err := s.mayPublish(ctx, userID, spaceID, pageID); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM kb_public_links WHERE space_id = $1 AND page_id IS NOT DISTINCT FROM $2`, spaceID, nullable(pageID))
	return err
}

// PageLink is the token of the page's link, "" — not published.
func (s *Store) PageLink(ctx context.Context, spaceID, pageID string) (string, error) {
	var token string
	err := s.db.QueryRow(ctx, `SELECT token FROM kb_public_links WHERE space_id = $1 AND page_id IS NOT DISTINCT FROM $2`,
		spaceID, nullable(pageID)).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	return token, err
}

// PublicLinks are the links of the base: the whole base first, then its pages by title.
func (s *Store) PublicLinks(ctx context.Context, spaceID string) ([]PublicLink, error) {
	rows, err := s.db.Query(ctx, `SELECT l.token, coalesce(g.id::text, ''), coalesce(g.title, '') FROM kb_public_links l
		LEFT JOIN kb_pages_all g ON g.id = l.page_id
		WHERE l.space_id = $1 AND g.deleted_at IS NULL ORDER BY l.page_id IS NOT NULL, g.title`, spaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PublicLink, error) {
		var l PublicLink
		err := r.Scan(&l.Token, &l.PageID, &l.Title)
		return l, err
	})
}

// OpenPublic is the link by its token: not switched off, its base and page not in the trash.
func (s *Store) OpenPublic(ctx context.Context, token string) (Public, error) {
	var p Public
	err := s.db.QueryRow(ctx, `SELECT l.token, l.space_id, coalesce(l.page_id::text, ''), s.name, s.description, coalesce(g.title, '')
		FROM kb_public_links l JOIN kb_spaces s ON s.id = l.space_id LEFT JOIN kb_pages_all g ON g.id = l.page_id
		WHERE l.token = $1 AND g.deleted_at IS NULL`, token).Scan(&p.Token, &p.SpaceID, &p.RootID, &p.Name, &p.Description, &p.RootTitle)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PublicTree is what the link opens: the tree of the base or the page with its subpages.
func (s *Store) PublicTree(ctx context.Context, p Public) ([]*Page, error) {
	tree, err := s.Tree(ctx, p.SpaceID)
	if err != nil || p.RootID == "" {
		return tree, err
	}
	path := Path(tree, p.RootID)
	if path == nil {
		return nil, ErrNotFound
	}
	return path[len(path)-1:], nil
}

// PublicPage is a page under the link: id "" — the first one.
func (s *Store) PublicPage(ctx context.Context, p Public, tree []*Page, id string) (Page, error) {
	if id == "" {
		if len(tree) == 0 {
			return Page{}, ErrNotFound
		}
		id = tree[0].ID
	}
	if Path(tree, id) == nil {
		return Page{}, ErrNotFound
	}
	var g Page
	err := s.db.QueryRow(ctx, pageSQL+` WHERE g.id::text = $1`, id).Scan(&g.ID, &g.SpaceID, &g.ParentID, &g.Title, &g.Body,
		&g.Author, &g.Editor, &g.AuthorID, &g.EditorID, &g.Created, &g.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return g, err
}

// PublicFile: the file is of the base and is shown on a page under the link.
func (s *Store) PublicFile(ctx context.Context, tree []*Page, spaceID, fileID string) error {
	var ids []string
	var walk func([]*Page)
	walk = func(list []*Page) {
		for _, p := range list {
			ids = append(ids, p.ID)
			walk(p.Children)
		}
	}
	walk(tree)
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files f WHERE f.id::text = $2 AND f.kb_space_id = $1)
		AND EXISTS (SELECT 1 FROM kb_pages g WHERE g.space_id = $1 AND g.id::text = ANY($3) AND strpos(g.body, '/files/' || $2) > 0)`,
		spaceID, fileID, ids).Scan(&ok)
	if err == nil && !ok {
		err = ErrNotFound
	}
	return err
}

// PublicBody is the text of a page for the link: its files and the links to the pages of the base lead under the link.
func PublicBody(body, spaceID, token string) string {
	return strings.NewReplacer("](/files/", "](/kbp/"+token+"/f/", "](/kb/"+spaceID+"/", "](/kbp/"+token+"/").Replace(body)
}

func nullable(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
