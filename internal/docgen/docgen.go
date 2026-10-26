// Package docgen fills DOCX templates («Генератор документов»): every {Name} in the text becomes its value. Word
// often splits what the author typed into several runs (<w:r>) — "{Ti" + "tle}" — so a placeholder is found across
// the tags between its braces and those tags go with it.
//
// Lists repeat: the table row (<w:tr>) — or, out of a table, the paragraph — that has a list's placeholder,
// {Product.Name}, is made once for each of the list's items (the products of a deal), and gone when there are none.
package docgen

import (
	"archive/zip"
	"bytes"
	"errors"
	"html"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// ErrNotDocx: the file is not a Word document.
var ErrNotDocx = errors.New("docgen: not a docx file")

// span is "{" … "}" with tags in between allowed; the name is checked after the tags are dropped.
var (
	span = regexp.MustCompile(`\{(?:[^{}<]|<[^>]*>){1,200}?\}`)
	tags = regexp.MustCompile(`<[^>]*>`)
	name = regexp.MustCompile(`^\{(\pL[\pL\pN_. -]*)\}$`) // {Title}, and the cards' own fields by their names: {Срок поставки}
)

// fillXML replaces the known placeholders of one XML part of the document; unknown ones stay as they are. pic is the
// picture's XML of a placeholder of an image (ok false — not an image's).
func fillXML(xml string, values map[string]string, pic func(name string) (string, bool)) string {
	return span.ReplaceAllStringFunc(xml, func(m string) string {
		n := name.FindStringSubmatch(tags.ReplaceAllString(m, ""))
		if n == nil {
			return m
		}
		if pic != nil {
			if x, ok := pic(n[1]); ok {
				return x
			}
		}
		v, ok := values[n[1]]
		if !ok {
			return m
		}
		return html.EscapeString(v)
	})
}

// Rows are the lists of a document by their prefix: "Product" → the items, each its fields by their names (Name, Price…).
type Rows map[string][]map[string]string

// listOf tells the list a placeholder's name belongs to ("Product.Name" → "Product").
func (rows Rows) listOf(n string) (string, bool) {
	p, _, ok := strings.Cut(n, ".")
	if !ok {
		return "", false
	}
	_, ok = rows[p]
	return p, ok
}

// block is where the element of the tag (w:tr, w:p) around pos begins and ends; ok false if pos is out of one.
func block(xml string, pos int, tag string) (start, end int, ok bool) {
	open, closing := "<"+tag, "</"+tag+">"
	for i := pos; ; {
		i = strings.LastIndex(xml[:i], open)
		if i < 0 {
			return 0, 0, false
		}
		if c := xml[i+len(open)]; c == '>' || c == ' ' { // not <w:trPr>, <w:pPr>
			start = i
			break
		}
	}
	if strings.Contains(xml[start:pos], closing) {
		return 0, 0, false
	}
	e := strings.Index(xml[pos:], closing)
	if e < 0 {
		return 0, 0, false
	}
	return start, pos + e + len(closing), true
}

// fillRows makes the blocks with the lists' placeholders once for each item; an item's field it has not is empty.
func fillRows(xml string, rows Rows) string {
	if len(rows) == 0 {
		return xml
	}
	var out strings.Builder
	for {
		var list string
		loc := span.FindAllStringIndex(xml, -1)
		at := -1
		for _, l := range loc {
			if n := name.FindStringSubmatch(tags.ReplaceAllString(xml[l[0]:l[1]], "")); n != nil {
				if p, ok := rows.listOf(n[1]); ok {
					list, at = p, l[0]
					break
				}
			}
		}
		if at < 0 {
			out.WriteString(xml)
			return out.String()
		}
		start, end, ok := block(xml, at, "w:tr")
		if !ok {
			if start, end, ok = block(xml, at, "w:p"); !ok {
				start, end = at, at+len(span.FindString(xml[at:]))
			}
		}
		out.WriteString(xml[:start])
		for _, item := range rows[list] {
			out.WriteString(span.ReplaceAllStringFunc(fillXML(xml[start:end], prefixed(list, item), nil), func(m string) string {
				if n := name.FindStringSubmatch(tags.ReplaceAllString(m, "")); n != nil && strings.HasPrefix(n[1], list+".") {
					return ""
				}
				return m
			}))
		}
		xml = xml[end:]
	}
}

// prefixed are an item's fields by the names the template has them: "Product.Name".
func prefixed(list string, item map[string]string) map[string]string {
	v := make(map[string]string, len(item))
	for k, x := range item {
		v[list+"."+k] = x
	}
	return v
}

// textPart is a part of the document that has text with placeholders: the body, headers and footers.
func textPart(name string) bool {
	return name == "word/document.xml" || strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") ||
		strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml")
}

// Names are the placeholders the template has, each once, in the order of the text («Изменить значения» lists them).
func Names(template []byte) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return nil, ErrNotDocx
	}
	var names []string
	seen := map[string]bool{}
	for _, f := range zr.File {
		if !textPart(f.Name) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 50<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		for _, m := range span.FindAllString(string(data), -1) {
			if n := name.FindStringSubmatch(tags.ReplaceAllString(m, "")); n != nil && !seen[n[1]] {
				seen[n[1]] = true
				names = append(names, n[1])
			}
		}
	}
	return names, nil
}

// Fill makes the document: the template with the values put in its text, headers and footers.
func Fill(template []byte, values map[string]string) ([]byte, error) {
	return FillRows(template, values, nil)
}

// FillRows is Fill with the lists repeated by their items.
func FillRows(template []byte, values map[string]string, rows Rows) ([]byte, error) {
	return Make(template, values, rows, nil)
}

// Make is FillRows with pictures: the placeholders of the images (by their names) become them; an image that is not
// a PNG nor a JPEG — nothing.
func Make(template []byte, values map[string]string, rows Rows, images map[string]Image) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(template), int64(len(template)))
	if err != nil {
		return nil, ErrNotDocx
	}
	type part struct {
		hdr  zip.FileHeader
		data []byte
	}
	var parts []part
	at := map[string]int{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(rc, 50<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		at[f.Name] = len(parts)
		parts = append(parts, part{f.FileHeader, data})
	}
	if _, ok := at["word/document.xml"]; !ok {
		return nil, ErrNotDocx
	}
	pics := map[string]*picture{}
	for k, img := range images {
		pics[k] = pictureOf(img)
	}
	n := 0
	used := map[string][]*picture{} // by the part they are in
	exts := map[string]bool{}
	for i := range parts {
		name := parts[i].hdr.Name
		if !textPart(name) {
			continue
		}
		parts[i].data = []byte(fillXML(fillRows(string(parts[i].data), rows), values, func(k string) (string, bool) {
			p, ok := pics[k]
			if !ok || p == nil {
				return "", ok
			}
			n++
			q := *p
			q.rel = "rIdGo24Img" + strconv.Itoa(n)
			used[name], exts[q.ext] = append(used[name], &q), true
			return drawing(&q, 90000+n), true
		}))
	}
	for name, list := range used {
		rels := relsOf(name)
		if j, ok := at[rels]; ok {
			parts[j].data = []byte(addRels(string(parts[j].data), list))
		} else {
			at[rels] = len(parts)
			parts = append(parts, part{zip.FileHeader{Name: rels, Method: zip.Deflate}, []byte(addRels("", list))})
		}
		for _, p := range list {
			parts = append(parts, part{zip.FileHeader{Name: path.Join(path.Dir(name), "media", p.rel+"."+p.ext), Method: zip.Deflate}, p.img.Data})
		}
	}
	if j, ok := at["[Content_Types].xml"]; ok && len(exts) > 0 {
		parts[j].data = []byte(addTypes(string(parts[j].data), exts))
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, p := range parts {
		hdr := p.hdr
		w, err := zw.CreateHeader(&hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(p.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
