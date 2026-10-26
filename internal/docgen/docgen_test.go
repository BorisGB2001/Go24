package docgen

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func docx(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for n, c := range files {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(c))
	}
	w.Close()
	return b.Bytes()
}

func part(t *testing.T, doc []byte, name string) string {
	r, err := zip.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range r.File {
		if f.Name == name {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			return string(b)
		}
	}
	t.Fatalf("no %s", name)
	return ""
}

func TestFill(t *testing.T) {
	body := `<w:p><w:r><w:t>Договор {Title} от {Date}</w:t></w:r><w:r><w:t>Сумма: {Am</w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>ount} {Unknown} {a b}</w:t></w:r></w:p>`
	doc := docx(t, map[string]string{"word/document.xml": body, "word/footer1.xml": "<w:t>{Company}</w:t>", "docProps/app.xml": "{Title}"})
	out, err := Fill(doc, map[string]string{"Title": "№5 <Поставка>", "Date": "13.10.2026", "Amount": "1 000 RUB", "Company": "ООО «Ромашка» & Ко"})
	if err != nil {
		t.Fatal(err)
	}
	got := part(t, out, "word/document.xml")
	for _, want := range []string{"Договор №5 &lt;Поставка&gt; от 13.10.2026", "Сумма: 1 000 RUB {Unknown}", "{Unknown}", "{a b}"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %s", want, got)
		}
	}
	if strings.Count(got, "<w:p>") != strings.Count(got, "</w:p>") || strings.Count(got, "<w:t>") != strings.Count(got, "</w:t>") {
		t.Fatalf("broken XML: %s", got)
	}
	if f := part(t, out, "word/footer1.xml"); f != "<w:t>ООО «Ромашка» &amp; Ко</w:t>" {
		t.Fatalf("footer = %s", f)
	}
	if part(t, out, "docProps/app.xml") != "{Title}" {
		t.Fatal("only the document's text parts are filled")
	}
	if _, err := Fill([]byte("not a zip"), nil); err != ErrNotDocx {
		t.Fatalf("not a docx: %v", err)
	}
	if _, err := Fill(docx(t, map[string]string{"a.txt": "x"}), nil); err != ErrNotDocx {
		t.Fatalf("a zip without a document: %v", err)
	}
}

func TestNames(t *testing.T) {
	body := `<w:t>{Title} от {Da</w:t></w:r><w:r><w:t>te}, {Срок поставки}, {Title}, {1x}</w:t>`
	doc := docx(t, map[string]string{"word/document.xml": body, "word/header1.xml": "<w:t>{DocumentNumber}</w:t>", "docProps/app.xml": "{Other}"})
	names, err := Names(doc)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "Title,Date,Срок поставки,DocumentNumber" && got != "DocumentNumber,Title,Date,Срок поставки" {
		t.Fatalf("names = %s", got)
	}
	out, _ := Fill(doc, map[string]string{"Срок поставки": "10 дней"})
	if !strings.Contains(part(t, out, "word/document.xml"), "10 дней") {
		t.Fatal("a field named in Russian is filled")
	}
	if _, err := Names([]byte("x")); err != ErrNotDocx {
		t.Fatal(err)
	}
}

func TestFillRows(t *testing.T) {
	row := `<w:tr><w:trPr/><w:tc><w:p><w:r><w:t>{Product.Number}. {Product.Na</w:t></w:r><w:r><w:t>me}</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>{Product.Sum} {Currency} {Product.Other}</w:t></w:r></w:p></w:tc></w:tr>`
	body := `<w:tbl><w:tr><w:tc><w:p><w:t>Товар</w:t></w:p></w:tc></w:tr>` + row + `<w:tr><w:tc><w:p><w:t>Итого {Total}</w:t></w:p></w:tc></w:tr></w:tbl>` +
		`<w:p><w:pPr/><w:r><w:t>— {Product.Name}</w:t></w:r></w:p><w:p><w:t>{Title}</w:t></w:p>`
	doc := docx(t, map[string]string{"word/document.xml": body})
	items := []map[string]string{{"Number": "1", "Name": "Стол", "Sum": "100"}, {"Number": "2", "Name": "Стул <б/у>", "Sum": "50"}}
	out, err := FillRows(doc, map[string]string{"Currency": "RUB", "Total": "150", "Title": "Счёт"}, Rows{"Product": items})
	if err != nil {
		t.Fatal(err)
	}
	got := part(t, out, "word/document.xml")
	for _, want := range []string{"<w:t>1. Стол</w:t>", "<w:t>100 RUB </w:t>", "<w:t>2. Стул &lt;б/у&gt;</w:t>", "<w:t>50 RUB </w:t>",
		"Итого 150", "<w:t>— Стол</w:t></w:r></w:p><w:p><w:pPr/><w:r><w:t>— Стул &lt;б/у&gt;</w:t>", "<w:t>Счёт</w:t>"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in %s", want, got)
		}
	}
	if strings.Count(got, "<w:tr>") != 4 || strings.Count(got, "</w:tr>") != 4 || strings.Contains(got, "{Product") {
		t.Fatalf("rows: %s", got)
	}
	out, _ = FillRows(doc, nil, Rows{"Product": nil})
	if got := part(t, out, "word/document.xml"); strings.Count(got, "<w:tr>") != 2 || strings.Contains(got, "Product") || !strings.Contains(got, "{Title}") {
		t.Fatalf("no items — no rows: %s", got)
	}
	if got := part(t, mustFill(t, doc), "word/document.xml"); !strings.Contains(got, "{Product.Name}") {
		t.Fatal("a list not given stays as it is")
	}
}

func mustFill(t *testing.T, doc []byte) []byte {
	out, err := Fill(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAppendSheet: the sheet goes at the end of the body, before the body's own section settings, from a new page.
func TestAppendSheet(t *testing.T) {
	var src bytes.Buffer
	zw := zip.NewWriter(&src)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>Договор</w:t></w:r></w:p><w:sectPr><w:pgSz/></w:sectPr></w:body></w:document>`)
	zw.Close()
	out, err := AppendSheet(src.Bytes(), Sheet{Title: "Лист подписания", Lines: []string{"SHA-256: ab<c"},
		Header: []string{"Подписант", "Дата"}, Rows: [][]string{{"Анна & Ко", "25.10.2026\n10:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	rc, _ := zr.File[0].Open()
	data, _ := io.ReadAll(rc)
	x := string(data)
	i, j, k := strings.Index(x, "Договор"), strings.Index(x, `<w:br w:type="page"/>`), strings.Index(x, "<w:sectPr>")
	if i < 0 || j < i || k < j || !strings.Contains(x, "Лист подписания") || !strings.Contains(x, "ab&lt;c") ||
		!strings.Contains(x, "Анна &amp; Ко") || strings.Count(x, "<w:tr>") != 2 || !strings.Contains(x, "10:00") {
		t.Fatalf("sheet: %s", x)
	}
	if _, err := AppendSheet([]byte("not a zip"), Sheet{}); err != ErrNotDocx {
		t.Fatalf("not docx: %v", err)
	}
}
