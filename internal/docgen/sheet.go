package docgen

import (
	"archive/zip"
	"bytes"
	"html"
	"io"
	"strconv"
	"strings"
)

// Sheet is a page put at the end of a document: a heading, lines of text and a table (the signing sheet of «Подпись»).
type Sheet struct {
	Title  string
	Lines  []string
	Header []string
	Rows   [][]string
}

func para(text string, bold bool, size int) string {
	rpr := ""
	if bold || size > 0 {
		rpr = "<w:rPr>"
		if bold {
			rpr += "<w:b/>"
		}
		if size > 0 {
			rpr += `<w:sz w:val="` + strconv.Itoa(size) + `"/>`
		}
		rpr += "</w:rPr>"
	}
	return `<w:p><w:r>` + rpr + `<w:t xml:space="preserve">` + html.EscapeString(text) + `</w:t></w:r></w:p>`
}

func cell(text string, bold bool) string {
	var ps strings.Builder
	for _, line := range strings.Split(text, "\n") {
		ps.WriteString(para(line, bold, 18))
	}
	return `<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/></w:tcPr>` + ps.String() + `</w:tc>`
}

// xml is the sheet as WordprocessingML, from a new page.
func (s Sheet) xml() string {
	var b strings.Builder
	b.WriteString(`<w:p><w:r><w:br w:type="page"/></w:r></w:p>`)
	b.WriteString(para(s.Title, true, 28))
	for _, l := range s.Lines {
		b.WriteString(para(l, false, 18))
	}
	if len(s.Header) > 0 {
		border := `<w:top w:val="single" w:sz="4" w:space="0" w:color="999999"/><w:left w:val="single" w:sz="4" w:space="0" w:color="999999"/>` +
			`<w:bottom w:val="single" w:sz="4" w:space="0" w:color="999999"/><w:right w:val="single" w:sz="4" w:space="0" w:color="999999"/>` +
			`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="999999"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="999999"/>`
		b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="5000" w:type="pct"/><w:tblBorders>` + border + `</w:tblBorders></w:tblPr><w:tblGrid>`)
		for range s.Header {
			b.WriteString(`<w:gridCol/>`)
		}
		b.WriteString(`</w:tblGrid><w:tr>`)
		for _, h := range s.Header {
			b.WriteString(cell(h, true))
		}
		b.WriteString(`</w:tr>`)
		for _, row := range s.Rows {
			b.WriteString(`<w:tr>`)
			for _, c := range row {
				b.WriteString(cell(c, false))
			}
			b.WriteString(`</w:tr>`)
		}
		b.WriteString(`</w:tbl><w:p/>`)
	}
	return b.String()
}

// AppendSheet puts the sheet at the end of the document's body (before its last section's settings).
func AppendSheet(doc []byte, s Sheet) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		return nil, ErrNotDocx
	}
	found := false
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
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
		if f.Name == "word/document.xml" {
			x := string(data)
			end := strings.LastIndex(x, "</w:body>")
			if end < 0 {
				return nil, ErrNotDocx
			}
			at := end
			// the body's own section settings stay last: <w:sectPr> right before </w:body>
			if i := strings.LastIndex(x[:end], "<w:sectPr"); i >= 0 && !strings.Contains(x[i:end], "</w:p>") {
				at = i
			}
			data = []byte(x[:at] + s.xml() + x[at:])
			found = true
		}
		hdr := f.FileHeader
		w, err := zw.CreateHeader(&hdr)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(data); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, ErrNotDocx
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
