// Package feed is «Лента»: posts to all employees or to a project, comments, reactions,
// important posts confirmed as read. Task events (created, completed) come from the task tracker.
package feed

import (
	"context"
	"errors"
	"sort"
	"strconv"
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

type Store struct {
	db    *pgxpool.Pool
	tasks *tasks.Store
}

func NewStore(db *pgxpool.Pool, t *tasks.Store) *Store { return &Store{db: db, tasks: t} }

type Comment struct {
	ID, AuthorID, AuthorName, Body string
	PostID                         string // set by Comment only
	CreatedAt                      time.Time
	Edited                         bool
	Reactions                      Summary
	Files                          []File // files and pictures of the comment
	CanEdit, CanDelete             bool   // the viewer's rights: the author edits, the author or an administrator deletes
}

type Post struct {
	ID          string
	AuthorID    string
	AuthorName  string
	AuthorPhoto string // photo key
	ProjectKey  string // "" = all employees
	ProjectName string
	Title       string
	Body        string
	Important   bool      // important now: not past ImportantUntil
	Until       time.Time // «Важное до»: zero — important always
	CreatedAt   time.Time
	Edited      bool
	Reactions   Summary
	Read        bool // the viewer confirmed an important post
	Readers     int
	Comments    []Comment
	Files       []File
	Poll        *Poll
	Gratitude   *Gratitude
	To          []Recipient // a post to chosen people and departments only, or those a project post was shared with
	Fav, Pinned bool        // the viewer's marks: in «Избранное», pinned above the feed
	Following   bool        // the viewer is told of new comments («Следить»)
	Hidden      bool        // the viewer hid it from their feed («Скрыть»)
	Views       int         // «Просмотры»: how many people (besides the author) saw it
	Tags        []string    // «Теги»: typed ones and the #tags of the text
	Process     string      // the business process the post is of ("" — none)
	ProcessCard any         // what the viewer sees of the process (filled by the web layer)
}

// Recipient is an addressee of a post: a person or a department (with its sub-departments).
type Recipient struct {
	ID, Name string
	Dept     bool
}

// Audience: the chosen addressees of a new post — people and departments (none — all employees or the project).
type Audience struct {
	Users, Depts []string
}

// File is a file or a picture attached to a post.
type File struct {
	ID          string
	Name        string
	Size        int64
	ContentType string
}

// Image: pictures are shown in the post (SVG is not, it can carry scripts).
func (f File) Image() bool {
	return strings.HasPrefix(f.ContentType, "image/") && f.ContentType != "image/svg+xml"
}

// Item is one entry of the feed: a post or a task event.
type Item struct {
	Post  *Post
	Event *tasks.Event
	At    time.Time
}

type Filter struct {
	Important bool   // only important posts
	Mine      bool   // only my posts
	Project   string // project ID
	Before    time.Time
	// the filter of the feed: words in a post, its title or comments; the author; from a date; what
	// it is — poll | gratitude | bp (business processes) | tasks (the events of tasks only), "" — everything
	Query, Author, Kind string
	From                time.Time
	Favorites           bool   // only the viewer's favourites
	Pinned              bool   // only the posts the viewer pinned (all of them, newest pin first)
	NoPinned            bool   // without them (they are shown above the feed)
	Post                string // one post only: the link of a post or of its comment
	Hidden              bool   // only the posts the viewer hid («Скрытые»); otherwise they are left out
	Posts               bool   // posts only, without the events of tasks (REST)
	Tag                 string // only the posts with this tag
}

// Kinds are the values of Filter.Kind.
var Kinds = []string{"poll", "gratitude", "bp", "tasks"}

// posts: the filter leaves posts out (only task events).
func (f Filter) events() bool {
	return !f.Posts && !f.Important && !f.Hidden && !f.Mine && !f.Favorites && !f.Pinned && f.Project == "" && f.Query == "" && f.Author == "" && f.Post == "" && f.Tag == "" &&
		(f.Kind == "" || f.Kind == "tasks")
}

const PageSize = 30

// important: the post p is important now — not past its «Важное до».
const important = `(p.important AND (p.important_until IS NULL OR p.important_until > now()))`

// hidden: the user $1 hid the post p from their feed.
const hidden = `EXISTS (SELECT 1 FROM feed_hidden x WHERE x.post_id = p.id AND x.user_id = $1)`

// visibleProject: a post to a project is for its members (and administrators in admin mode).
// The company's posts are for employees: a guest from outside sees none of them (only those of their collabs).
// A post to chosen addressees is for them and its author; a project post is also for those it was shared with.
const visible = `((p.project_id IS NULL AND NOT EXISTS (SELECT 1 FROM users g WHERE g.id = $1 AND g.guest)
		AND (NOT p.limited OR p.author_id = $1 OR go24_feed_recipient(p.id, $1)))
	OR $2 OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.project_id AND m.user_id = $1)
	OR (p.project_id IS NOT NULL AND go24_feed_recipient(p.id, $1)))`

// List returns the newest feed entries the user may see, before f.Before.
func (s *Store) List(ctx context.Context, userID string, f Filter) ([]Item, error) {
	return s.list(ctx, userID, f, "")
}

func (s *Store) list(ctx context.Context, userID string, f Filter, only string) ([]Item, error) {
	if f.Before.IsZero() {
		f.Before = time.Now().Add(time.Minute)
	}
	if f.Post != "" {
		only = f.Post
	}
	admin := tasks.IsAdmin(ctx)
	where := []string{visible, "p.created_at < $3"}
	args := []any{userID, admin, f.Before}
	if f.Important {
		where = append(where, important)
	}
	// a hidden post is only in «Скрытые» (and by its link)
	if f.Hidden {
		where = append(where, hidden)
	} else if only == "" {
		where = append(where, "NOT "+hidden)
	}
	if f.Mine {
		where = append(where, "p.author_id = $1")
	}
	if f.Project != "" {
		args = append(args, f.Project)
		where = append(where, "p.project_id = $"+strconv.Itoa(len(args)))
	}
	if only != "" {
		args = append(args, only)
		where = append(where, "p.id::text = $"+strconv.Itoa(len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(q)+"%")
		n := "$" + strconv.Itoa(len(args))
		where = append(where, "(p.title ILIKE "+n+" OR p.body ILIKE "+n+" OR EXISTS (SELECT 1 FROM feed_comments c WHERE c.post_id = p.id AND c.body ILIKE "+n+"))")
	}
	if f.Author != "" {
		args = append(args, f.Author)
		where = append(where, "p.author_id::text = $"+strconv.Itoa(len(args)))
	}
	if t := NormTag(f.Tag); t != "" {
		args = append(args, t)
		where = append(where, "EXISTS (SELECT 1 FROM feed_tags x WHERE x.post_id = p.id AND x.tag = $"+strconv.Itoa(len(args))+")")
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		where = append(where, "p.created_at >= $"+strconv.Itoa(len(args)))
	}
	if f.Favorites {
		where = append(where, "EXISTS (SELECT 1 FROM feed_favorites x WHERE x.post_id = p.id AND x.user_id = $1)")
	}
	if f.Pinned {
		where = append(where, "EXISTS (SELECT 1 FROM feed_pins x WHERE x.post_id = p.id AND x.user_id = $1)")
	}
	if f.NoPinned {
		where = append(where, "NOT EXISTS (SELECT 1 FROM feed_pins x WHERE x.post_id = p.id AND x.user_id = $1)")
	}
	switch f.Kind {
	case "poll":
		where = append(where, "EXISTS (SELECT 1 FROM feed_polls x WHERE x.post_id = p.id)")
	case "gratitude":
		where = append(where, "EXISTS (SELECT 1 FROM feed_gratitudes x WHERE x.post_id = p.id)")
	case "bp":
		where = append(where, "p.bp_instance IS NOT NULL")
	case "tasks":
		where = append(where, "false")
	}
	rows, err := s.db.Query(ctx, `SELECT p.id, p.author_id, u.name, u.photo_key, coalesce(pr.key, ''), coalesce(pr.name, ''),
			p.title, p.body, `+important+`, coalesce(p.important_until, 'epoch'), p.created_at, p.edited_at IS NOT NULL,
			EXISTS (SELECT 1 FROM feed_reads r WHERE r.post_id = p.id AND r.user_id = $1),
			(SELECT count(*) FROM feed_reads r WHERE r.post_id = p.id)::int,
			EXISTS (SELECT 1 FROM feed_favorites x WHERE x.post_id = p.id AND x.user_id = $1),
			EXISTS (SELECT 1 FROM feed_pins x WHERE x.post_id = p.id AND x.user_id = $1),
			`+following+`, `+hidden+`,
			(SELECT count(*) FROM feed_views v WHERE v.post_id = p.id)::int, coalesce(p.bp_instance::text, '')
		FROM feed_posts p JOIN users u ON u.id = p.author_id LEFT JOIN projects pr ON pr.id = p.project_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY p.created_at DESC LIMIT `+strconv.Itoa(PageSize), args...)
	if err != nil {
		return nil, err
	}
	posts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (*Post, error) {
		p := &Post{}
		return p, r.Scan(&p.ID, &p.AuthorID, &p.AuthorName, &p.AuthorPhoto, &p.ProjectKey, &p.ProjectName, &p.Title, &p.Body,
			&p.Important, &p.Until, &p.CreatedAt, &p.Edited, &p.Read, &p.Readers, &p.Fav, &p.Pinned, &p.Following, &p.Hidden, &p.Views, &p.Process)
	})
	if err != nil {
		return nil, err
	}
	if err := s.fillComments(ctx, userID, posts); err != nil {
		return nil, err
	}
	if err := s.fillReactions(ctx, userID, posts); err != nil {
		return nil, err
	}
	if err := s.fillRecipients(ctx, posts); err != nil {
		return nil, err
	}
	if err := s.fillTags(ctx, posts); err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(posts)+PageSize)
	for _, p := range posts {
		if p.Until.Unix() <= 0 {
			p.Until = time.Time{}
		}
		items = append(items, Item{Post: p, At: p.CreatedAt})
	}
	// task events only in the general view (not for «important», «mine», a project's posts or a search)
	if f.events() && only == "" {
		evs, err := s.tasks.Events(ctx, userID, f.Before, PageSize)
		if err != nil {
			return nil, err
		}
		for i := range evs {
			if evs[i].At.Before(f.From) {
				continue
			}
			items = append(items, Item{Event: &evs[i], At: evs[i].At})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	if len(items) > PageSize {
		items = items[:PageSize]
	}
	return items, nil
}

// fillComments sets the comments, files and polls of the posts (polls as the viewer sees them).
func (s *Store) fillComments(ctx context.Context, viewer string, posts []*Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]string, len(posts))
	idx := map[string]*Post{}
	for i, p := range posts {
		ids[i] = p.ID
		idx[p.ID] = p
	}
	if err := s.fillPolls(ctx, viewer, ids, idx); err != nil {
		return err
	}
	if err := s.fillGratitudes(ctx, ids, idx); err != nil {
		return err
	}
	frows, err := s.db.Query(ctx, `SELECT post_id::text, id, name, size, content_type FROM files
		WHERE post_id = ANY($1::uuid[]) AND feed_comment_id IS NULL ORDER BY created_at`, ids)
	if err != nil {
		return err
	}
	for frows.Next() {
		var pid string
		var f File
		if err := frows.Scan(&pid, &f.ID, &f.Name, &f.Size, &f.ContentType); err != nil {
			frows.Close()
			return err
		}
		idx[pid].Files = append(idx[pid].Files, f)
	}
	frows.Close()
	if err := frows.Err(); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, `SELECT c.post_id::text, c.id, c.author_id, u.name, c.body, c.created_at, c.edited_at IS NOT NULL FROM feed_comments c
		JOIN users u ON u.id = c.author_id WHERE c.post_id = ANY($1::uuid[]) ORDER BY c.created_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid string
		var c Comment
		if err := rows.Scan(&pid, &c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt, &c.Edited); err != nil {
			return err
		}
		c.CanEdit = c.AuthorID == viewer
		c.CanDelete = c.CanEdit || tasks.IsAdmin(ctx)
		idx[pid].Comments = append(idx[pid].Comments, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	var cs []*Comment
	for _, p := range posts {
		for i := range p.Comments {
			cs = append(cs, &p.Comments[i])
		}
	}
	return s.fillCommentFiles(ctx, cs)
}

// FileNames are the names of the comment's files.
func (c Comment) FileNames() []string {
	names := make([]string, len(c.Files))
	for i, f := range c.Files {
		names[i] = f.Name
	}
	return names
}

// fillCommentFiles sets the files of the comments.
func (s *Store) fillCommentFiles(ctx context.Context, cs []*Comment) error {
	if len(cs) == 0 {
		return nil
	}
	ids := make([]string, len(cs))
	idx := map[string]*Comment{}
	for i, c := range cs {
		ids[i] = c.ID
		idx[c.ID] = c
	}
	rows, err := s.db.Query(ctx, `SELECT feed_comment_id::text, id, name, size, content_type FROM files
		WHERE feed_comment_id = ANY($1::uuid[]) ORDER BY created_at`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		var f File
		if err := rows.Scan(&cid, &f.ID, &f.Name, &f.Size, &f.ContentType); err != nil {
			return err
		}
		idx[cid].Files = append(idx[cid].Files, f)
	}
	return rows.Err()
}

// CanSee: the post is in the user's feed (its files are downloaded by the same rule).
func (s *Store) CanSee(ctx context.Context, userID, postID string) error {
	return s.canSee(ctx, userID, postID)
}

// canSee checks the post's audience for the user.
func (s *Store) canSee(ctx context.Context, userID, postID string) error {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT `+visible+` FROM feed_posts p WHERE p.id = $3`, userID, tasks.IsAdmin(ctx), postID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// CreatePost publishes a message to all employees (projectID "") or to a project the author belongs to.
// People mentioned as "@Name" (or passed in mentions) who may read the post are notified.
func (s *Store) CreatePost(ctx context.Context, authorID, projectID, title, body string, important bool, mentions ...string) (string, error) {
	return s.CreatePostTo(ctx, authorID, projectID, Audience{}, title, body, important, mentions...)
}

// CreatePostTo is CreatePost with chosen addressees (not with a project): only they and the author see the post,
// and each of them is told (an important post — as important).
func (s *Store) CreatePostTo(ctx context.Context, authorID, projectID string, to Audience, title, body string, important bool, mentions ...string) (string, error) {
	limited := len(to.Users)+len(to.Depts) > 0
	if limited && projectID != "" {
		return "", i18n.E("err.bad_form")
	}
	body, title = strings.TrimSpace(body), strings.TrimSpace(title)
	if body == "" {
		return "", i18n.E("err.post_empty")
	}
	if len([]rune(body)) > 20000 || len([]rune(title)) > 300 {
		return "", i18n.E("err.post_long")
	}
	var project any
	if projectID != "" {
		var member bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2 AND role <> 'viewer')`,
			projectID, authorID).Scan(&member); err != nil {
			return "", err
		}
		if !member && !tasks.IsAdmin(ctx) {
			return "", ErrForbidden
		}
		project = projectID
	}
	var id string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO feed_posts (author_id, project_id, title, body, important, limited) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			authorID, project, title, body, important, limited).Scan(&id); err != nil {
			return err
		}
		if limited {
			// employees only (a guest is never an addressee of the company's feed)
			if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, user_id)
				SELECT $1, u.id FROM users u WHERE u.id::text = ANY($2::text[]) AND u.active AND NOT u.guest`, id, to.Users); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, department_id)
				SELECT $1, d.id FROM departments d WHERE d.id::text = ANY($2::text[])`, id, to.Depts); err != nil {
				return err
			}
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM feed_post_recipients WHERE post_id = $1`, id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return i18n.E("err.post_to")
			}
			kind := "post_to_you"
			if important {
				kind = "important"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
				SELECT u.id, $2, $3, $1, left(coalesce(nullif($4, ''), $5), 300) FROM users u
				WHERE u.active AND NOT u.guest AND u.id <> $2 AND go24_feed_recipient($1, u.id) AND NOT go24_quiet()`,
				id, authorID, kind, title, body); err != nil {
				return err
			}
		}
		return s.notifyMentions(ctx, tx, authorID, id, title+"\n"+body, mentions)
	})
	return id, err
}

// following: the user $1 follows the post p — by choice (feed_follows), else as its author.
const following = `coalesce((SELECT f.follow FROM feed_follows f WHERE f.post_id = p.id AND f.user_id = $1), p.author_id = $1)`

// Mark turns the viewer's mark of a post on or off: fav («В избранное»), pin («Закрепить»), follow
// («Следить / Не следить») or hide («Скрыть» from their feed / «Вернуть в ленту»); returns whether it is on.
func (s *Store) Mark(ctx context.Context, userID, postID, mark string) (bool, error) {
	table := map[string]string{"fav": "feed_favorites", "pin": "feed_pins", "follow": "feed_follows", "hide": "feed_hidden"}[mark]
	if table == "" {
		return false, i18n.E("err.bad_form")
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return false, err
	}
	if mark == "follow" {
		var on bool
		err := s.db.QueryRow(ctx, `INSERT INTO feed_follows (post_id, user_id, follow)
			SELECT p.id, $1, NOT `+following+` FROM feed_posts p WHERE p.id = $2
			ON CONFLICT (post_id, user_id) DO UPDATE SET follow = NOT feed_follows.follow RETURNING follow`, userID, postID).Scan(&on)
		return on, err
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM `+table+` WHERE user_id = $1 AND post_id = $2`, userID, postID)
	if err != nil || tag.RowsAffected() > 0 {
		return false, err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO `+table+` (user_id, post_id) VALUES ($1, $2)`, userID, postID)
	return err == nil, err
}

// reader: the user u may read the post p (as visible, without the admin mode).
const reader = `((p.project_id IS NULL AND NOT u.guest AND (NOT p.limited OR p.author_id = u.id OR go24_feed_recipient(p.id, u.id)))
	OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.project_id AND m.user_id = u.id)
	OR (p.project_id IS NOT NULL AND go24_feed_recipient(p.id, u.id)))`

// Share («Поделиться») adds addressees to a post the user sees: to a post to chosen people or to a project
// (a post to everyone needs none). Those who could not read it before are told; returns how many.
func (s *Store) Share(ctx context.Context, userID, postID string, to Audience) (int, error) {
	if len(to.Users)+len(to.Depts) == 0 {
		return 0, i18n.E("err.post_to")
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return 0, err
	}
	var n int
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var open bool
		if err := tx.QueryRow(ctx, `SELECT project_id IS NULL AND NOT limited FROM feed_posts WHERE id = $1`, postID).Scan(&open); err != nil {
			return err
		}
		if open {
			return i18n.E("err.bad_form")
		}
		var before []string
		if err := tx.QueryRow(ctx, `SELECT coalesce(array_agg(u.id::text), '{}') FROM users u JOIN feed_posts p ON p.id = $1
			WHERE u.active AND `+reader, postID).Scan(&before); err != nil {
			return err
		}
		// employees only, as with CreatePostTo; an addressee already there is skipped
		if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, user_id)
			SELECT $1, u.id FROM users u WHERE u.id::text = ANY($2::text[]) AND u.active AND NOT u.guest
			  AND NOT EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = $1 AND r.user_id = u.id)`, postID, to.Users); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO feed_post_recipients (post_id, department_id)
			SELECT $1, d.id FROM departments d WHERE d.id::text = ANY($2::text[])
			  AND NOT EXISTS (SELECT 1 FROM feed_post_recipients r WHERE r.post_id = $1 AND r.department_id = d.id)`, postID, to.Depts); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
			SELECT u.id, $2, 'post_shared', p.id, left(coalesce(nullif(p.title, ''), p.body), 300) FROM users u JOIN feed_posts p ON p.id = $1
			WHERE u.active AND u.id <> $2 AND NOT (u.id::text = ANY($3::text[])) AND `+reader+` AND NOT go24_quiet()`,
			postID, userID, before)
		n = int(tag.RowsAffected())
		return err
	})
	return n, err
}

// fillRecipients names the addressees of the limited posts and those a post was shared with.
func (s *Store) fillRecipients(ctx context.Context, posts []*Post) error {
	at := map[string]*Post{}
	var ids []string
	for _, p := range posts {
		at[p.ID] = p
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT r.post_id::text, coalesce(r.user_id::text, r.department_id::text), coalesce(u.name, d.name, ''), r.department_id IS NOT NULL
		FROM feed_post_recipients r LEFT JOIN users u ON u.id = r.user_id LEFT JOIN departments d ON d.id = r.department_id
		WHERE r.post_id = ANY($1::uuid[]) ORDER BY r.department_id IS NULL, coalesce(u.name, d.name)`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var post string
		var r Recipient
		if err := rows.Scan(&post, &r.ID, &r.Name, &r.Dept); err != nil {
			return err
		}
		at[post].To = append(at[post].To, r)
	}
	return rows.Err()
}

// notifyMentions tells the people mentioned in a post or comment (who may read the post). Someone the
// triggers or AddComment have already notified in this transaction (the followers of a commented post, everyone
// of an important post) gets one notification: a comment notice becomes a mention. The mentioned follow the post.
func (s *Store) notifyMentions(ctx context.Context, tx pgx.Tx, actor, postID, text string, extra []string) error {
	found, err := s.tasks.Mentioned(ctx, text)
	if err != nil {
		return err
	}
	seen := map[string]bool{actor: true}
	var ids []string
	for _, id := range append(extraIDs(found), extra...) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `UPDATE notifications SET kind = 'mention'
		WHERE post_id = $1 AND kind IN ('post_comment', 'post_followed') AND created_at = now() AND user_id = ANY($2::uuid[])`, postID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO feed_follows (post_id, user_id, follow)
		SELECT p.id, u.id, true FROM users u JOIN feed_posts p ON p.id = $1 WHERE u.id = ANY($2::uuid[]) AND u.active AND `+reader+`
		ON CONFLICT (post_id, user_id) DO NOTHING`, postID, ids); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
		SELECT u.id, $3, 'mention', p.id, left($4, 300) FROM users u JOIN feed_posts p ON p.id = $1
		WHERE u.id = ANY($2::uuid[]) AND u.active AND NOT go24_quiet()
		  AND `+reader+`
		  AND NOT EXISTS (SELECT 1 FROM notifications n WHERE n.user_id = u.id AND n.post_id = p.id AND n.created_at = now())`,
		postID, ids, actor, strings.TrimSpace(text))
	return err
}

func extraIDs(list []tasks.Mention) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.ID
	}
	return out
}

// DeletePost: the author or an administrator in admin mode.
func (s *Store) DeletePost(ctx context.Context, userID, postID string) error {
	var author string
	err := s.db.QueryRow(ctx, `SELECT author_id FROM feed_posts WHERE id = $1`, postID).Scan(&author)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != userID && !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `DELETE FROM feed_posts WHERE id = $1`, postID)
	return err
}

func (s *Store) AddComment(ctx context.Context, userID, postID, body string, mentions ...string) (Comment, error) {
	return s.AddCommentFiles(ctx, userID, postID, body, nil, mentions...)
}

// AddCommentFiles adds a comment that will carry files (named files; the caller attaches them): its text may be
// empty then, the notifications tell the files' names.
func (s *Store) AddCommentFiles(ctx context.Context, userID, postID, body string, files []string, mentions ...string) (Comment, error) {
	var c Comment
	body = strings.TrimSpace(body)
	if body == "" && len(files) == 0 {
		return c, i18n.E("err.comment_empty")
	}
	note := body
	if note == "" {
		note = "📎 " + strings.Join(files, ", ")
	}
	if len([]rune(body)) > 5000 {
		return c, i18n.E("err.post_long")
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return c, err
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `WITH n AS (INSERT INTO feed_comments (post_id, author_id, body) VALUES ($1, $2, $3)
				RETURNING id, author_id, body, created_at)
			SELECT n.id, n.author_id, u.name, n.body, n.created_at FROM n JOIN users u ON u.id = n.author_id`, postID, userID, body).
			Scan(&c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt); err != nil {
			return err
		}
		// a comment makes its author follow the post again, even after «Не следить»
		if _, err := tx.Exec(ctx, `INSERT INTO feed_follows (post_id, user_id, follow) VALUES ($1, $2, true)
			ON CONFLICT (post_id, user_id) DO UPDATE SET follow = true`, postID, userID); err != nil {
			return err
		}
		// the followers who may read the post: its author learns of a comment to their post, the others —
		// of one to a post they follow
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id, body)
			SELECT u.id, $2, CASE WHEN u.id = p.author_id THEN 'post_comment' ELSE 'post_followed' END, p.id, left($3, 300)
			FROM users u JOIN feed_posts p ON p.id = $1 LEFT JOIN feed_follows f ON f.post_id = p.id AND f.user_id = u.id
			WHERE coalesce(f.follow, u.id = p.author_id) AND u.id <> $2 AND u.active AND `+reader+` AND NOT go24_quiet()`,
			postID, userID, note); err != nil {
			return err
		}
		return s.notifyMentions(ctx, tx, userID, postID, body, mentions)
	})
	c.CanEdit, c.CanDelete = true, true
	return c, err
}

// MarkRead confirms an important post («Прочитано»).
func (s *Store) MarkRead(ctx context.Context, userID, postID string) (int, error) {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO feed_reads (post_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, postID, userID); err != nil {
		return 0, err
	}
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM feed_reads WHERE post_id = $1`, postID).Scan(&n)
	return n, err
}

// Unread counts important posts the user has not confirmed (the menu badge).
func (s *Store) Unread(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM feed_posts p WHERE `+important+` AND p.author_id <> $1 AND `+visible+`
		AND NOT EXISTS (SELECT 1 FROM feed_reads r WHERE r.post_id = p.id AND r.user_id = $1)`, userID, false).Scan(&n)
	return n, err
}

// SetUntil makes an important post important only until the moment (zero — always); its author or an administrator.
func (s *Store) SetUntil(ctx context.Context, userID, postID string, until time.Time) error {
	if err := s.CanChange(ctx, userID, postID); err != nil {
		return err
	}
	var at any
	if !until.IsZero() {
		if !until.After(time.Now()) {
			return i18n.E("err.until_past")
		}
		at = until
	}
	_, err := s.db.Exec(ctx, `UPDATE feed_posts SET important_until = $2 WHERE id = $1`, postID, at)
	return err
}

// ---------- «Просмотры» ----------

// See records that the user saw the posts in their feed (their own and the ones they may not see are skipped).
func (s *Store) See(ctx context.Context, userID string, postIDs []string) error {
	if len(postIDs) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `INSERT INTO feed_views (post_id, user_id)
		SELECT p.id, $1 FROM feed_posts p WHERE p.id::text = ANY($3::text[]) AND p.author_id <> $1 AND `+visible+`
		ON CONFLICT DO NOTHING`, userID, false, postIDs)
	return err
}

// Viewer is one who saw a post, and when.
type Viewer struct {
	ID, Name, Photo string
	At              time.Time
}

// Viewers: who saw the post, the latest first (for anyone who sees it).
func (s *Store) Viewers(ctx context.Context, userID, postID string) ([]Viewer, error) {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.photo_key, v.viewed_at FROM feed_views v JOIN users u ON u.id = v.user_id
		WHERE v.post_id = $1 ORDER BY v.viewed_at DESC`, postID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Viewer, error) {
		var v Viewer
		return v, r.Scan(&v.ID, &v.Name, &v.Photo, &v.At)
	})
}

// ---------- editing and the messenger bridge ----------

// EditPost changes a post (its author, or an administrator in admin mode).
func (s *Store) EditPost(ctx context.Context, userID, postID, title, body string) error {
	body, title = strings.TrimSpace(body), strings.TrimSpace(title)
	if body == "" {
		return i18n.E("err.post_empty")
	}
	if len([]rune(body)) > 20000 || len([]rune(title)) > 300 {
		return i18n.E("err.post_long")
	}
	var author string
	err := s.db.QueryRow(ctx, `SELECT author_id FROM feed_posts WHERE id = $1`, postID).Scan(&author)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != userID && !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	_, err = s.db.Exec(ctx, `UPDATE feed_posts SET title = $2, body = $3, edited_at = now(), updated_at = now() WHERE id = $1`, postID, title, body)
	return err
}

// EditComment: the comment's author only; the comment gets «изменено».
func (s *Store) EditComment(ctx context.Context, userID, commentID, body string) error {
	body = strings.TrimSpace(body)
	if body == "" { // a comment of files may lose its text
		var files bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM files WHERE feed_comment_id = $1)`, commentID).Scan(&files); err != nil {
			return err
		}
		if !files {
			return i18n.E("err.comment_empty")
		}
	}
	if len([]rune(body)) > 5000 {
		return i18n.E("err.post_long")
	}
	tag, err := s.db.Exec(ctx, `UPDATE feed_comments SET body = $3, edited_at = now() WHERE id = $1 AND author_id = $2`, commentID, userID, body)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrForbidden
	}
	return err
}

// DeleteComment: the comment's author, or an administrator in admin mode.
func (s *Store) DeleteComment(ctx context.Context, userID, commentID string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM feed_comments WHERE id = $1 AND (author_id = $2 OR $3)`, commentID, userID, tasks.IsAdmin(ctx))
	if err == nil && tag.RowsAffected() == 0 {
		return ErrForbidden
	}
	return err
}

// Comment is one comment as the viewer sees it (after an edit); its post must be visible to the viewer.
func (s *Store) Comment(ctx context.Context, userID, commentID string) (Comment, error) {
	var c Comment
	var postID string
	err := s.db.QueryRow(ctx, `SELECT c.post_id::text, c.id, c.author_id, u.name, c.body, c.created_at, c.edited_at IS NOT NULL
		FROM feed_comments c JOIN users u ON u.id = c.author_id WHERE c.id = $1`, commentID).
		Scan(&postID, &c.ID, &c.AuthorID, &c.AuthorName, &c.Body, &c.CreatedAt, &c.Edited)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return c, err
	}
	c.PostID = postID
	if c.Reactions, err = s.summary(ctx, userID, "feed_comment_likes", "comment_id", commentID); err != nil {
		return c, err
	}
	c.CanEdit = c.AuthorID == userID
	c.CanDelete = c.CanEdit || tasks.IsAdmin(ctx)
	return c, s.fillCommentFiles(ctx, []*Comment{&c})
}

// CommentBridge is what the messenger bridge needs to change a comment: the post's room and the comment's event
// ("" when the comment is not in the messenger).
func (s *Store) CommentBridge(ctx context.Context, commentID string) (authorID, room, event string, err error) {
	err = s.db.QueryRow(ctx, `SELECT c.author_id::text, p.matrix_room, c.matrix_event FROM feed_comments c
		JOIN feed_posts p ON p.id = c.post_id WHERE c.id = $1`, commentID).Scan(&authorID, &room, &event)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// Bridge is what the messenger bridge needs to know about a post.
type Bridge struct {
	ID, AuthorID, ProjectID, Title, Body string
	Important, Limited                   bool // a limited post (to chosen addressees) stays in go24
	Room, Event                          string
}

func (s *Store) BridgeInfo(ctx context.Context, postID string) (Bridge, error) {
	var b Bridge
	err := s.db.QueryRow(ctx, `SELECT id, author_id, coalesce(project_id::text, ''), title, body, important, limited, matrix_room, matrix_event
		FROM feed_posts WHERE id = $1`, postID).Scan(&b.ID, &b.AuthorID, &b.ProjectID, &b.Title, &b.Body, &b.Important, &b.Limited, &b.Room, &b.Event)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

func (s *Store) SetPostEvent(ctx context.Context, postID, room, event string) error {
	_, err := s.db.Exec(ctx, `UPDATE feed_posts SET matrix_room = $2, matrix_event = $3 WHERE id = $1`, postID, room, event)
	return err
}

func (s *Store) SetCommentEvent(ctx context.Context, commentID, event string) error {
	_, err := s.db.Exec(ctx, `UPDATE feed_comments SET matrix_event = $2 WHERE id = $1`, commentID, event)
	return err
}

// ByEvent finds what a messenger event is in the feed: kind "post", "comment", "like" (of a post) or "clike" (of
// a comment; id is the comment's) — "" when unknown.
func (s *Store) ByEvent(ctx context.Context, event string) (kind, id, userID string, err error) {
	err = s.db.QueryRow(ctx, `SELECT 'post', id::text, author_id::text FROM feed_posts WHERE matrix_event = $1
		UNION ALL SELECT 'comment', id::text, author_id::text FROM feed_comments WHERE matrix_event = $1
		UNION ALL SELECT 'like', post_id::text, user_id::text FROM feed_likes WHERE matrix_event = $1
		UNION ALL SELECT 'clike', comment_id::text, user_id::text FROM feed_comment_likes WHERE matrix_event = $1 LIMIT 1`, event).Scan(&kind, &id, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil
	}
	return kind, id, userID, err
}

// Item returns one post as a feed entry, if the user may see it (live updates).
func (s *Store) Item(ctx context.Context, userID, postID string) (Item, error) {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return Item{}, err
	}
	items, err := s.list(ctx, userID, Filter{}, postID)
	if err != nil || len(items) == 0 {
		return Item{}, ErrNotFound
	}
	return items[0], nil
}

// CanChange: may the user edit or delete the post (its author, or an administrator in admin mode).
func (s *Store) CanChange(ctx context.Context, userID, postID string) error {
	var author string
	err := s.db.QueryRow(ctx, `SELECT author_id FROM feed_posts WHERE id = $1`, postID).Scan(&author)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if author != userID && !tasks.IsAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}
