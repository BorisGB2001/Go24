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
var mentionNames atomic.Pointer[[]string] // HTML-escaped, longest first

// RefreshMentionNames keeps the list current until ctx ends.
func (s *Server) RefreshMentionNames(ctx context.Context) {
	for t := time.NewTicker(time.Minute); ; {
		rows, err := s.DB.Query(ctx, `SELECT name FROM users WHERE active AND name <> ''`)
		if err == nil {
			var names []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					names = append(names, template.HTMLEscapeString(n))
				}
			}
			rows.Close()
			sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
			mentionNames.Store(&names)
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

// markMentions wraps "@Name" of employees in already escaped text.
func markMentions(escaped string) string {
	names := mentionNames.Load()
	if names == nil || !strings.Contains(escaped, "@") {
		return escaped
	}
	var b strings.Builder
	for i := 0; i < len(escaped); {
		if escaped[i] == '@' {
			if n := matchName(escaped[i+1:], *names); n != "" {
				b.WriteString(`<span class="mention">@` + n + `</span>`)
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
