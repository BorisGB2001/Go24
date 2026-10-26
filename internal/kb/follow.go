package kb

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Mark kinds: «В избранное» and «Следить».
const (
	MarkFav   = "fav"
	MarkWatch = "watch"
)

// Marks are the viewer's stars and watches at a base and a page of it.
type Marks struct {
	FavSpace, WatchSpace bool
	FavPage, WatchPage   bool
	Inherited            bool // the page is watched through a page above it or the base
}

// Item is a starred or lately opened base or page (PageID "" — the base itself).
type Item struct {
	SpaceID, SpaceName string
	PageID, Title      string
	At                 time.Time
}

// ancestorsSQL: the page $1 and the pages above it.
const ancestorsSQL = `WITH RECURSIVE up AS (SELECT id, parent_id, 0 AS depth FROM kb_pages WHERE id = $1
	UNION ALL SELECT g.id, g.parent_id, up.depth + 1 FROM kb_pages g JOIN up ON g.id = up.parent_id WHERE up.depth < 50) SELECT id FROM up`

// SetMark stars (MarkFav) or watches (MarkWatch) a base the user sees or, with pageID, a page of it — or takes it
// back (on false).
func (s *Store) SetMark(ctx context.Context, userID, kind, spaceID, pageID string, on bool) error {
	if kind != MarkFav && kind != MarkWatch {
		return i18n.E("err.bad_form")
	}
	var sp Space
	var err error
	if pageID != "" {
		var p Page
		p, sp, err = s.Page(ctx, userID, pageID)
		if err == nil && spaceID != "" && p.SpaceID != spaceID {
			err = ErrNotFound
		}
	} else {
		sp, err = s.Space(ctx, userID, spaceID)
	}
	if err != nil {
		return err
	}
	var page *string
	if pageID != "" {
		page = &pageID
	}
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO kb_marks (user_id, kind, space_id, page_id) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
			userID, kind, sp.ID, page)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM kb_marks WHERE user_id = $1 AND kind = $2 AND space_id = $3 AND page_id IS NOT DISTINCT FROM $4::uuid`,
			userID, kind, sp.ID, page)
	}
	return err
}

// MarksAt are the user's marks at the base and the page ("" — the base only).
func (s *Store) MarksAt(ctx context.Context, userID, spaceID, pageID string) (Marks, error) {
	var m Marks
	rows, err := s.db.Query(ctx, `SELECT kind, coalesce(page_id::text, '') FROM kb_marks WHERE user_id = $1 AND space_id::text = $2`, userID, spaceID)
	if err != nil {
		return m, err
	}
	type mark struct{ Kind, Page string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mark])
	if err != nil {
		return m, err
	}
	watched := map[string]bool{}
	for _, k := range list {
		switch {
		case k.Page == "" && k.Kind == MarkFav:
			m.FavSpace = true
		case k.Page == "" && k.Kind == MarkWatch:
			m.WatchSpace = true
		case k.Page == pageID && k.Kind == MarkFav:
			m.FavPage = true
		case k.Page == pageID && k.Kind == MarkWatch:
			m.WatchPage = true
		case k.Kind == MarkWatch:
			watched[k.Page] = true
		}
	}
	m.Inherited = m.WatchSpace
	if pageID != "" && !m.Inherited && len(watched) > 0 {
		rows, err := s.db.Query(ctx, ancestorsSQL, pageID)
		if err != nil {
			return m, err
		}
		up, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return m, err
		}
		for _, id := range up {
			m.Inherited = m.Inherited || watched[id]
		}
	}
	return m, nil
}

// visible keeps the items of the bases the user sees, with the names of the bases.
func (s *Store) visible(ctx context.Context, userID string, items []Item) ([]Item, error) {
	spaces, err := s.Spaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, sp := range spaces {
		names[sp.ID] = sp.Name
	}
	out := []Item{}
	for _, it := range items {
		if name, ok := names[it.SpaceID]; ok {
			it.SpaceName = name
			out = append(out, it)
		}
	}
	return out, nil
}

// Favorites are the bases and pages the user starred, the bases first, then the latest starred.
func (s *Store) Favorites(ctx context.Context, userID string) ([]Item, error) {
	rows, err := s.db.Query(ctx, `SELECT m.space_id::text, '', coalesce(m.page_id::text, ''), coalesce(g.title, ''), m.created_at
		FROM kb_marks m JOIN kb_spaces sp ON sp.id = m.space_id LEFT JOIN kb_pages_all g ON g.id = m.page_id
		WHERE m.user_id = $1 AND m.kind = 'fav' AND (m.page_id IS NULL OR g.deleted_at IS NULL)
		ORDER BY m.page_id IS NOT NULL, m.created_at DESC LIMIT 100`, userID)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Item])
	if err != nil {
		return nil, err
	}
	return s.visible(ctx, userID, items)
}

// Viewed notes that the user opened a page («Недавние»).
func (s *Store) Viewed(ctx context.Context, userID, pageID string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO kb_views (user_id, page_id) VALUES ($1, $2)
		ON CONFLICT (user_id, page_id) DO UPDATE SET viewed_at = now()`, userID, pageID)
	return err
}

// Recent are the pages the user opened lately, the latest first.
func (s *Store) Recent(ctx context.Context, userID string, limit int) ([]Item, error) {
	rows, err := s.db.Query(ctx, `SELECT g.space_id::text, '', g.id::text, g.title, v.viewed_at
		FROM kb_views v JOIN kb_pages g ON g.id = v.page_id JOIN kb_spaces sp ON sp.id = g.space_id
		WHERE v.user_id = $1 ORDER BY v.viewed_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Item])
	if err != nil {
		return nil, err
	}
	return s.visible(ctx, userID, items)
}

// tell notifies those who watch the page (or a page above it, or the base) but the actor: kind kb_page_changed,
// kb_page_new or kb_page_trashed, the body — the page's title. Only who still sees the base is told, and one unread
// notice of a kind about a page is enough. A failure to notify does not undo the change.
func (s *Store) tell(ctx context.Context, actorID, spaceID, pageID, kind, title string) error {
	users, err := s.watchers(ctx, spaceID, pageID)
	if err != nil {
		return err
	}
	url := "/kb/" + spaceID + "/" + pageID
	if kind == "kb_page_trashed" {
		url = "/kb/" + spaceID
	}
	return s.notify(ctx, actorID, spaceID, users, map[string]bool{actorID: true}, kind, title, url)
}

// MarkedPages are the pages of the base the user starred and those they watch themselves.
func (s *Store) MarkedPages(ctx context.Context, userID, spaceID string) (fav, watch []string, err error) {
	rows, err := s.db.Query(ctx, `SELECT m.kind, m.page_id::text FROM kb_marks m JOIN kb_pages g ON g.id = m.page_id
		WHERE m.user_id = $1 AND m.space_id = $2 ORDER BY m.created_at`, userID, spaceID)
	if err != nil {
		return nil, nil, err
	}
	type mark struct{ Kind, Page string }
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[mark])
	if err != nil {
		return nil, nil, err
	}
	fav, watch = []string{}, []string{}
	for _, m := range list {
		if m.Kind == MarkFav {
			fav = append(fav, m.Page)
		} else {
			watch = append(watch, m.Page)
		}
	}
	return fav, watch, nil
}
