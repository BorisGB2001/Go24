package mail

import (
	"html"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The HTML of letters. What go24 keeps, shows and puts into the editor is cleaned: only the tags and attributes of
// formatted text stay (no scripts, styles, forms, frames, event handlers, classes or ids — they would act on the
// page itself); links go to http(s), mailto and tel; pictures come from http(s), cid: (the letter's own),
// data: images or go24's files. The reading pane shows it in a sandboxed frame where outside pictures load only
// when the user asks (as Outlook protects privacy).

// maxHTML bounds the HTML kept of a letter.
const maxHTML = 1 << 20

// tags of formatted text kept as they are; others are dropped with what is inside (dropTags) or give way to it
var keepTags = map[atom.Atom]bool{
	atom.A: true, atom.Abbr: true, atom.Address: true, atom.Article: true, atom.B: true, atom.Big: true, atom.Blockquote: true,
	atom.Br: true, atom.Caption: true, atom.Center: true, atom.Cite: true, atom.Code: true, atom.Col: true, atom.Colgroup: true,
	atom.Dd: true, atom.Del: true, atom.Div: true, atom.Dl: true, atom.Dt: true, atom.Em: true, atom.Figcaption: true,
	atom.Figure: true, atom.Font: true, atom.Footer: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true,
	atom.H5: true, atom.H6: true, atom.Header: true, atom.Hr: true, atom.I: true, atom.Img: true, atom.Ins: true, atom.Li: true,
	atom.Mark: true, atom.Ol: true, atom.P: true, atom.Pre: true, atom.Q: true, atom.S: true, atom.Section: true, atom.Small: true,
	atom.Span: true, atom.Strike: true, atom.Strong: true, atom.Sub: true, atom.Sup: true, atom.Table: true, atom.Tbody: true,
	atom.Td: true, atom.Tfoot: true, atom.Th: true, atom.Thead: true, atom.Tr: true, atom.Tt: true, atom.U: true, atom.Ul: true,
}

var dropTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Applet: true,
	atom.Head: true, atom.Title: true, atom.Noscript: true, atom.Template: true, atom.Svg: true, atom.Math: true,
	atom.Frame: true, atom.Frameset: true, atom.Base: true, atom.Link: true, atom.Meta: true, atom.Select: true,
	atom.Textarea: true, atom.Input: true, atom.Button: true, atom.Option: true, atom.Audio: true, atom.Video: true,
	atom.Source: true, atom.Track: true, atom.Canvas: true, atom.Map: true, atom.Area: true, atom.Dialog: true,
}

// attributes kept on any kept tag (href, src, target are checked by the tag)
var keepAttrs = map[string]bool{
	"style": true, "align": true, "valign": true, "dir": true, "title": true, "lang": true, "width": true, "height": true,
	"colspan": true, "rowspan": true, "cellpadding": true, "cellspacing": true, "border": true, "bgcolor": true, "color": true,
	"face": true, "size": true, "start": true, "type": true, "alt": true, "span": true, "nowrap": true,
	"data-signature": true, "data-quote": true, // the editor's marks: the signature (swapped by «Подпись»), the quoted letter
}

// CSS properties kept in style attributes: how text and boxes look, never where they are on the page
var keepCSS = map[string]bool{
	"color": true, "background-color": true, "background": true, "font": true, "font-family": true, "font-size": true,
	"font-style": true, "font-weight": true, "font-variant": true, "text-align": true, "text-decoration": true,
	"text-indent": true, "text-transform": true, "line-height": true, "letter-spacing": true, "word-spacing": true,
	"white-space": true, "word-break": true, "word-wrap": true, "overflow-wrap": true, "vertical-align": true, "direction": true,
	"margin": true, "margin-top": true, "margin-right": true, "margin-bottom": true, "margin-left": true,
	"padding": true, "padding-top": true, "padding-right": true, "padding-bottom": true, "padding-left": true,
	"border": true, "border-top": true, "border-right": true, "border-bottom": true, "border-left": true, "border-color": true,
	"border-style": true, "border-width": true, "border-radius": true, "border-collapse": true, "border-spacing": true,
	"width": true, "max-width": true, "min-width": true, "height": true, "max-height": true, "min-height": true,
	"display": true, "list-style": true, "list-style-type": true, "table-layout": true, "float": true, "clear": true,
	"text-overflow": true, "caption-side": true, "empty-cells": true, "opacity": true,
}

var voidTags = map[atom.Atom]bool{atom.Br: true, atom.Hr: true, atom.Img: true, atom.Col: true}

var (
	reFileSrc = regexp.MustCompile(`^/files/[0-9a-fA-F-]{36}(\?inline=1)?$`)
	reDataImg = regexp.MustCompile(`^data:image/(png|gif|jpeg|webp);base64,[A-Za-z0-9+/=\s]+$`)
)

// Sanitize cleans a letter's HTML (a whole document or a piece of one) down to its formatted text.
func Sanitize(src string) string {
	if len(src) > maxHTML {
		src = src[:maxHTML]
	}
	nodes, err := xhtml.ParseFragment(strings.NewReader(src), &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, n := range nodes {
		clean(&b, n)
	}
	return strings.TrimSpace(b.String())
}

func clean(b *strings.Builder, n *xhtml.Node) {
	switch n.Type {
	case xhtml.TextNode:
		b.WriteString(html.EscapeString(n.Data))
		return
	case xhtml.ElementNode:
	default: // comments (Outlook's conditional ones too), doctypes
		if n.Type == xhtml.DocumentNode {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				clean(b, c)
			}
		}
		return
	}
	if dropTags[n.DataAtom] {
		return
	}
	keep := keepTags[n.DataAtom]
	if keep {
		b.WriteString("<" + n.Data)
		for _, a := range n.Attr {
			if a.Namespace != "" {
				continue
			}
			k, v := strings.ToLower(a.Key), a.Val
			switch {
			case k == "href" && n.DataAtom == atom.A:
				if v = safeHref(v); v == "" {
					continue
				}
			case k == "src" && n.DataAtom == atom.Img:
				if v = safeSrc(v); v == "" {
					continue
				}
			case k == "style":
				if v = safeCSS(v); v == "" {
					continue
				}
			case !keepAttrs[k]:
				continue
			}
			b.WriteString(" " + k + `="` + html.EscapeString(v) + `"`)
		}
		if n.DataAtom == atom.A {
			b.WriteString(` target="_blank" rel="noopener noreferrer"`)
		}
		b.WriteString(">")
		if voidTags[n.DataAtom] {
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		clean(b, c)
	}
	if keep {
		b.WriteString("</" + n.Data + ">")
	}
}

func safeHref(v string) string {
	v = strings.TrimSpace(v)
	low := strings.ToLower(v)
	for _, p := range []string{"http://", "https://", "mailto:", "tel:"} {
		if strings.HasPrefix(low, p) {
			return v
		}
	}
	return ""
}

func safeSrc(v string) string {
	v = strings.TrimSpace(v)
	low := strings.ToLower(v)
	switch {
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"), strings.HasPrefix(low, "cid:"),
		reFileSrc.MatchString(v), len(v) < maxHTML && reDataImg.MatchString(v):
		return v
	}
	return ""
}

// safeCSS keeps the allowed declarations of a style attribute that load nothing and run nothing.
func safeCSS(v string) string {
	var out []string
	for _, d := range strings.Split(v, ";") {
		k, val, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		k, val = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(val)
		low := strings.ToLower(val)
		if !keepCSS[k] || val == "" || strings.ContainsAny(val, `\<>{}@`) || strings.Contains(low, "url(") ||
			strings.Contains(low, "expression") || strings.Contains(low, "javascript") || (k == "display" && strings.Contains(low, "fixed")) {
			continue
		}
		out = append(out, k+": "+val)
	}
	return strings.Join(out, "; ")
}

// HTMLText is the plain text of an HTML letter: paragraphs and line breaks kept, lists with dashes, links with
// their addresses.
func HTMLText(src string) string {
	nodes, err := xhtml.ParseFragment(strings.NewReader(src), &xhtml.Node{Type: xhtml.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, n := range nodes {
		textOf(&b, n)
	}
	t := reSpaces.ReplaceAllString(reTwoSpaces.ReplaceAllString(b.String(), " "), "\n")
	return strings.TrimSpace(reBlank.ReplaceAllString(t, "\n\n"))
}

// squeeze: the white space of HTML text is one space.
func squeeze(s string) string {
	f := strings.Join(strings.Fields(s), " ")
	if f == "" {
		if s != "" {
			return " "
		}
		return ""
	}
	if strings.TrimLeft(s, " \t\r\n") != s {
		f = " " + f
	}
	if strings.TrimRight(s, " \t\r\n") != s {
		f += " "
	}
	return f
}

var (
	reSpaces    = regexp.MustCompile(`[ \t]*\n[ \t]*`)
	reTwoSpaces = regexp.MustCompile(` {2,}`)
	blockTag    = map[atom.Atom]bool{atom.P: true, atom.Div: true, atom.Table: true, atom.Tr: true, atom.Ul: true, atom.Ol: true,
		atom.Blockquote: true, atom.H1: true, atom.H2: true, atom.H3: true, atom.H4: true, atom.H5: true, atom.H6: true,
		atom.Pre: true, atom.Hr: true, atom.Section: true, atom.Article: true, atom.Header: true, atom.Footer: true}
)

func textOf(b *strings.Builder, n *xhtml.Node) {
	switch {
	case n.Type == xhtml.TextNode:
		b.WriteString(squeeze(n.Data))
		return
	case n.Type != xhtml.ElementNode && n.Type != xhtml.DocumentNode, dropTags[n.DataAtom]:
		return
	}
	switch n.DataAtom {
	case atom.Br:
		b.WriteString("\n")
		return
	case atom.Li:
		b.WriteString("\n- ")
	case atom.Td, atom.Th:
		b.WriteString("\t")
	case atom.Img:
		for _, a := range n.Attr {
			if a.Key == "alt" && strings.TrimSpace(a.Val) != "" {
				b.WriteString("[" + strings.TrimSpace(a.Val) + "]")
			}
		}
	}
	if blockTag[n.DataAtom] {
		b.WriteString("\n")
	}
	start := b.Len()
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		textOf(b, c)
	}
	if n.DataAtom == atom.A {
		for _, a := range n.Attr {
			if a.Key == "href" && strings.HasPrefix(strings.ToLower(a.Val), "http") && !strings.Contains(b.String()[start:], a.Val) {
				b.WriteString(" <" + a.Val + ">")
			}
		}
	}
	if blockTag[n.DataAtom] {
		b.WriteString("\n")
	}
}

// TextHTML is a plain text letter as HTML (for the editor and the quote of an answer).
func TextHTML(text string) string {
	if text == "" {
		return ""
	}
	return strings.ReplaceAll(html.EscapeString(strings.ReplaceAll(text, "\r\n", "\n")), "\n", "<br>")
}

var (
	reCID      = regexp.MustCompile(`src="cid:([^"]+)"`)
	reOutside  = regexp.MustCompile(`<img [^>]*src="https?://`)
	reFileImgs = regexp.MustCompile(`src="/files/([0-9a-fA-F-]{36})(?:\?inline=1)?"`)
)

// WithPictures points the letter's own pictures (cid:) at its files kept in go24 (Content-ID → file id).
func WithPictures(h string, byCID map[string]string) string {
	if len(byCID) == 0 {
		return h
	}
	return reCID.ReplaceAllStringFunc(h, func(m string) string {
		cid := strings.Trim(html.UnescapeString(reCID.FindStringSubmatch(m)[1]), "<>")
		if id, ok := byCID[cid]; ok {
			return `src="/files/` + id + `?inline=1"`
		}
		return m
	})
}

// OutsidePictures: does the HTML show pictures from elsewhere (they load only when the user asks)?
func OutsidePictures(h string) bool { return reOutside.MatchString(h) }
