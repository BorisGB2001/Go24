package docgen

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"testing"
)

func TestTextPNG(t *testing.T) {
	data, err := TextPNG("Иванов И. И. 28.10.2026", 40)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil || img.Bounds().Dy() != 40 || img.Bounds().Dx() < 200 {
		t.Fatalf("text png: %v %v", err, img.Bounds())
	}
	inked := 0
	for x := 0; x < img.Bounds().Dx(); x++ {
		for y := 0; y < 40; y++ {
			if _, _, _, a := img.At(x, y).RGBA(); a > 0 {
				inked++
			}
		}
	}
	if inked < 300 {
		t.Fatalf("text png is blank: %d", inked)
	}
}

func TestOverlayPDF(t *testing.T) {
	sig := image.NewNRGBA(image.Rect(0, 0, 20, 10))
	sig.Set(1, 1, color.NRGBA{0, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, sig)
	out, err := overlayPDF([]PageSize{{595, 842}, {842, 595}}, []Stamp{{Page: 2, X: 0.5, Y: 0.5, W: 0.2, H: 0.1, Image: b.Bytes()}, {Page: 3, Image: b.Bytes()}})
	if err != nil || !IsPDF(out) {
		t.Fatal(err)
	}
	for _, want := range []string{"/Count 2", "/SMask", "/MediaBox [0 0 842.00 595.00]", "/Im0 Do"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("overlay: no %q", want)
		}
	}
	if bytes.Contains(out, []byte("/Im1 Do")) {
		t.Fatal("a stamp of a page the document has not")
	}
}

func TestStampPDF(t *testing.T) {
	for _, tool := range []string{"qpdf", "pdfinfo", "pdftoppm"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
	}
	ctx := context.Background()
	white := image.NewRGBA(image.Rect(0, 0, 200, 300))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	var wb bytes.Buffer
	_ = png.Encode(&wb, white)
	doc, err := ImagePDF(wb.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	pages, err := PDFPages(ctx, doc)
	if err != nil || len(pages) != 1 || pages[0].W < 590 || pages[0].H < 840 {
		t.Fatalf("pages: %v %v", pages, err)
	}
	black := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	for i := 3; i < len(black.Pix); i += 4 {
		black.Pix[i] = 255
	}
	var bb bytes.Buffer
	_ = png.Encode(&bb, black)
	out, err := StampPDF(ctx, doc, []Stamp{{Page: 1, X: 0, Y: 0, W: 0.5, H: 0.5, Image: bb.Bytes()}})
	if err != nil {
		t.Fatal(err)
	}
	pic, err := PDFPagePNG(ctx, out, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(pic))
	if err != nil || img.Bounds().Dx() != 100 {
		t.Fatalf("page png: %v", err)
	}
	dark := func(x, y int) bool { r, _, _, _ := img.At(x, y).RGBA(); return r < 0x4000 }
	h := img.Bounds().Dy()
	if !dark(25, h/4) || dark(75, h*3/4) {
		t.Fatal("the stamp is not at the top left quarter")
	}
}
