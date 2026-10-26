package docgen

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"slices"
	"strings"
	"testing"
)

// TestMakeImages: the placeholders of images — split across runs too, in the body and a header with no
// relationships of its own — become pictures with their files, relationships and content types; an image that is not
// a picture leaves nothing; the parts stay well-formed XML.
func TestMakeImages(t *testing.T) {
	var pngData, jpgData bytes.Buffer
	png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 200, 100)))
	jpeg.Encode(&jpgData, image.NewRGBA(image.Rect(0, 0, 100, 100)), nil)
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	tpl := docx(t, map[string]string{
		"[Content_Types].xml":          `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/_rels/document.xml.rels": relsEmpty,
		"word/document.xml": `<w:document ` + w + `><w:body><w:p><w:r><w:t>Печать: {My</w:t></w:r><w:r><w:t>Company.Stamp} {Title}</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t xml:space="preserve">{MyCompany.AccountantSignature}</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`,
		"word/header1.xml": `<w:hdr ` + w + `><w:p><w:r><w:t>{MyCompany.DirectorSignature}</w:t></w:r></w:p></w:hdr>`,
	})
	out, err := Make(tpl, map[string]string{"Title": "Счёт", "MyCompany.Stamp": "текст"}, nil, map[string]Image{
		"MyCompany.Stamp":               {Data: pngData.Bytes(), Width: Cm(4)},
		"MyCompany.DirectorSignature":   {Data: jpgData.Bytes(), Width: Cm(3), Float: true},
		"MyCompany.AccountantSignature": {Data: []byte("not a picture"), Width: Cm(3)},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := part(t, out, "word/document.xml")
	if !strings.Contains(body, `<wp:extent cx="1440000" cy="720000"/>`) || !strings.Contains(body, `r:embed="rIdGo24Img`) ||
		!strings.Contains(body, "Счёт") || strings.Contains(body, "{") || strings.Contains(body, "текст") {
		t.Fatalf("body: %s", body)
	}
	head := part(t, out, "word/header1.xml")
	if !strings.Contains(head, `<wp:extent cx="1080000" cy="1080000"/>`) || !strings.Contains(head, `<wp:anchor `) ||
		!strings.Contains(head, `<wp:posOffset>-540000</wp:posOffset>`) || strings.Contains(body, `<wp:anchor `) {
		t.Fatalf("header: %s", head)
	}
	rels := part(t, out, "word/_rels/document.xml.rels")
	hrels := part(t, out, "word/_rels/header1.xml.rels")
	if strings.Count(rels, "relationships/image") != 1 || !strings.Contains(rels, `.png"`) || !strings.Contains(hrels, `.jpeg"`) {
		t.Fatalf("rels: %s %s", rels, hrels)
	}
	types := part(t, out, "[Content_Types].xml")
	if !strings.Contains(types, `Extension="png" ContentType="image/png"`) || !strings.Contains(types, `Extension="jpeg" ContentType="image/jpeg"`) {
		t.Fatalf("types: %s", types)
	}
	for _, rel := range []string{rels, hrels} {
		i := strings.Index(rel, `Target="media/`)
		target := rel[i+len(`Target="`) : i+strings.Index(rel[i:], `"/>`)]
		if part(t, out, "word/"+target) == "" {
			t.Fatalf("no picture %s", target)
		}
	}
	for _, x := range []string{body, head, rels, hrels, types} {
		d := xml.NewDecoder(strings.NewReader(x))
		for {
			if _, err := d.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("not well-formed: %v\n%s", err, x)
			}
		}
	}
	// no images — as before
	plain, err := Make(tpl, map[string]string{"Title": "Счёт"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if names, _ := Names(plain); len(names) != 3 || !slices.Contains(names, "MyCompany.Stamp") {
		t.Fatalf("plain: %v", names)
	}
}

// TestAppendSheetImages: a row's picture goes in its last cell, its file, relationship (made when the document had
// none) and content type with it; a row with no picture keeps its text; the document stays well-formed XML.
func TestAppendSheetImages(t *testing.T) {
	var pngData bytes.Buffer
	png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 300, 100)))
	const w = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	src := docx(t, map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
		"word/document.xml":   `<w:document ` + w + `><w:body><w:p><w:r><w:t>Договор</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`,
	})
	out, err := AppendSheet(src, Sheet{Title: "Лист подписания", Header: []string{"Подписант", "Подпись"},
		Rows:   [][]string{{"Анна", ""}, {"Борис", "—"}},
		Images: map[int]Image{0: {Data: pngData.Bytes(), Width: Cm(3)}, 1: {Data: []byte("not a picture"), Width: Cm(3)}}})
	if err != nil {
		t.Fatal(err)
	}
	body := part(t, out, "word/document.xml")
	if strings.Count(body, "<w:drawing>") != 1 || !strings.Contains(body, `<wp:extent cx="1080000" cy="360000"/>`) ||
		!strings.Contains(body, `r:embed="rIdGo24Sig1"`) || !strings.Contains(body, "—") {
		t.Fatalf("body: %s", body)
	}
	if err := xml.Unmarshal([]byte(body), new(any)); err != nil {
		t.Fatalf("xml: %v", err)
	}
	rels := part(t, out, "word/_rels/document.xml.rels")
	if !strings.Contains(rels, `Target="media/rIdGo24Sig1.png"`) || part(t, out, "word/media/rIdGo24Sig1.png") == "" ||
		!strings.Contains(part(t, out, "[Content_Types].xml"), `Extension="png"`) {
		t.Fatalf("rels: %s", rels)
	}
}
