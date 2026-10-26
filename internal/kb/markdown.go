package kb

import (
	"html"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// A small, safe Markdown for knowledge base pages: headings (#, ##, ###), paragraphs, lists (- and 1.), checklists
// (- [ ] and - [x]), quotes (>), code blocks (```), tables (| a | b | with a |---| line under the head), a line (---),
// inline **bold**, *italic*, ~~struck~~, `code`, [links](https://…) and ![pictures](/files/…). The text is escaped
// first, so no HTML of the author ever reaches the page; links may only be http(s), mailto or go24's own paths.

var (
	reBold   = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic = regexp.MustCompile(`(^|[^*\w])\*([^*\s][^*]*)\*`)
	reStrike = regexp.MustCompile(`~~([^~]+)~~`)
	reCode   = regexp.MustCompile("`([^`]+)`")
	reLink   = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reImage  = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)\)`)
	reNumber = regexp.MustCompile(`^\d+[.)]\s+`)
	reCheck  = regexp.MustCompile(`^\[([ xXхХ])\]\s+`)
	reRule   = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	reDelim  = regexp.MustCompile(`^\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?$`)
)

// cells splits a table row «| a | b |» into its cells.
func cells(line string) []string {
	line = strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func safeURL(u string) bool {
	u = html.UnescapeString(u)
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "mailto:") ||
		(strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//"))
}

// inline renders the marks of one escaped line; code spans are kept apart from the other marks.
func inline(s string) string {
	var codes []string
	s = reCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+m[1:len(m)-1]+"</code>")
		return "\x00" + string(rune('0'+len(codes)-1)) + "\x00"
	})
	s = reImage.ReplaceAllStringFunc(s, func(m string) string {
		p := reImage.FindStringSubmatch(m)
		if !safeURL(p[2]) || strings.HasPrefix(p[2], "mailto:") {
			return p[1]
		}
		codes = append(codes, `<img src="`+p[2]+`" alt="`+p[1]+`" loading="lazy">`)
		return "\x00" + string(rune('0'+len(codes)-1)) + "\x00"
	})
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		p := reLink.FindStringSubmatch(m)
		if !safeURL(p[2]) {
			return p[1]
		}
		ext := ""
		if strings.HasPrefix(p[2], "http") {
			ext = ` target="_blank" rel="noopener"`
		}
		return `<a href="` + p[2] + `"` + ext + `>` + p[1] + `</a>`
	})
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reStrike.ReplaceAllString(s, "<s>$1</s>")
	s = reItalic.ReplaceAllString(s, "$1<i>$2</i>")
	for i, c := range codes {
		s = strings.Replace(s, "\x00"+string(rune('0'+i))+"\x00", c, 1)
	}
	return s
}

// Heading is a heading of a page, for its «Содержание»: ID is the anchor Render gives it.
type Heading struct {
	Level    int // 1 — #, 2 — ##, 3 — ###
	Text, ID string
}

var reTag = regexp.MustCompile(`<[^>]*>`)

// anchor makes the id of a heading from its text: the letters and digits in lower case joined by «-»; a repeated
// one gets «-2», «-3»…
func anchor(text string, seen map[string]int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	id := b.String()
	if id == "" {
		id = "h"
	}
	seen[id]++
	if n := seen[id]; n > 1 {
		id += "-" + strconv.Itoa(n)
	}
	return id
}

// Render turns the page's Markdown into HTML.
func Render(md string) template.HTML {
	h, _ := render(md)
	return h
}

// Headings are the page's headings in their order (none from code blocks).
func Headings(md string) []Heading {
	_, list := render(md)
	return list
}

func render(md string) (template.HTML, []Heading) {
	var out strings.Builder
	var heads []Heading
	seen := map[string]int{}
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	para := []string{}
	list := "" // "ul" | "ol" while a list is open
	flush := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + strings.Join(para, "<br>") + "</p>\n")
			para = para[:0]
		}
		if list != "" {
			out.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") { // a code block, as it is
			flush()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, html.EscapeString(lines[i]))
			}
			out.WriteString("<pre><code>" + strings.Join(code, "\n") + "</code></pre>\n")
			continue
		}
		esc := html.EscapeString(line)
		switch {
		case line == "":
			flush()
		case reRule.MatchString(line):
			flush()
			out.WriteString("<hr>\n")
		case strings.HasPrefix(line, "|") && i+1 < len(lines) && reDelim.MatchString(strings.TrimSpace(lines[i+1])):
			flush()
			out.WriteString("<table>\n<thead><tr>")
			head := cells(line)
			for _, c := range head {
				out.WriteString("<th>" + inline(html.EscapeString(c)) + "</th>")
			}
			out.WriteString("</tr></thead>\n<tbody>\n")
			for i += 2; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				row := cells(strings.TrimSpace(lines[i]))
				out.WriteString("<tr>")
				for n := range head { // as many cells as the head has
					c := ""
					if n < len(row) {
						c = row[n]
					}
					out.WriteString("<td>" + inline(html.EscapeString(c)) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			i--
			out.WriteString("</tbody>\n</table>\n")
		case strings.HasPrefix(line, "### "), strings.HasPrefix(line, "## "), strings.HasPrefix(line, "# "):
			flush()
			n := strings.Index(line, " ")
			tag := "h" + string(rune('1'+n)) // # → h2: the page title is the h1
			text := inline(html.EscapeString(line[n+1:]))
			plain := html.UnescapeString(reTag.ReplaceAllString(text, ""))
			id := anchor(plain, seen)
			heads = append(heads, Heading{Level: n, Text: plain, ID: id})
			out.WriteString("<" + tag + ` id="` + html.EscapeString(id) + `">` + text + "</" + tag + ">\n")
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "), reNumber.MatchString(line):
			kind, text := "ul", line[2:]
			if m := reNumber.FindString(line); m != "" {
				kind, text = "ol", line[len(m):]
			}
			if len(para) > 0 || list != kind {
				flush()
				out.WriteString("<" + kind + ">\n")
				list = kind
			}
			if m := reCheck.FindStringSubmatch(text); m != nil && kind == "ul" { // a checklist item
				box := `<input type="checkbox" disabled>`
				if m[1] != " " {
					box = `<input type="checkbox" checked disabled>`
				}
				out.WriteString(`<li class="check">` + box + " " + inline(html.EscapeString(text[len(m[0]):])) + "</li>\n")
				continue
			}
			out.WriteString("<li>" + inline(html.EscapeString(text)) + "</li>\n")
		case strings.HasPrefix(line, "> "), line == ">":
			flush()
			out.WriteString("<blockquote>" + inline(html.EscapeString(strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))) + "</blockquote>\n")
		default:
			if list != "" {
				flush()
			}
			para = append(para, inline(esc))
		}
	}
	flush()
	return template.HTML(out.String()), heads
}

// Mark escapes a found snippet and marks the words of the query in it.
func Mark(text, q string) template.HTML {
	q = strings.TrimSpace(q)
	if q == "" {
		return template.HTML(html.EscapeString(text))
	}
	re, err := regexp.Compile(`(?i)` + regexp.QuoteMeta(q))
	if err != nil {
		return template.HTML(html.EscapeString(text))
	}
	var out strings.Builder
	last := 0
	for _, m := range re.FindAllStringIndex(text, -1) {
		out.WriteString(html.EscapeString(text[last:m[0]]) + "<mark>" + html.EscapeString(text[m[0]:m[1]]) + "</mark>")
		last = m[1]
	}
	out.WriteString(html.EscapeString(text[last:]))
	return template.HTML(out.String())
}
