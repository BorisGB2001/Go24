package feed

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Reactions of the feed, in the order of its picker; a like is the first one.
var Reactions = []string{"like", "kiss", "laugh", "wonder", "cry", "angry", "facepalm"}

// ReactionEmoji is how a reaction looks (and its key in the messenger).
var ReactionEmoji = map[string]string{"like": "👍", "kiss": "😍", "laugh": "😂", "wonder": "😮", "cry": "😢", "angry": "😡", "facepalm": "🤦"}

// ReactionOf is the reaction of a messenger key: its emoji (with or without a variation selector or a skin tone),
// a heart is «Восхищаюсь», anything else is a like.
func ReactionOf(key string) string {
	key = strings.Map(func(r rune) rune {
		if r == 0xFE0F || r == 0x200D || r == 0x2642 || r == 0x2640 || (r >= 0x1F3FB && r <= 0x1F3FF) {
			return -1
		}
		return r
	}, key)
	for _, r := range Reactions {
		if ReactionEmoji[r] == key {
			return r
		}
	}
	if key == "❤" || key == "😘" || key == "🥰" {
		return "kiss"
	}
	return "like"
}

func validReaction(r string) bool {
	_, ok := ReactionEmoji[r]
	return ok
}

// Summary is what the reactions of a post or a comment look like to the viewer: their own one, how many in all and
// the most frequent ones first.
type Summary struct {
	Mine  string
	Total int
	Top   []Count
}

type Count struct {
	Reaction string
	N        int
}

func (c Count) Emoji() string { return ReactionEmoji[c.Reaction] }

// MineEmoji is the emoji of the viewer's own reaction ("" when none).
func (s Summary) MineEmoji() string { return ReactionEmoji[s.Mine] }

// Reactor is a person who reacted («Кто оценил»).
type Reactor struct {
	ID, Name, Photo, Reaction string
}

func (r Reactor) Emoji() string { return ReactionEmoji[r.Reaction] }

// summaries counts the reactions of posts (table feed_likes, key post_id) or comments (feed_comment_likes, comment_id).
func (s *Store) summaries(ctx context.Context, viewer, table, key string, ids []string) (map[string]*Summary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+key+`::text, reaction, count(*)::int, bool_or(user_id = $2) FROM `+table+`
		WHERE `+key+` = ANY($1::uuid[]) GROUP BY 1, 2`, ids, viewer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*Summary{}
	for rows.Next() {
		var id string
		var c Count
		var mine bool
		if err := rows.Scan(&id, &c.Reaction, &c.N, &mine); err != nil {
			return nil, err
		}
		sm := out[id]
		if sm == nil {
			sm = &Summary{}
			out[id] = sm
		}
		sm.Total += c.N
		sm.Top = append(sm.Top, c)
		if mine {
			sm.Mine = c.Reaction
		}
	}
	order := map[string]int{}
	for i, r := range Reactions {
		order[r] = i
	}
	for _, sm := range out {
		sort.Slice(sm.Top, func(i, j int) bool {
			if sm.Top[i].N != sm.Top[j].N {
				return sm.Top[i].N > sm.Top[j].N
			}
			return order[sm.Top[i].Reaction] < order[sm.Top[j].Reaction]
		})
	}
	return out, rows.Err()
}

// fillReactions sets the reactions of the posts and of their comments.
func (s *Store) fillReactions(ctx context.Context, viewer string, posts []*Post) error {
	var ids, cids []string
	for _, p := range posts {
		ids = append(ids, p.ID)
		for _, c := range p.Comments {
			cids = append(cids, c.ID)
		}
	}
	ps, err := s.summaries(ctx, viewer, "feed_likes", "post_id", ids)
	if err != nil {
		return err
	}
	cs, err := s.summaries(ctx, viewer, "feed_comment_likes", "comment_id", cids)
	if err != nil {
		return err
	}
	for _, p := range posts {
		if sm := ps[p.ID]; sm != nil {
			p.Reactions = *sm
		}
		for i := range p.Comments {
			if sm := cs[p.Comments[i].ID]; sm != nil {
				p.Comments[i].Reactions = *sm
			}
		}
	}
	return nil
}

// commentPost is the post of a comment.
func (s *Store) commentPost(ctx context.Context, commentID string) (string, error) {
	var postID string
	err := s.db.QueryRow(ctx, `SELECT post_id FROM feed_comments WHERE id = $1`, commentID).Scan(&postID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return postID, err
}

// CommentPostVisible is the post of a comment the user may see.
func (s *Store) CommentPostVisible(ctx context.Context, userID, commentID string) (string, error) {
	postID, err := s.commentPost(ctx, commentID)
	if err == nil {
		err = s.canSee(ctx, userID, postID)
	}
	return postID, err
}

// React sets the viewer's reaction to a post, as a click in the picker: the same reaction again (or "")
// takes it back. It returns the reactions now.
func (s *Store) React(ctx context.Context, userID, postID, reaction string) (Summary, error) {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return Summary{}, err
	}
	if err := s.react(ctx, "feed_likes", "post_id", postID, userID, reaction, true, ""); err != nil {
		return Summary{}, err
	}
	return s.summary(ctx, userID, "feed_likes", "post_id", postID)
}

// ReactComment is React for a comment; it returns the comment's post too.
func (s *Store) ReactComment(ctx context.Context, userID, commentID, reaction string) (string, Summary, error) {
	postID, err := s.commentPost(ctx, commentID)
	if err != nil {
		return "", Summary{}, err
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return "", Summary{}, err
	}
	if err := s.react(ctx, "feed_comment_likes", "comment_id", commentID, userID, reaction, true, ""); err != nil {
		return "", Summary{}, err
	}
	sm, err := s.summary(ctx, userID, "feed_comment_likes", "comment_id", commentID)
	return postID, sm, err
}

// react stores a reaction ("" — none); toggle: the same one again takes it back. A changed reaction forgets the
// messenger event of the old one (it was redacted) unless the new event is given.
func (s *Store) react(ctx context.Context, table, key, id, userID, reaction string, toggle bool, event string) error {
	if reaction != "" && !validReaction(reaction) {
		return ErrNotFound
	}
	if toggle && reaction != "" {
		var cur string
		err := s.db.QueryRow(ctx, `SELECT reaction FROM `+table+` WHERE `+key+` = $1 AND user_id = $2`, id, userID).Scan(&cur)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if cur == reaction {
			reaction = ""
		}
	}
	if reaction == "" {
		_, err := s.db.Exec(ctx, `DELETE FROM `+table+` WHERE `+key+` = $1 AND user_id = $2`, id, userID)
		return err
	}
	_, err := s.db.Exec(ctx, `INSERT INTO `+table+` (`+key+`, user_id, reaction, matrix_event) VALUES ($1, $2, $3, $4)
		ON CONFLICT (`+key+`, user_id) DO UPDATE SET reaction = EXCLUDED.reaction, matrix_event = EXCLUDED.matrix_event,
			created_at = now()`, id, userID, reaction, event)
	return err
}

func (s *Store) summary(ctx context.Context, viewer, table, key, id string) (Summary, error) {
	m, err := s.summaries(ctx, viewer, table, key, []string{id})
	if err != nil || m[id] == nil {
		return Summary{}, err
	}
	return *m[id], nil
}

// Reactors are the people who reacted to a post («Кто оценил»), the latest first.
func (s *Store) Reactors(ctx context.Context, userID, postID string) ([]Reactor, error) {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return nil, err
	}
	return s.reactors(ctx, "feed_likes", "post_id", postID)
}

// CommentReactors: the same for a comment.
func (s *Store) CommentReactors(ctx context.Context, userID, commentID string) ([]Reactor, error) {
	postID, err := s.commentPost(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return nil, err
	}
	return s.reactors(ctx, "feed_comment_likes", "comment_id", commentID)
}

func (s *Store) reactors(ctx context.Context, table, key, id string) ([]Reactor, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id, u.name, u.photo_key, l.reaction FROM `+table+` l JOIN users u ON u.id = l.user_id
		WHERE l.`+key+` = $1 ORDER BY l.created_at DESC`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Reactor, error) {
		var x Reactor
		return x, r.Scan(&x.ID, &x.Name, &x.Photo, &x.Reaction)
	})
}

// ---------- the messenger bridge ----------

// SetReaction stores a reaction to a post from the messenger ("" — taken back), remembering its event.
func (s *Store) SetReaction(ctx context.Context, userID, postID, reaction, event string) error {
	if err := s.canSee(ctx, userID, postID); err != nil {
		return err
	}
	return s.react(ctx, "feed_likes", "post_id", postID, userID, reaction, false, event)
}

// SetCommentReaction: the same for a comment.
func (s *Store) SetCommentReaction(ctx context.Context, userID, commentID, reaction, event string) error {
	postID, err := s.commentPost(ctx, commentID)
	if err != nil {
		return err
	}
	if err := s.canSee(ctx, userID, postID); err != nil {
		return err
	}
	return s.react(ctx, "feed_comment_likes", "comment_id", commentID, userID, reaction, false, event)
}

// MyReaction is a user's reaction to a post or a comment (comment — id is a comment's) and its messenger event.
func (s *Store) MyReaction(ctx context.Context, id, userID string, comment bool) (reaction, event string) {
	table, key := "feed_likes", "post_id"
	if comment {
		table, key = "feed_comment_likes", "comment_id"
	}
	_ = s.db.QueryRow(ctx, `SELECT reaction, matrix_event FROM `+table+` WHERE `+key+` = $1 AND user_id = $2`, id, userID).
		Scan(&reaction, &event)
	return reaction, event
}

// SetReactionEvent remembers the messenger event of a reaction sent there.
func (s *Store) SetReactionEvent(ctx context.Context, id, userID, event string, comment bool) error {
	table, key := "feed_likes", "post_id"
	if comment {
		table, key = "feed_comment_likes", "comment_id"
	}
	_, err := s.db.Exec(ctx, `UPDATE `+table+` SET matrix_event = $3 WHERE `+key+` = $1 AND user_id = $2`, id, userID, event)
	return err
}

// CommentEvent is the messenger event of a comment ("" — it is not there).
func (s *Store) CommentEvent(ctx context.Context, commentID string) string {
	var ev string
	_ = s.db.QueryRow(ctx, `SELECT matrix_event FROM feed_comments WHERE id = $1`, commentID).Scan(&ev)
	return ev
}
