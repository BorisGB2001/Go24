package feed

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/company"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Благодарность»: the author thanks colleagues with a badge; they are notified, and the profile lists what
// they were thanked for.

// Badges are the badges of a gratitude in the order the form offers them.
var Badges = []string{"cup", "star", "heart", "like", "flowers", "smile", "gift", "crown", "rocket"}

// BadgeEmoji is how a badge is drawn.
var BadgeEmoji = map[string]string{"cup": "🏆", "star": "⭐", "heart": "❤️", "like": "👍", "flowers": "💐", "smile": "😊",
	"gift": "🎁", "crown": "👑", "rocket": "🚀"}

// Gratitude of a post.
type Gratitude struct {
	Badge  string
	People []Person
}

type Person struct{ ID, Name string }

// Emoji draws the gratitude's badge.
func (g Gratitude) Emoji() string { return BadgeEmoji[g.Badge] }

// SetGratitude makes the author's new post a gratitude to the people (active employees, not the author).
func (s *Store) SetGratitude(ctx context.Context, authorID, postID, badge string, people []string) error {
	if !slices.Contains(Badges, badge) || len(people) == 0 {
		return i18n.E("err.gratitude")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO feed_gratitudes (post_id, badge) SELECT id, $3 FROM feed_posts WHERE id = $1 AND author_id = $2`,
		postID, authorID, badge)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrForbidden
	}
	tag, err = tx.Exec(ctx, `INSERT INTO feed_gratitude_users (post_id, user_id)
		SELECT $1, u.id FROM users u WHERE u.id::text = ANY($2) AND u.active AND u.id <> $3`, postID, people, authorID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return i18n.E("err.gratitude")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id)
		SELECT user_id, $2, 'gratitude', $1 FROM feed_gratitude_users WHERE post_id = $1 AND NOT go24_quiet()`, postID, authorID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) fillGratitudes(ctx context.Context, ids []string, idx map[string]*Post) error {
	rows, err := s.db.Query(ctx, `SELECT g.post_id::text, g.badge, u.id, u.name FROM feed_gratitudes g
		JOIN feed_gratitude_users gu ON gu.post_id = g.post_id JOIN users u ON u.id = gu.user_id
		WHERE g.post_id = ANY($1::uuid[]) ORDER BY u.name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, badge string
		var p Person
		if err := rows.Scan(&pid, &badge, &p.ID, &p.Name); err != nil {
			return err
		}
		post := idx[pid]
		if post.Gratitude == nil {
			post.Gratitude = &Gratitude{Badge: badge}
		}
		post.Gratitude.People = append(post.Gratitude.People, p)
	}
	return rows.Err()
}

// Thanks is one gratitude a person received.
type Thanks struct {
	PostID, Badge, From, Text string
	At                        time.Time
}

// Emoji draws the badge.
func (t Thanks) Emoji() string { return BadgeEmoji[t.Badge] }

// Gratitudes are the thanks the person received that the viewer may see, newest first.
func (s *Store) Gratitudes(ctx context.Context, viewer, userID string) ([]Thanks, error) {
	rows, err := s.db.Query(ctx, `SELECT p.id, g.badge, a.name, coalesce(nullif(p.title, ''), left(p.body, 120)), p.created_at
		FROM feed_gratitude_users gu JOIN feed_gratitudes g ON g.post_id = gu.post_id JOIN feed_posts p ON p.id = g.post_id
		JOIN users a ON a.id = p.author_id
		WHERE gu.user_id = $3 AND `+visible+` ORDER BY p.created_at DESC LIMIT 50`, viewer, tasks.IsAdmin(ctx), userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Thanks, error) {
		var t Thanks
		err := r.Scan(&t.PostID, &t.Badge, &t.From, &t.Text, &t.At)
		return t, err
	})
}

// BoardFilter narrows «Благодарности» of the company: the thanked people of a department (with its
// sub-departments) or one person, a badge, the days From..To (both included; zero — open).
type BoardFilter struct {
	DepartmentID, UserID, Badge string
	From, To                    time.Time
}

// BoardEntry is a gratitude on the company's page: who thanked whom and for what.
type BoardEntry struct {
	PostID, Badge, Text string
	Author              Person
	People              []Person
	At                  time.Time
}

// Emoji draws the badge.
func (e BoardEntry) Emoji() string { return BadgeEmoji[e.Badge] }

// Leader is one of the most thanked people of the period: how many gratitudes and which badges.
type Leader struct {
	Person
	Position, PhotoKey string
	Count              int
	Badges             []BadgeCount // the most given first
}

// PhotoURL is the leader's photo.
func (l Leader) PhotoURL() string { return company.PhotoURL(l.ID, l.PhotoKey) }

type BadgeCount struct {
	Badge string
	Count int
}

// Emoji draws the badge.
func (b BadgeCount) Emoji() string { return BadgeEmoji[b.Badge] }

// where is the WHERE of the gratitudes the viewer sees by the filter; who is the column of a thanked person.
func (f BoardFilter) where(who string, args *[]any) string {
	arg := func(v any) string {
		*args = append(*args, v)
		return "$" + strconv.Itoa(len(*args))
	}
	where := []string{visible}
	if slices.Contains(Badges, f.Badge) {
		where = append(where, "g.badge = "+arg(f.Badge))
	}
	if !f.From.IsZero() {
		where = append(where, "p.created_at >= "+arg(f.From.Format(time.DateOnly))+"::date")
	}
	if !f.To.IsZero() {
		where = append(where, "p.created_at < "+arg(f.To.Format(time.DateOnly))+"::date + 1")
	}
	if f.UserID != "" {
		where = append(where, who+"::text = "+arg(f.UserID))
	}
	if f.DepartmentID != "" {
		where = append(where, who+` IN (SELECT ud.user_id FROM user_departments ud WHERE ud.department_id IN (
			WITH RECURSIVE sub AS (SELECT id FROM departments WHERE id::text = `+arg(f.DepartmentID)+`
				UNION ALL SELECT d.id FROM departments d JOIN sub ON d.parent_id = sub.id) SELECT id FROM sub))`)
	}
	return strings.Join(where, " AND ")
}

// Board lists the gratitudes of the company the viewer sees by the filter, newest first (at most 500), each with
// all its thanked people.
func (s *Store) Board(ctx context.Context, viewer string, f BoardFilter) ([]BoardEntry, error) {
	args := []any{viewer, tasks.IsAdmin(ctx)}
	rows, err := s.db.Query(ctx, `SELECT p.id::text, g.badge, coalesce(nullif(p.title, ''), left(p.body, 300)), p.created_at, a.id::text, a.name,
			ARRAY(SELECT u.id::text FROM feed_gratitude_users x JOIN users u ON u.id = x.user_id WHERE x.post_id = p.id ORDER BY u.name),
			ARRAY(SELECT u.name FROM feed_gratitude_users x JOIN users u ON u.id = x.user_id WHERE x.post_id = p.id ORDER BY u.name)
		FROM feed_gratitudes g JOIN feed_posts p ON p.id = g.post_id JOIN users a ON a.id = p.author_id
		WHERE EXISTS (SELECT 1 FROM feed_gratitude_users gu WHERE gu.post_id = g.post_id AND `+f.where("gu.user_id", &args)+`)
		ORDER BY p.created_at DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (BoardEntry, error) {
		var e BoardEntry
		var ids, names []string
		err := r.Scan(&e.PostID, &e.Badge, &e.Text, &e.At, &e.Author.ID, &e.Author.Name, &ids, &names)
		for i := range ids {
			e.People = append(e.People, Person{ID: ids[i], Name: names[i]})
		}
		return e, err
	})
}

// Leaders are the most thanked active people by the filter («почётная доска»), at most n.
func (s *Store) Leaders(ctx context.Context, viewer string, f BoardFilter, n int) ([]Leader, error) {
	args := []any{viewer, tasks.IsAdmin(ctx)}
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, u.position, u.photo_key, count(*), array_agg(g.badge)
		FROM feed_gratitude_users gu JOIN feed_gratitudes g ON g.post_id = gu.post_id JOIN feed_posts p ON p.id = g.post_id
		JOIN users u ON u.id = gu.user_id
		WHERE u.active AND `+f.where("gu.user_id", &args)+`
		GROUP BY u.id ORDER BY count(*) DESC, u.name LIMIT `+strconv.Itoa(n), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Leader, error) {
		var l Leader
		var badges []string
		err := r.Scan(&l.ID, &l.Name, &l.Position, &l.PhotoKey, &l.Count, &badges)
		for _, b := range Badges {
			if c := countOf(badges, b); c > 0 {
				l.Badges = append(l.Badges, BadgeCount{Badge: b, Count: c})
			}
		}
		slices.SortStableFunc(l.Badges, func(a, b BadgeCount) int { return b.Count - a.Count })
		return l, err
	})
}

func countOf(list []string, v string) (n int) {
	for _, x := range list {
		if x == v {
			n++
		}
	}
	return n
}
