package mail

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/xml"
	"html"
	"io"
	"mime"
	"net/mail"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// Previews of attachments, as Outlook's: a file opens in go24 without being downloaded. Pictures and PDF are shown
// by the browser itself; the text of office documents (Word, Excel, PowerPoint, OpenDocument), plain text, tables
// (CSV), HTML and letters (.eml) is turned into HTML here — escaped, with no styles or scripts of its own.

// Preview kinds.
const (
	PreviewImage = "image" // the browser shows the file (/files/ID?inline=1)
	PreviewPDF   = "pdf"   // the browser's PDF viewer
	PreviewHTML  = "html"  // the HTML of Preview
	PreviewNone  = ""      // no preview: download it
)

// maxPreview bounds a file previewed and what is read out of an archive (a document is a zip).
const maxPreview = 20 << 20

// maxCells bounds the table cells, maxText the characters of a preview.
const (
	maxCells = 20000
	maxText  = 1 << 20
)

// PreviewKind is how a file of the name and type can be shown.
func PreviewKind(name, contentType string) string {
	ct, ext := strings.ToLower(contentType), strings.ToLower(path.Ext(name))
	switch {
	case strings.HasPrefix(ct, "image/") && ct != "image/svg+xml", oneOf(ext, []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"}):
		return PreviewImage
	case ct == "application/pdf", ext == ".pdf":
		return PreviewPDF
	case docKind(name, contentType) != "":
		return PreviewHTML
	}
	return PreviewNone
}

// Preview is how the file can be shown in go24 (see PreviewKind).
func (f File) Preview() string { return PreviewKind(f.Name, f.ContentType) }

// docKind is what Preview makes HTML of: docx, xlsx, pptx, odt, ods, odp, csv, html, eml, text ("" — nothing).
func docKind(name, contentType string) string {
	ct, ext := strings.ToLower(contentType), strings.ToLower(path.Ext(name))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	switch ext {
	case ".docx", ".docm", ".dotx":
		return "docx"
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".pptx", ".ppsx":
		return "pptx"
	case ".odt", ".ods", ".odp":
		return ext[1:]
	case ".csv":
		return "csv"
	case ".htm", ".html":
		return "html"
	case ".eml":
		return "eml"
	case ".txt", ".log", ".md", ".json", ".xml", ".ini", ".cfg", ".conf", ".yaml", ".yml", ".ics", ".vcf", ".sql", ".tsv":
		return "text"
	}
	switch {
	case ct == "text/csv":
		return "csv"
	case ct == "text/html":
		return "html"
	case ct == "message/rfc822":
		return "eml"
	case strings.HasPrefix(ct, "text/"), ct == "application/json", ct == "application/xml":
		return "text"
	}
	return ""
}

// Preview is the HTML of a document's content; false — it cannot be read.
func Preview(name, contentType string, data []byte) (string, bool) {
	if len(data) > maxPreview {
		return "", false
	}
	var b strings.Builder
	var ok bool
	switch docKind(name, contentType) {
	case "docx":
		ok = officeXML(&b, data, []string{"word/document.xml"})
	case "pptx":
		ok = officeXML(&b, data, slides(data))
	case "odt", "odp":
		ok = officeXML(&b, data, []string{"content.xml"})
	case "xlsx":
		ok = xlsx(&b, data)
	case "ods":
		ok = officeXML(&b, data, []string{"content.xml"})
	case "csv":
		ok = csvTable(&b, data)
	case "html":
		b.WriteString(Sanitize(textOf8(data)))
		ok = true
	case "eml":
		ok = eml(&b, data)
	case "text":
		b.WriteString(`<pre class="pv-text">` + html.EscapeString(clip(textOf8(data), maxText)) + `</pre>`)
		ok = true
	}
	return b.String(), ok
}

// textOf8 is the text of a file: UTF-8 (without its BOM), else Windows-1251, as Russian files often are.
func textOf8(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if utf8.Valid(data) {
		return string(data)
	}
	if s, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
		return string(s)
	}
	return strings.ToValidUTF8(string(data), "�")
}

// zipFile reads a member of an archive (bounded).
func zipFile(z *zip.Reader, name string) ([]byte, bool) {
	for _, f := range z.File {
		if f.Name == name {
			r, err := f.Open()
			if err != nil {
				return nil, false
			}
			defer r.Close()
			b, err := io.ReadAll(io.LimitReader(r, maxPreview))
			return b, err == nil
		}
	}
	return nil, false
}

// slides are the slides of a presentation in their order.
func slides(data []byte) []string {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range z.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			out = append(out, f.Name)
		}
	}
	num := func(s string) int {
		n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(s, "ppt/slides/slide"), ".xml"))
		return n
	}
	sort.Slice(out, func(i, j int) bool { return num(out[i]) < num(out[j]) })
	return out
}

// officeXML writes the text of the parts of a Word, PowerPoint or OpenDocument file: paragraphs, headings, line
// breaks, tables; bold and italic. Each part is a section (a slide).
func officeXML(b *strings.Builder, data []byte, parts []string) bool {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(parts) == 0 {
		return false
	}
	for i, p := range parts {
		x, ok := zipFile(z, p)
		if !ok {
			return false
		}
		if len(parts) > 1 {
			b.WriteString(`<section class="pv-slide"><div class="pv-slide-n">` + strconv.Itoa(i+1) + ` / ` + strconv.Itoa(len(parts)) + `</div>`)
		}
		if !walkDoc(b, x) {
			return false
		}
		if len(parts) > 1 {
			b.WriteString(`</section>`)
		}
		if b.Len() > maxText {
			break
		}
	}
	return true
}

// walkDoc turns a document's XML into HTML: the element names are those of WordprocessingML (w:), DrawingML (a:) and
// OpenDocument (text:, table:).
func walkDoc(b *strings.Builder, x []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(x))
	var stack []string // the HTML elements open
	open := func(tag string) { b.WriteString("<" + tag + ">"); stack = append(stack, tag) }
	shut := func(tag string) {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i] == tag {
				for j := len(stack) - 1; j >= i; j-- {
					b.WriteString("</" + stack[j] + ">")
				}
				stack = stack[:i]
				return
			}
		}
	}
	cells, inText, odf := 0, false, 0 // odf: inside OpenDocument paragraphs
	var para string                   // the paragraph's tag: p or a heading
	for {
		t, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return false
		}
		switch e := t.(type) {
		case xml.StartElement:
			switch sp, n := e.Name.Space, e.Name.Local; {
			case n == "p" && (strings.HasSuffix(sp, "/main") || strings.Contains(sp, "drawingml")), n == "p" && strings.Contains(sp, ":text:"), n == "h" && strings.Contains(sp, ":text:"):
				para = "p"
				if n == "h" {
					para = "h3"
				}
				if strings.Contains(sp, ":text:") {
					odf++
				}
				open(para)
			case n == "pStyle" && strings.HasSuffix(sp, "/main"):
				for _, a := range e.Attr {
					if a.Name.Local == "val" && strings.HasPrefix(strings.ToLower(a.Value), "heading") && para == "p" && len(stack) > 0 && stack[len(stack)-1] == "p" {
						b.WriteString(`<b class="pv-h">`)
						stack = append(stack, "b")
					}
				}
			case n == "tbl", n == "table" && strings.Contains(sp, ":table:"):
				open("table")
			case n == "tr", n == "table-row":
				open("tr")
			case n == "tc", n == "table-cell":
				if cells++; cells > maxCells {
					return true
				}
				open("td")
			case n == "t" && (strings.HasSuffix(sp, "/main") || strings.Contains(sp, "drawingml")):
				inText = true
			case n == "tab":
				b.WriteString("\t")
			case n == "br", n == "line-break":
				b.WriteString("<br>")
			case n == "s" && strings.Contains(sp, ":text:"):
				b.WriteString(" ")
			}
		case xml.EndElement:
			switch sp, n := e.Name.Space, e.Name.Local; {
			case n == "p" && (strings.HasSuffix(sp, "/main") || strings.Contains(sp, "drawingml")), n == "p" && strings.Contains(sp, ":text:"), n == "h" && strings.Contains(sp, ":text:"):
				if strings.Contains(sp, ":text:") && odf > 0 {
					odf--
				}
				shut(para)
			case n == "tbl", n == "table" && strings.Contains(sp, ":table:"):
				shut("table")
			case n == "tr", n == "table-row":
				shut("tr")
			case n == "tc", n == "table-cell":
				shut("td")
			case n == "t" && (strings.HasSuffix(sp, "/main") || strings.Contains(sp, "drawingml")):
				inText = false
			}
		case xml.CharData:
			// Word and PowerPoint keep text in w:t and a:t; OpenDocument keeps it right in its paragraphs
			if inText || odf > 0 {
				b.WriteString(html.EscapeString(string(e)))
			}
		}
		if b.Len() > maxText {
			break
		}
	}
	for len(stack) > 0 {
		shut(stack[len(stack)-1])
	}
	return true
}

// xlsx writes the sheets of a workbook as tables, each under its name.
func xlsx(b *strings.Builder, data []byte) bool {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return false
	}
	var shared []string
	if x, ok := zipFile(z, "xl/sharedStrings.xml"); ok {
		var sst struct {
			SI []struct {
				T string `xml:"t"`
				R []struct {
					T string `xml:"t"`
				} `xml:"r"`
			} `xml:"si"`
		}
		if xml.Unmarshal(x, &sst) == nil {
			for _, si := range sst.SI {
				t := si.T
				for _, r := range si.R {
					t += r.T
				}
				shared = append(shared, t)
			}
		}
	}
	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		R []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	x, ok := zipFile(z, "xl/workbook.xml")
	if !ok || xml.Unmarshal(x, &wb) != nil {
		return false
	}
	if x, ok := zipFile(z, "xl/_rels/workbook.xml.rels"); ok {
		_ = xml.Unmarshal(x, &rels)
	}
	cells := 0
	for i, sh := range wb.Sheets {
		target := "xl/worksheets/sheet" + strconv.Itoa(i+1) + ".xml"
		for _, r := range rels.R {
			if r.ID == sh.RID {
				target = path.Join("xl", strings.TrimPrefix(r.Target, "/xl/"))
				if strings.HasPrefix(r.Target, "/") {
					target = strings.TrimPrefix(r.Target, "/")
				}
			}
		}
		x, ok := zipFile(z, target)
		if !ok {
			continue
		}
		var ws struct {
			Rows []struct {
				C []struct {
					R  string `xml:"r,attr"`
					T  string `xml:"t,attr"`
					V  string `xml:"v"`
					IS struct {
						T string `xml:"t"`
					} `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		if xml.Unmarshal(x, &ws) != nil {
			continue
		}
		b.WriteString(`<h3 class="pv-sheet">` + html.EscapeString(sh.Name) + `</h3><table>`)
		for _, row := range ws.Rows {
			b.WriteString("<tr>")
			col := 0
			for _, c := range row.C {
				if cells++; cells > maxCells {
					break
				}
				for want := colOf(c.R); want > col && want-col < 100; col++ { // empty cells are not written
					b.WriteString("<td></td>")
				}
				v := c.V
				switch c.T {
				case "s":
					if n, err := strconv.Atoi(c.V); err == nil && n >= 0 && n < len(shared) {
						v = shared[n]
					}
				case "inlineStr":
					v = c.IS.T
				case "b":
					v = map[string]string{"1": "TRUE", "0": "FALSE"}[c.V]
				}
				b.WriteString("<td>" + html.EscapeString(v) + "</td>")
				col++
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</table>")
	}
	return true
}

// colOf is the column of a cell's reference (A1 → 0, C7 → 2).
func colOf(ref string) int {
	n := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		n = n*26 + int(r-'A'+1)
	}
	return n - 1
}

// csvTable writes a CSV file (commas or semicolons) as a table.
func csvTable(b *strings.Builder, data []byte) bool {
	text := textOf8(data)
	r := csv.NewReader(strings.NewReader(text))
	first, _, _ := strings.Cut(text, "\n")
	if strings.Count(first, ";") > strings.Count(first, ",") {
		r.Comma = ';'
	}
	r.FieldsPerRecord, r.LazyQuotes = -1, true
	b.WriteString("<table>")
	cells := 0
	for {
		rec, err := r.Read()
		if err == io.EOF || cells > maxCells {
			break
		}
		if err != nil {
			return false
		}
		b.WriteString("<tr>")
		for _, v := range rec {
			cells++
			b.WriteString("<td>" + html.EscapeString(v) + "</td>")
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table>")
	return true
}

// eml writes a letter attached to a letter: its headers and its text.
func eml(b *strings.Builder, data []byte) bool {
	m, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return false
	}
	dec := new(mime.WordDecoder)
	b.WriteString(`<dl class="pv-head">`)
	for _, h := range []string{"From", "To", "Cc", "Date", "Subject"} {
		if v := m.Header.Get(h); v != "" {
			if d, err := dec.DecodeHeader(v); err == nil {
				v = d
			}
			b.WriteString("<dt>" + h + "</dt><dd>" + html.EscapeString(v) + "</dd>")
		}
	}
	b.WriteString("</dl>")
	p := parseFull(bytes.NewReader(data))
	if p.html != "" {
		b.WriteString(`<div class="pv-mail">` + p.html + `</div>`)
	} else {
		b.WriteString(`<pre class="pv-text">` + html.EscapeString(p.body) + `</pre>`)
	}
	return true
}
