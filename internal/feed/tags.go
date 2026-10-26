package feed

import (
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Tags of posts: the «Теги» typed for a post and the #tags of its title and text. A tag opens the
// feed of the posts with it (Filter.Tag).

// MaxTags is how many tags a post may have; a tag is at most maxTagLen characters.
const (
	MaxTags   = 20
	maxTagLen = 50
)

// Hashtag is "#tag" in a text: at the start or after a space or an opening bracket (not "/feed#post-…" of a link
// nor "&#39;" of escaped HTML), letters, digits, "_" and "-".
var Hashtag = regexp.MustCompile(`(^|[\s(\[«"])#([\p{L}\p{N}_][\p{L}\p{N}_-]*)`)

// NormTag is a tag as kept: without "#" and spaces around, lowercase; "" when it is not a tag.
func NormTag(s string) string {
	s = strings.ToLower(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(s), "#")))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxTagLen {
		s = string([]rune(s)[:maxTagLen])
	}
	return strings.TrimRight(s, "-")
}

// Hashtags are the #tags of a text, normalized, each once, in their order; "#123" (a number) is not a tag.
func Hashtags(text string) []string {
	var out []string
	for _, m := range Hashtag.FindAllStringSubmatch(text, -1) {
		if t := NormTag(m[2]); t != "" && strings.Trim(t, "0123456789") != "" {
			out = appendTag(out, t)
		}
	}
	return out
}

// ParseTags splits the «Теги» field: by commas (or by spaces when there are no commas), "#" allowed.
func ParseTags(s string) []string {
	sep := func(r rune) bool { return r == ',' || r == ';' || r == '\n' }
	if !strings.ContainsAny(s, ",;\n") {
		sep = func(r rune) bool { return r == ' ' || r == '\t' }
	}
	var out []string
	for _, f := range strings.FieldsFunc(s, sep) {
		if t := NormTag(f); t != "" {
			out = appendTag(out, t)
		}
	}
	return out
}

func appendTag(list []string, t string) []string {
	for _, x := range list {
		if x == t {
			return list
		}
	}
	return append(list, t)
}

// SetTags keeps the tags of a post: the typed ones (nil — those it has, as in an edit without the «Теги» field) and
// the #tags of its title and text. The caller checked that the user may change the post.
func (s *Store) SetTags(ctx context.Context, postID string, typed []string) error {
	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var title, body string
		if err := tx.QueryRow(ctx, `SELECT title, body FROM feed_posts WHERE id = $1`, postID).Scan(&title, &body); err != nil {
			return err
		}
		if typed == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM feed_tags WHERE post_id = $1 AND NOT typed`, postID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `DELETE FROM feed_tags WHERE post_id = $1`, postID); err != nil {
			return err
		}
		var tags []string
		for _, t := range typed {
			if t = NormTag(t); t != "" {
				tags = appendTag(tags, t)
			}
		}
		n := len(tags)
		for _, t := range Hashtags(title + "\n" + body) {
			tags = appendTag(tags, t)
		}
		if len(tags) > MaxTags {
			tags = tags[:MaxTags]
		}
		for i, t := range tags {
			if _, err := tx.Exec(ctx, `INSERT INTO feed_tags (post_id, tag, typed) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				postID, t, i < n); err != nil {
				return err
			}
		}
		return nil
	})
}

// fillTags sets the tags of the posts, in alphabetical order.
func (s *Store) fillTags(ctx context.Context, posts []*Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]string, len(posts))
	idx := map[string]*Post{}
	for i, p := range posts {
		ids[i] = p.ID
		idx[p.ID] = p
	}
	rows, err := s.db.Query(ctx, `SELECT post_id::text, tag FROM feed_tags WHERE post_id = ANY($1::uuid[]) ORDER BY tag`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var pid, tag string
		if err := rows.Scan(&pid, &tag); err != nil {
			return err
		}
		idx[pid].Tags = append(idx[pid].Tags, tag)
	}
	return rows.Err()
}

// TypedTags are the tags of a post that are not #tags of its text (the «Теги» field of its edit form).
func (p *Post) TypedTags() string {
	in := map[string]bool{}
	for _, t := range Hashtags(p.Title + "\n" + p.Body) {
		in[t] = true
	}
	var out []string
	for _, t := range p.Tags {
		if !in[t] {
			out = append(out, t)
		}
	}
	return strings.Join(out, ", ")
}
