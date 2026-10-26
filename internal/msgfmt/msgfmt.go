// Package msgfmt formats the text of a chat message, as the portal messenger does: its BB codes ([b], [i], [u],
// [s], [code], [quote], [url]) and the usual marks (*bold*, _italic_, ~struck~, `code`, ```a block```, "> a quote");
// links in the text become clickable. The same HTML goes to Matrix as formatted_body, so Element shows it too.
package msgfmt

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// a block: ```code``` (the language after the fence is dropped), [code]…[/code] or [quote]…[/quote]
	blockRe = regexp.MustCompile("(?s)```(?:[A-Za-z0-9_+-]*\\n)?(.*?)```|(?is)\\[code\\](.*?)\\[/code\\]|(?is)\\[quote\\](.*?)\\[/quote\\]")
	// inline pieces kept as they are: `code`, [url=…]…[/url], [url]…[/url] and bare links
	pieceRe = regexp.MustCompile("`([^`\\n]+)`|(?i)\\[url=([^\\]\\s]+)\\](.*?)\\[/url\\]|(?i)\\[url\\]([^\\[\\s]+)\\[/url\\]|(?i)\\b(?:https?://|www\\.)[^\\s<>\"\\[\\]]+")
	// [b]…[/b] and its kin
	bbRe = map[string]*regexp.Regexp{}
	// one formatted piece in Plain: the marks around the text are dropped
	plainRe = regexp.MustCompile(`(?i)\[/?(?:b|i|u|s|code|quote)\]|\[url(?:=[^\]]*)?\]|\[/url\]|` + "```")
)

var bbTags = map[string]string{"b": "b", "i": "i", "u": "u", "s": "s"}

func init() {
	for bb := range bbTags {
		bbRe[bb] = regexp.MustCompile(`(?is)\[` + bb + `\](.+?)\[/` + bb + `\]`)
	}
}

// marks: the inline marks, the longer ones first
var marks = []struct{ mark, tag string }{{"**", "b"}, {"__", "i"}, {"~~", "s"}, {"*", "b"}, {"_", "i"}, {"~", "s"}}

// HTML is the message's text as safe HTML: escaped, formatted, its line breaks as <br>.
func HTML(text string) string {
	text = strings.ReplaceAll(text, "\x00", "")
	var b strings.Builder
	last := 0
	for _, m := range blockRe.FindAllStringSubmatchIndex(text, -1) {
		b.WriteString(lines(trimNL(text[last:m[0]], last > 0, true)))
		switch {
		case m[2] >= 0:
			b.WriteString("<pre><code>" + html.EscapeString(strings.Trim(text[m[2]:m[3]], "\n")) + "</code></pre>")
		case m[4] >= 0:
			b.WriteString("<pre><code>" + html.EscapeString(strings.Trim(text[m[4]:m[5]], "\n")) + "</code></pre>")
		default:
			b.WriteString("<blockquote>" + lines(strings.Trim(text[m[6]:m[7]], "\n")) + "</blockquote>")
		}
		last = m[1]
	}
	b.WriteString(lines(trimNL(text[last:], last > 0, false)))
	return b.String()
}

// trimNL drops the line break that sits next to a block: the block is a line of its own anyway.
func trimNL(s string, afterBlock, beforeBlock bool) string {
	if afterBlock {
		s = strings.TrimPrefix(s, "\n")
	}
	if beforeBlock {
		s = strings.TrimSuffix(s, "\n")
	}
	return s
}

// lines formats the lines of a text; the lines that start with ">" make a quote.
func lines(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	var quote []string
	flush := func() {
		if quote != nil {
			b.WriteString("<blockquote>" + strings.Join(quote, "<br>") + "</blockquote>")
			quote = nil
		}
	}
	prevText := false
	for _, ln := range strings.Split(s, "\n") {
		if q, ok := strings.CutPrefix(ln, ">"); ok {
			quote = append(quote, inline(strings.TrimPrefix(q, " ")))
			prevText = false
			continue
		}
		if quote != nil {
			flush()
		} else if prevText {
			b.WriteString("<br>")
		}
		b.WriteString(inline(ln))
		prevText = true
	}
	flush()
	return b.String()
}

// inline formats one line: the pieces kept as they are, then the BB codes and the marks.
func inline(s string) string {
	var kept []string
	keep := func(h string) string {
		kept = append(kept, h)
		return "\x00" + strconv.Itoa(len(kept)-1) + "\x00"
	}
	var b strings.Builder
	last := 0
	for _, m := range pieceRe.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		last = m[1]
		switch {
		case m[2] >= 0:
			b.WriteString(keep("<code>" + html.EscapeString(s[m[2]:m[3]]) + "</code>"))
		case m[4] >= 0:
			text := s[m[6]:m[7]]
			if h := href(s[m[4]:m[5]]); h != "" {
				if text == "" {
					text = s[m[4]:m[5]]
				}
				b.WriteString(keep(link(h, html.EscapeString(text))))
			} else {
				b.WriteString(html.EscapeString(s[m[0]:m[1]]))
			}
		case m[8] >= 0:
			if h := href(s[m[8]:m[9]]); h != "" {
				b.WriteString(keep(link(h, html.EscapeString(s[m[8]:m[9]]))))
			} else {
				b.WriteString(html.EscapeString(s[m[0]:m[1]]))
			}
		default:
			u := trimURL(s[m[0]:m[1]])
			last = m[0] + len(u)
			b.WriteString(keep(link(href(u), html.EscapeString(u))))
		}
	}
	b.WriteString(html.EscapeString(s[last:]))
	out := b.String()
	for bb, tag := range bbTags {
		for {
			next := bbRe[bb].ReplaceAllString(out, "<"+tag+">$1</"+tag+">")
			if next == out {
				break
			}
			out = next
		}
	}
	for _, m := range marks {
		out = wrap(out, m.mark, m.tag)
	}
	for i, h := range kept {
		out = strings.Replace(out, "\x00"+strconv.Itoa(i)+"\x00", h, 1)
	}
	return out
}

// trimURL leaves out of a bare link the punctuation after it and a closing bracket it does not open.
func trimURL(u string) string {
	end := len(u)
	for end > 0 {
		c := u[end-1]
		if strings.IndexByte(".,!?:;'\"", c) >= 0 {
			end--
			continue
		}
		if c == ')' && strings.Count(u[:end], "(") < strings.Count(u[:end], ")") {
			end--
			continue
		}
		break
	}
	return u[:end]
}

// href is the address of a link, or "" when it is not a web or mail link (javascript: and the like stay text).
func href(u string) string {
	l := strings.ToLower(u)
	switch {
	case strings.HasPrefix(l, "http://"), strings.HasPrefix(l, "https://"), strings.HasPrefix(l, "mailto:"):
		return u
	case strings.HasPrefix(l, "www."):
		return "https://" + u
	}
	return ""
}

func link(href, text string) string {
	return `<a href="` + html.EscapeString(href) + `" target="_blank" rel="noopener">` + text + `</a>`
}

// wrap turns mark…mark into <tag>…</tag>: the mark opens after a non-letter and closes before one, the text
// between does not start or end with a space; snake_case and 2*3*4 stay as they are.
func wrap(s, mark, tag string) string {
	if !strings.Contains(s, mark) {
		return s
	}
	var b strings.Builder
	i := 0
	for i < len(s) {
		j := strings.Index(s[i:], mark)
		if j < 0 {
			break
		}
		j += i
		if !opens(s, j, mark) {
			b.WriteString(s[i : j+len(mark)])
			i = j + len(mark)
			continue
		}
		k := closing(s, j+len(mark), mark)
		if k < 0 {
			b.WriteString(s[i : j+len(mark)])
			i = j + len(mark)
			continue
		}
		b.WriteString(s[i:j] + "<" + tag + ">" + s[j+len(mark):k] + "</" + tag + ">")
		i = k + len(mark)
	}
	b.WriteString(s[i:])
	return b.String()
}

func opens(s string, at int, mark string) bool {
	before, _ := utf8.DecodeLastRuneInString(s[:at])
	after, _ := utf8.DecodeRuneInString(s[at+len(mark):])
	return (at == 0 || !word(before) && !strings.ContainsRune(mark, before)) &&
		at+len(mark) < len(s) && !unicode.IsSpace(after) && !strings.ContainsRune(mark, after)
}

// closing finds the closing mark from the position on, or -1.
func closing(s string, from int, mark string) int {
	for k := from; k < len(s); {
		j := strings.Index(s[k:], mark)
		if j < 0 {
			return -1
		}
		j += k
		before, _ := utf8.DecodeLastRuneInString(s[:j])
		after, _ := utf8.DecodeRuneInString(s[j+len(mark):])
		if j > from && !unicode.IsSpace(before) && !strings.ContainsRune(mark, before) &&
			(j+len(mark) == len(s) || !word(after) && !strings.ContainsRune(mark, after)) {
			return j
		}
		k = j + len(mark)
	}
	return -1
}

func word(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Plain is the text without its marks — for a chat's last message, a quote, a search hit.
func Plain(text string) string {
	text = plainRe.ReplaceAllString(text, "")
	for _, m := range marks {
		text = unwrap(text, m.mark)
	}
	return text
}

// unwrap drops the marks that HTML would turn into formatting.
func unwrap(s, mark string) string {
	const open, shut = "\x01", "\x02"
	w := wrap(s, mark, open)
	return strings.NewReplacer("<"+open+">", "", "</"+open+">", "").Replace(w)
}
