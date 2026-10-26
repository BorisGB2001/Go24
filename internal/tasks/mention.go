package tasks

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// @-mentions (the portal «упомянуть»): "@Full Name" of an active employee in a message. The text keeps the plain
// "@Name" people see; the messenger gets real Matrix mentions built from the same list.

// Mention is a person mentioned in a text.
type Mention struct{ ID, Name string }

// Mentioned returns the active employees mentioned as "@Full Name" in body.
func (s *Store) Mentioned(ctx context.Context, body string) ([]Mention, error) {
	return mentioned(ctx, s.db, body)
}

func mentioned(ctx context.Context, q querier, body string) ([]Mention, error) {
	if !strings.Contains(body, "@") {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text, name FROM users WHERE active AND name <> '' AND strpos($1, '@' || name) > 0`, body)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Mention])
	if err != nil {
		return nil, err
	}
	// "@Анна Ли" must not match inside "@Анна Лиина": the name has to end at a word boundary
	var out []Mention
	for _, m := range list {
		if MentionAt(body, m.Name) >= 0 {
			out = append(out, m)
		}
	}
	return out, nil
}

// MentionAt is the byte offset of the first "@name" in body that ends at a word boundary (-1: none).
func MentionAt(body, name string) int {
	tag := "@" + name
	for from := 0; ; {
		i := strings.Index(body[from:], tag)
		if i < 0 {
			return -1
		}
		i += from
		end := i + len(tag)
		r, _ := utf8.DecodeRuneInString(body[end:])
		if end == len(body) || !(unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return i
		}
		from = i + 1
	}
}

// MarkMentions puts "@" before the names of the given people in a text that names them without it (a mention
// written in Element arrives as the plain name), so go24 shows it as a mention too.
func MarkMentions(body string, names []string) string {
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		if n == "" || MentionAt(body, n) >= 0 {
			continue
		}
		if i := strings.Index(body, n); i >= 0 {
			body = body[:i] + "@" + body[i:]
		}
	}
	return body
}

// uniq merges mention IDs, dropping the author and repeats.
func uniq(author string, lists ...[]string) []string {
	seen := map[string]bool{author: true}
	var out []string
	for _, l := range lists {
		for _, id := range l {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func mentionIDs(list []Mention) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.ID
	}
	return out
}
