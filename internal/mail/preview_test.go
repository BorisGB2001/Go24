package mail

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestPreview(t *testing.T) {
	for _, c := range []struct{ name, ct, kind string }{
		{"a.png", "image/png", PreviewImage}, {"a.JPG", "application/octet-stream", PreviewImage}, {"a.svg", "image/svg+xml", PreviewNone},
		{"a.pdf", "application/octet-stream", PreviewPDF}, {"a.docx", "", PreviewHTML}, {"a.xlsx", "", PreviewHTML},
		{"a.csv", "", PreviewHTML}, {"a.txt", "text/plain", PreviewHTML}, {"a.exe", "application/x-msdownload", PreviewNone},
		{"a", "message/rfc822", PreviewHTML}, {"a.doc", "application/msword", PreviewNone},
	} {
		if got := PreviewKind(c.name, c.ct); got != c.kind {
			t.Errorf("PreviewKind(%q, %q) = %q, want %q", c.name, c.ct, got, c.kind)
		}
	}

	w := `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	docx := zipOf(t, map[string]string{"word/document.xml": `<w:document ` + w + `><w:body><w:p><w:r><w:t>Счёт &lt;№5&gt;</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl></w:body></w:document>`})
	h, ok := Preview("a.docx", "", docx)
	if !ok || !strings.Contains(h, "<p>Счёт &lt;№5&gt;</p>") || !strings.Contains(h, "<td><p>A1</p></td><td><p>B1</p></td>") {
		t.Errorf("docx: %v %s", ok, h)
	}

	xl := zipOf(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Итоги" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>Товар</t></si><si><r><t>Ц</t></r><r><t>ена</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>` +
			`<row r="2"><c r="A2"><v>42</v></c></row></sheetData></worksheet>`,
	})
	h, ok = Preview("a.xlsx", "", xl)
	if !ok || !strings.Contains(h, "Итоги") || !strings.Contains(h, "<tr><td>Товар</td><td></td><td>Цена</td></tr><tr><td>42</td></tr>") {
		t.Errorf("xlsx: %v %s", ok, h)
	}

	a := `xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"`
	pp := zipOf(t, map[string]string{"ppt/slides/slide2.xml": `<p:sld ` + a + `><a:p><a:r><a:t>Второй</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide1.xml": `<p:sld ` + a + `><a:p><a:r><a:t>Первый</a:t></a:r></a:p></p:sld>`})
	if h, ok = Preview("a.pptx", "", pp); !ok || strings.Index(h, "Первый") > strings.Index(h, "Второй") || !strings.Contains(h, "1 / 2") {
		t.Errorf("pptx: %v %s", ok, h)
	}

	odt := zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
		`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0"><office:body><text:h>Глава</text:h><text:p>Абзац <text:span>с</text:span> текстом</text:p></office:body></office:document-content>`})
	if h, ok = Preview("a.odt", "", odt); !ok || !strings.Contains(h, "<h3>Глава</h3>") || !strings.Contains(h, "<p>Абзац с текстом</p>") {
		t.Errorf("odt: %v %s", ok, h)
	}

	if h, ok = Preview("a.csv", "", []byte("a;b\n<x>;2\n")); !ok || !strings.Contains(h, "<td>&lt;x&gt;</td><td>2</td>") {
		t.Errorf("csv: %v %s", ok, h)
	}
	if h, ok = Preview("a.txt", "", []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}); !ok || !strings.Contains(h, "Привет") { // Windows-1251
		t.Errorf("text: %v %s", ok, h)
	}
	if h, ok = Preview("a.html", "", []byte(`<p onclick="x()">Hi<script>alert(1)</script></p>`)); !ok || strings.Contains(h, "script") || strings.Contains(h, "onclick") {
		t.Errorf("html: %v %s", ok, h)
	}
	eml := "From: =?UTF-8?B?0JDQvdC90LA=?= <anna@x.test>\r\nSubject: Hello\r\nContent-Type: text/plain\r\n\r\n<b>body</b>\r\n"
	if h, ok = Preview("a.eml", "", []byte(eml)); !ok || !strings.Contains(h, "Анна") || !strings.Contains(h, "&lt;b&gt;body") {
		t.Errorf("eml: %v %s", ok, h)
	}
	if _, ok = Preview("a.docx", "", []byte("not a zip")); ok {
		t.Error("a broken docx was read")
	}
}

func TestSenders(t *testing.T) {
	for in, want := range map[string]string{"Anna <Anna@X.test>": "anna@x.test", "x.test": "@x.test", "@Mail.X.test": "@mail.x.test",
		"anna": "", "a@b": "", "": "", "a b@x.test": "", "@.x": ""} {
		if got := NormSender(in); got != want {
			t.Errorf("NormSender(%q) = %q, want %q", in, got, want)
		}
	}
	if got := strings.Join(senderKeys("A@mail.X.test"), " "); got != "a@mail.x.test @mail.x.test @x.test" {
		t.Errorf("senderKeys = %q", got)
	}
}
