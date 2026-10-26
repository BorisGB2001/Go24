package kb

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/tasks"
)

// Comment is one of the discussion under a page; a reply has ParentID, the top ones carry their Replies.
type Comment struct {
	ID, ParentID       string
	Body               string
	Author, AuthorID   string
	Created            time.Time
	Edited             *time.Time
	Replies            []*Comment
	CanEdit, CanDelete bool // for the viewer: their own comment; any of the base they manage
}

// Discussion is what is under a page: its comments, who liked it and how many people opened it.
type Discussion struct {
	Comments []*Comment
	Count    int // the comments with the replies
	Likes    []string
	Liked    bool // the viewer likes the page
	Views    int
}

// maxComment is the longest comment, in characters.
const maxComment = 20000

// Discussion is the discussion under the page p of the base sp, which the user sees.
func (s *Store) Discussion(ctx context.Context, userID string, p Page, sp Space) (Discussion, error) {
	d := Discussion{Comments: []*Comment{}, Likes: []string{}}
	rows, err := s.db.Query(ctx, `SELECT c.id::text, coalesce(c.parent_id::text, ''), c.body, coalesce(u.name, ''), coalesce(c.author_id::text, ''),
			c.created_at, c.edited_at
		FROM kb_comments c LEFT JOIN users u ON u.id = c.author_id WHERE c.page_id = $1 ORDER BY c.created_at, c.id`, p.ID)
	if err != nil {
		return d, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Comment, error) {
		c := &Comment{}
		err := r.Scan(&c.ID, &c.ParentID, &c.Body, &c.Author, &c.AuthorID, &c.Created, &c.Edited)
		c.CanEdit = c.AuthorID == userID
		c.CanDelete = c.CanEdit || sp.CanManage
		return c, err
	})
	if err != nil {
		return d, err
	}
	byID := map[string]*Comment{}
	for _, c := range list {
		byID[c.ID] = c
	}
	for _, c := range list {
		if parent := byID[c.ParentID]; parent != nil {
			parent.Replies = append(parent.Replies, c)
		} else {
			d.Comments = append(d.Comments, c)
		}
	}
	d.Count = len(list)
	rows, err = s.db.Query(ctx, `SELECT u.name, u.id = $2 FROM kb_likes l JOIN users u ON u.id = l.user_id WHERE l.page_id = $1
		ORDER BY l.created_at`, p.ID, userID)
	if err != nil {
		return d, err
	}
	type like struct {
		Name string
		Me   bool
	}
	likes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[like])
	if err != nil {
		return d, err
	}
	for _, l := range likes {
		d.Likes = append(d.Likes, l.Name)
		d.Liked = d.Liked || l.Me
	}
	err = s.db.QueryRow(ctx, `SELECT count(*) FROM kb_views WHERE page_id = $1`, p.ID).Scan(&d.Views)
	return d, err
}

func checkComment(body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", i18n.E("err.comment_empty")
	}
	if utf8.RuneCountInString(body) > maxComment {
		body = string([]rune(body)[:maxComment])
	}
	return body, nil
}

// AddComment writes the user's comment under a page they see — or, with parentID, a reply to a comment there (a
// reply to a reply goes under the same comment: one step. The mentioned «@Имя» hear of it, then the
// author of the comment replied to, then those who watch the page — each once, those who still see the base.
func (s *Store) AddComment(ctx context.Context, userID, pageID, parentID, body string) (Comment, error) {
	p, sp, err := s.Page(ctx, userID, pageID)
	if err != nil {
		return Comment{}, err
	}
	if body, err = checkComment(body); err != nil {
		return Comment{}, err
	}
	var parent *string
	replyTo := ""
	if parentID != "" {
		var top string
		err := s.db.QueryRow(ctx, `SELECT coalesce(parent_id, id)::text, coalesce(author_id::text, '') FROM kb_comments
			WHERE id::text = $1 AND page_id = $2`, parentID, p.ID).Scan(&top, &replyTo)
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrNotFound
		}
		if err != nil {
			return Comment{}, err
		}
		parent = &top
	}
	c := Comment{ParentID: deref(parent), Body: body, AuthorID: userID, CanEdit: true, CanDelete: true}
	if err := s.db.QueryRow(ctx, `INSERT INTO kb_comments (page_id, parent_id, author_id, body) VALUES ($1, $2, $3, $4)
		RETURNING id::text, created_at, (SELECT name FROM users WHERE id = $3)`, p.ID, parent, userID, body).Scan(&c.ID, &c.Created, &c.Author); err != nil {
		return c, err
	}
	url := "/kb/" + sp.ID + "/" + p.ID + "#c-" + c.ID
	told := map[string]bool{userID: true}
	mentioned, err := tasks.NewStore(s.db).Mentioned(ctx, body)
	if err != nil {
		return c, err
	}
	var ids []string
	for _, m := range mentioned {
		ids = append(ids, m.ID)
	}
	if err := s.notify(ctx, userID, sp.ID, ids, told, "kb_mention", p.Title, url); err != nil {
		return c, err
	}
	if err := s.notify(ctx, userID, sp.ID, []string{replyTo}, told, "kb_reply", p.Title, url); err != nil {
		return c, err
	}
	watchers, err := s.watchers(ctx, sp.ID, p.ID)
	if err != nil {
		return c, err
	}
	return c, s.notify(ctx, userID, sp.ID, watchers, told, "kb_comment", p.Title, url)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// comment finds a comment of a page the user sees, with that page's base.
func (s *Store) comment(ctx context.Context, userID, id string) (string, Space, error) {
	var page, author string
	err := s.db.QueryRow(ctx, `SELECT page_id::text, coalesce(author_id::text, '') FROM kb_comments WHERE id::text = $1`, id).Scan(&page, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Space{}, ErrNotFound
	}
	if err != nil {
		return "", Space{}, err
	}
	_, sp, err := s.Page(ctx, userID, page)
	return author, sp, err
}

// EditComment changes the user's own comment.
func (s *Store) EditComment(ctx context.Context, userID, id, body string) error {
	author, _, err := s.comment(ctx, userID, id)
	if err != nil {
		return err
	}
	if author != userID {
		return ErrForbidden
	}
	if body, err = checkComment(body); err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `UPDATE kb_comments SET body = $2, edited_at = now() WHERE id = $1 AND body <> $2`, id, body)
	return err
}

// DeleteComment deletes a comment with the replies to it: its author does, or who manages the base.
func (s *Store) DeleteComment(ctx context.Context, userID, id string) error {
	author, sp, err := s.comment(ctx, userID, id)
	if err != nil {
		return err
	}
	if author != userID && !sp.CanManage {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `DELETE FROM kb_comments WHERE id = $1`, id)
	return err
}

// Like sets «Нравится» of the user at a page they see (on false — takes it back). The page's author hears of it once.
func (s *Store) Like(ctx context.Context, userID, pageID string, on bool) error {
	p, sp, err := s.Page(ctx, userID, pageID)
	if err != nil {
		return err
	}
	if !on {
		_, err = s.db.Exec(ctx, `DELETE FROM kb_likes WHERE page_id = $1 AND user_id = $2`, p.ID, userID)
		return err
	}
	tag, err := s.db.Exec(ctx, `INSERT INTO kb_likes (page_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, p.ID, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	return s.notify(ctx, userID, sp.ID, []string{p.AuthorID}, map[string]bool{userID: true}, "kb_like", p.Title, "/kb/"+sp.ID+"/"+p.ID)
}

// watchers are those who watch the page, a page above it or the base.
func (s *Store) watchers(ctx context.Context, spaceID, pageID string) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT user_id::text FROM kb_marks WHERE kind = 'watch' AND space_id = $2
		AND (page_id IS NULL OR page_id IN (`+ancestorsSQL+`))`, pageID, spaceID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// notify tells the users not told yet (told grows) who still see the base: the notice kind about the page titled
// title, leading to url. One unread notice of a kind at the same url is enough.
func (s *Store) notify(ctx context.Context, actorID, spaceID string, users []string, told map[string]bool, kind, title, url string) error {
	plain := tasks.WithoutAdmin(ctx)
	for _, u := range users {
		if u == "" || told[u] {
			continue
		}
		told[u] = true
		if _, err := s.Space(plain, u, spaceID); err != nil {
			continue
		}
		if _, err := s.db.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url)
			SELECT $1, $2, $3, left($4, 300), $5 WHERE NOT EXISTS (SELECT 1 FROM notifications
				WHERE user_id = $1 AND kind = $3 AND url = $5 AND body = left($4, 300) AND read_at IS NULL)`, u, actorID, kind, title, url); err != nil {
			return err
		}
	}
	return nil
}
