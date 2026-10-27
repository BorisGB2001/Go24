// Package kb is «База знаний»: spaces for all employees or a project, a tree of Markdown pages in each, the
// history of every page and a search.
package kb

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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

// Space is a knowledge base.
type Space struct {
	ID, Name, Description string
	ProjectKey            string // "" — for all employees
	ProjectName           string
	Pages                 int
	CanEdit               bool // the viewer may write pages
	CanManage             bool // the viewer may rename or delete the space and set its rights
	Restricted            bool // the space has «Права доступа» of its own
}

// Page is one page; Children are filled in a tree.
type Page struct {
	ID, SpaceID, ParentID string
	Title, Body           string
	Author, Editor        string
	AuthorID, EditorID    string
	Created, Updated      time.Time
	Children              []*Page
}

// Version is an earlier state of a page.
type Version struct {
	ID       int64
	Title    string
	Editor   string
	EditedAt time.Time
	EditorID string
}

// access: who sees a space and who writes in it. A company space: everyone reads and writes (a wiki); a project
// space: its members read, those who work in the project write. A space with «Права доступа» of its own
// (kb_space_rights) is open only to those listed, with their access. The creator, the project's managers and
// administrators manage it always.
const spaceSQL = `SELECT s.id, s.name, s.description, coalesce(p.key, ''), coalesce(p.name, ''),
		(SELECT count(*) FROM kb_pages g WHERE g.space_id = s.id)::int,
		coalesce($2 OR s.created_by = $1 OR pm.role IN ('owner', 'manager') OR CASE WHEN a.acc IS NULL
			THEN s.project_id IS NULL OR pm.role = 'member' ELSE a.acc IN ('edit', 'full') END, false),
		coalesce($2 OR s.created_by = $1 OR pm.role IN ('owner', 'manager') OR a.acc = 'full', false),
		a.acc IS NOT NULL
	FROM kb_spaces s LEFT JOIN projects p ON p.id = s.project_id
	LEFT JOIN project_members pm ON pm.project_id = s.project_id AND pm.user_id = $1
	CROSS JOIN LATERAL (SELECT kb_space_access(s.id, $1::uuid) AS acc) a
	WHERE ($2 OR s.created_by = $1 OR pm.role IN ('owner', 'manager') OR CASE WHEN a.acc IS NULL
		THEN s.project_id IS NULL OR pm.user_id IS NOT NULL ELSE a.acc <> 'none' END)`

func scanSpaces(rows pgx.Rows, err error) ([]Space, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Space, error) {
		var s Space
		err := r.Scan(&s.ID, &s.Name, &s.Description, &s.ProjectKey, &s.ProjectName, &s.Pages, &s.CanEdit, &s.CanManage, &s.Restricted)
		return s, err
	})
}

// Spaces are the spaces the user sees.
func (s *Store) Spaces(ctx context.Context, userID string) ([]Space, error) {
	return scanSpaces(s.db.Query(ctx, spaceSQL+` ORDER BY s.project_id IS NOT NULL, s.name`, userID, tasks.IsAdmin(ctx)))
}

// Space is one space the user sees.
func (s *Store) Space(ctx context.Context, userID, id string) (Space, error) {
	list, err := scanSpaces(s.db.Query(ctx, spaceSQL+` AND s.id::text = $3`, userID, tasks.IsAdmin(ctx), id))
	if err != nil {
		return Space{}, err
	}
	if len(list) == 0 {
		return Space{}, ErrNotFound
	}
	return list[0], nil
}

// CreateSpace makes a space for everyone (projectID "") or for a project the user works in.
func (s *Store) CreateSpace(ctx context.Context, userID, name, description, projectID string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 200 {
		return "", i18n.E("err.kb_name")
	}
	var project *string
	if projectID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id::text = $1 AND user_id = $2
			AND role IN ('owner', 'manager', 'member'))`, projectID, userID).Scan(&ok); err != nil {
			return "", err
		}
		if !ok && !tasks.IsAdmin(ctx) {
			return "", ErrForbidden
		}
		project = &projectID
	}
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO kb_spaces (name, description, project_id, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
		name, strings.TrimSpace(description), project, userID).Scan(&id)
	return id, err
}

// Tree is the space's pages as a tree (titles only), in their order.
func (s *Store) Tree(ctx context.Context, spaceID string) ([]*Page, error) {
	rows, err := s.db.Query(ctx, `SELECT id, coalesce(parent_id::text, ''), title FROM kb_pages WHERE space_id = $1 ORDER BY sort, title`, spaceID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Page, error) {
		p := &Page{SpaceID: spaceID}
		err := r.Scan(&p.ID, &p.ParentID, &p.Title)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	byID := map[string]*Page{}
	for _, p := range list {
		byID[p.ID] = p
	}
	var roots []*Page
	for _, p := range list {
		if parent := byID[p.ParentID]; parent != nil {
			parent.Children = append(parent.Children, p)
		} else {
			roots = append(roots, p)
		}
	}
	return roots, nil
}

const pageSQL = `SELECT g.id, g.space_id, coalesce(g.parent_id::text, ''), g.title, g.body, a.name, e.name, a.id, e.id, g.created_at, g.updated_at
	FROM kb_pages g JOIN users a ON a.id = g.created_by JOIN users e ON e.id = g.updated_by`

// Page is a page of a space the user sees.
func (s *Store) Page(ctx context.Context, userID, id string) (Page, Space, error) {
	var p Page
	err := s.db.QueryRow(ctx, pageSQL+` WHERE g.id::text = $1`, id).Scan(&p.ID, &p.SpaceID, &p.ParentID, &p.Title, &p.Body,
		&p.Author, &p.Editor, &p.AuthorID, &p.EditorID, &p.Created, &p.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, Space{}, ErrNotFound
	}
	if err != nil {
		return p, Space{}, err
	}
	sp, err := s.Space(ctx, userID, p.SpaceID)
	return p, sp, err
}

func checkPage(title, body string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || len([]rune(title)) > 300 || len(body) > 200000 {
		return "", i18n.E("err.kb_page")
	}
	return title, nil
}

// CreatePage writes a new page (under parentID, "" — at the top).
func (s *Store) CreatePage(ctx context.Context, userID, spaceID, parentID, title, body string) (string, error) {
	sp, err := s.Space(ctx, userID, spaceID)
	if err != nil {
		return "", err
	}
	if !sp.CanEdit {
		return "", ErrForbidden
	}
	if title, err = checkPage(title, body); err != nil {
		return "", err
	}
	var parent *string
	if parentID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM kb_pages WHERE id::text = $1 AND space_id = $2)`, parentID, spaceID).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			return "", ErrNotFound
		}
		parent = &parentID
	}
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO kb_pages (space_id, parent_id, title, body, sort, created_by, updated_by)
		VALUES ($1, $2, $3, $4, (SELECT coalesce(max(sort), 0) + 1 FROM kb_pages WHERE space_id = $1), $5, $5) RETURNING id`,
		spaceID, parent, title, body, userID).Scan(&id)
	if err != nil {
		return "", err
	}
	if err := s.SetMark(ctx, userID, MarkWatch, spaceID, id, true); err != nil { // the author watches the page
		return "", err
	}
	_ = s.tell(ctx, userID, spaceID, id, "kb_page_new", title)
	return id, nil
}

// SavePage changes a page; the earlier text goes to its history.
func (s *Store) SavePage(ctx context.Context, userID, id, title, body string) error {
	return s.SavePageAt(ctx, userID, id, title, body, time.Time{})
}

// SavePageAt changes a page written over its version base (its updated time; zero — whatever it is now): if someone
// saved the page since, nothing is written and the error is ErrChanged. The user's draft of the page goes.
func (s *Store) SavePageAt(ctx context.Context, userID, id, title, body string, base time.Time) error {
	p, sp, err := s.Page(ctx, userID, id)
	if err != nil {
		return err
	}
	if !sp.CanEdit {
		return ErrForbidden
	}
	if title, err = checkPage(title, body); err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var updated time.Time
	if err := tx.QueryRow(ctx, `SELECT updated_at FROM kb_pages_all WHERE id = $1 FOR UPDATE`, p.ID).Scan(&updated); err != nil {
		return err
	}
	if !base.IsZero() && !updated.Equal(base) {
		return ErrChanged
	}
	same := title == p.Title && body == p.Body
	if !same {
		if _, err := tx.Exec(ctx, `INSERT INTO kb_versions (page_id, title, body, edited_by, edited_at)
			SELECT id, title, body, updated_by, updated_at FROM kb_pages WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE kb_pages SET title = $2, body = $3, updated_by = $4, updated_at = now() WHERE id = $1`,
			id, title, body, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM kb_drafts WHERE user_id = $1 AND page_id = $2`, userID, p.ID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if !same {
		_ = s.tell(ctx, userID, sp.ID, id, "kb_page_changed", title)
	}
	return nil
}

// Versions are the page's earlier states, newest first.
func (s *Store) Versions(ctx context.Context, pageID string) ([]Version, error) {
	rows, err := s.db.Query(ctx, `SELECT v.id, v.title, u.name, v.edited_at, u.id FROM kb_versions v JOIN users u ON u.id = v.edited_by
		WHERE v.page_id = $1 ORDER BY v.id DESC LIMIT 50`, pageID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Version])
}

// Version is one earlier text of a page the user sees.
func (s *Store) Version(ctx context.Context, userID, pageID string, id int64) (Page, error) {
	p, _, err := s.Page(ctx, userID, pageID)
	if err != nil {
		return p, err
	}
	err = s.db.QueryRow(ctx, `SELECT v.title, v.body, u.name, u.id, v.edited_at FROM kb_versions v JOIN users u ON u.id = v.edited_by
		WHERE v.page_id = $1 AND v.id = $2`, pageID, id).Scan(&p.Title, &p.Body, &p.Editor, &p.EditorID, &p.Updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// Hit is a page found by the search.
type Hit struct {
	ID, SpaceID, SpaceName, Title, Snippet string
}

// Search finds pages by words in the title or the text, in the spaces the user sees — or, with spaceID, in that one.
func (s *Store) Search(ctx context.Context, userID, q, spaceID string) ([]Hit, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	spaces, err := s.Spaces(ctx, userID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, sp := range spaces {
		if spaceID == "" || sp.ID == spaceID {
			ids = append(ids, sp.ID)
		}
	}
	like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q) + "%"
	rows, err := s.db.Query(ctx, `SELECT g.id, s.id, s.name, g.title,
			CASE WHEN g.title ILIKE $2 THEN left(g.body, 160)
			     ELSE substr(g.body, greatest(strpos(lower(g.body), lower($3)) - 60, 1), 160) END
		FROM kb_pages g JOIN kb_spaces s ON s.id = g.space_id
		WHERE s.id::text = ANY($1) AND (g.title ILIKE $2 OR g.body ILIKE $2)
		ORDER BY g.title ILIKE $2 DESC, g.updated_at DESC LIMIT 50`, ids, like, q)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Hit])
}
