package docgen

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestImagePDF(t *testing.T) {
	if _, err := ImagePDF([]byte("not a picture")); err != ErrNotPDF {
		t.Fatalf("text: %v", err)
	}
	img := image.NewNRGBA(image.Rect(0, 0, 400, 200)) // landscape, transparent
	img.Set(10, 10, color.NRGBA{R: 255, A: 255})
	var p, j bytes.Buffer
	if err := png.Encode(&p, img); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&j, image.NewGray(image.Rect(0, 0, 100, 300)), nil); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		data       []byte
		box, space string
		same       bool
	}{
		"png":  {p.Bytes(), "/MediaBox [0 0 842.00 595.00]", "/DeviceRGB", false},
		"jpeg": {j.Bytes(), "/MediaBox [0 0 595.00 842.00]", "/DeviceGray", true},
	} {
		pdf, err := ImagePDF(c.data)
		if err != nil || !IsPDF(pdf) || !bytes.Contains(pdf, []byte(c.box)) || !bytes.Contains(pdf, []byte(c.space)) ||
			bytes.Contains(pdf, c.data) != c.same || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
			t.Fatalf("%s: %v %q", name, err, pdf[:min(len(pdf), 400)])
		}
		// the xref points at the objects
		x := bytes.LastIndex(pdf, []byte("startxref\n"))
		if x < 0 || !bytes.HasPrefix(pdf[atoi(t, pdf[x+10:]):], []byte("xref\n0 6\n")) {
			t.Fatalf("%s: startxref", name)
		}
	}
	if pdf, _ := ImagePDF(j.Bytes()); bytes.Index(pdf, []byte("4 0 obj")) != atoi(t, pdf[bytes.Index(pdf, []byte("0000000000 65535 f \n"))+20+3*20:]) {
		t.Fatal("xref of the picture")
	}
}

func atoi(t *testing.T, b []byte) int {
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func TestSheetDoc(t *testing.T) {
	doc, err := SheetDoc(Sheet{Title: "Лист подписания", Lines: []string{"Документ: Акт"}, Header: []string{"Кто"}, Rows: [][]string{{"Анна"}}})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(doc), int64(len(doc)))
	if err != nil {
		t.Fatal(err)
	}
	var body string
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			body = string(b)
		}
	}
	if !strings.Contains(body, "Лист подписания") || !strings.Contains(body, "Анна") || strings.Contains(body, `w:type="page"`) ||
		!strings.HasSuffix(body, `</w:sectPr></w:body></w:document>`) {
		t.Fatalf("sheet doc: %s", body)
	}
	if _, err := Names(doc); err != nil {
		t.Fatal(err)
	}
}

func TestJoinPDF(t *testing.T) {
	if _, err := exec.LookPath("pdfunite"); err != nil {
		t.Skip("no pdfunite")
	}
	a, _ := ImagePDF(mustPNG(t, 100, 200))
	b, _ := ImagePDF(mustPNG(t, 200, 100))
	if _, err := JoinPDF(context.Background(), a, []byte("text")); err != ErrNotPDF {
		t.Fatalf("text: %v", err)
	}
	out, err := JoinPDF(context.Background(), a, b)
	if err != nil || !IsPDF(out) || bytes.Count(out, []byte("/Type /Page\n"))+bytes.Count(out, []byte("/Type /Page ")) < 2 {
		t.Fatalf("join: %v", err)
	}
}

func mustPNG(t *testing.T, w, h int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImagesPDF(t *testing.T) {
	if _, err := ImagesPDF(); err != ErrNotPDF {
		t.Fatalf("none: %v", err)
	}
	if _, err := ImagesPDF(mustPNG(t, 10, 10), []byte("text")); err != ErrNotPDF {
		t.Fatalf("text: %v", err)
	}
	pdf, err := ImagesPDF(mustPNG(t, 100, 200), mustPNG(t, 300, 100), mustPNG(t, 50, 50))
	if err != nil || !IsPDF(pdf) || !bytes.Contains(pdf, []byte("/Kids [3 0 R 6 0 R 9 0 R] /Count 3")) ||
		bytes.Count(pdf, []byte("/Type /Page /Parent")) != 3 || !bytes.Contains(pdf, []byte("/MediaBox [0 0 842.00 595.00]")) ||
		!bytes.Contains(pdf, []byte("xref\n0 12\n")) {
		t.Fatalf("pages: %v", err)
	}
	if _, err := exec.LookPath("pdfinfo"); err == nil {
		dir := t.TempDir()
		name := filepath.Join(dir, "p.pdf")
		_ = os.WriteFile(name, pdf, 0o600)
		if out, err := exec.Command("pdfinfo", name).CombinedOutput(); err != nil ||
			!regexp.MustCompile(`Pages:\s+3`).Match(out) {
			t.Fatalf("pdfinfo: %v %s", err, out)
		}
	}
}
