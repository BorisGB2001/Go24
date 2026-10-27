package thumb

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os/exec"
	"testing"
)

func TestImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1200; x++ {
			if x < 600 {
				src.Set(x, y, color.NRGBA{R: 255, A: 255})
			} // the right half is transparent: white in the thumbnail
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, src)
	data, err := Image(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != Side || b.Dy() != Side/2 {
		t.Fatalf("size %v", b)
	}
	if r, g, _, _ := img.At(50, 50).RGBA(); r>>8 < 200 || g>>8 > 60 {
		t.Errorf("left is not red: %d %d", r>>8, g>>8)
	}
	if r, g, b, _ := img.At(350, 50).RGBA(); r>>8 < 230 || g>>8 < 230 || b>>8 < 230 {
		t.Errorf("transparent is not white: %d %d %d", r>>8, g>>8, b>>8)
	}
	// a small picture keeps its size; not a picture — none
	small := image.NewRGBA(image.Rect(0, 0, 30, 20))
	buf.Reset()
	_ = png.Encode(&buf, small)
	if data, err := Image(buf.Bytes()); err != nil {
		t.Fatal(err)
	} else if cfg, _ := jpeg.DecodeConfig(bytes.NewReader(data)); cfg.Width != 30 || cfg.Height != 20 {
		t.Errorf("small %dx%d", cfg.Width, cfg.Height)
	}
	if _, err := Image([]byte("not a picture")); !errors.Is(err, ErrNone) {
		t.Errorf("text: %v", err)
	}
}

func TestPDF(t *testing.T) {
	if _, err := PDF(context.Background(), []byte("not a pdf")); !errors.Is(err, ErrNone) {
		t.Errorf("text: %v", err)
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("no pdftoppm")
	}
	data, err := PDF(context.Background(), []byte(minimalPDF))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Height != Side || cfg.Width >= cfg.Height { // a portrait page
		t.Fatalf("pdf thumb %v %dx%d", err, cfg.Width, cfg.Height)
	}
}

// minimalPDF is one A4 page with a line of text.
const minimalPDF = `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj
4 0 obj << /Length 44 >> stream
BT /F1 24 Tf 72 720 Td (Hello go24) Tj ET
endstream endobj
5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj
trailer << /Root 1 0 R >>
%%EOF
`
