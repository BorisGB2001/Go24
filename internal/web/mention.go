package web

import (
	"context"
	"html/template"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

// Employee names for showing "@Name" mentions in texts, refreshed in the background (texts are rendered by
// template functions that have no database access).
var mentionNames atomic.Pointer[mentionList]

type mentionList struct {
	names []string          // HTML-escaped, longest first
	ids   map[string]string // escaped name → the employee ("" when two have the name: no card then)
}

// RefreshMentionNames keeps the list current until ctx ends.
func (s *Server) RefreshMentionNames(ctx context.Context) {
	for t := time.NewTicker(time.Minute); ; {
		rows, err := s.DB.Query(ctx, `SELECT id, name FROM users WHERE active AND name <> ''`)
		if err == nil {
			l := mentionList{ids: map[string]string{}}
			for rows.Next() {
				var id, n string
				if rows.Scan(&id, &n) == nil {
					n = template.HTMLEscapeString(n)
					if _, twice := l.ids[n]; twice {
						l.ids[n] = ""
						continue
					}
					l.names, l.ids[n] = append(l.names, n), id
				}
			}
			rows.Close()
			sort.Slice(l.names, func(i, j int) bool { return len(l.names[i]) > len(l.names[j]) })
			mentionNames.Store(&l)
		} else if ctx.Err() == nil {
			s.Log.Error("mention names", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// markMentions wraps "@Name" of employees in already escaped text; a click on one shows the person's card.
func markMentions(escaped string) string {
	l := mentionNames.Load()
	if l == nil || !strings.Contains(escaped, "@") {
		return escaped
	}
	var b strings.Builder
	for i := 0; i < len(escaped); {
		if escaped[i] == '@' {
			if n := matchName(escaped[i+1:], l.names); n != "" {
				if id := l.ids[n]; id != "" {
					b.WriteString(`<span class="mention" data-user="` + template.HTMLEscapeString(id) + `">@` + n + `</span>`)
				} else {
					b.WriteString(`<span class="mention">@` + n + `</span>`)
				}
				i += 1 + len(n)
				continue
			}
		}
		b.WriteByte(escaped[i])
		i++
	}
	return b.String()
}

func matchName(rest string, names []string) string {
	for _, n := range names {
		if strings.HasPrefix(rest, n) {
			end := rest[len(n):]
			if end == "" || !isWordStart(end) {
				return n
			}
		}
	}
	return ""
}

func isWordStart(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
