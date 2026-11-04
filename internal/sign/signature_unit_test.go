package sign

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func makeTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 0, G: 0, B: 255, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makeTestPNG(%d, %d) failed: %v", w, h, err)
	}
	return buf.Bytes()
}

func makeTestJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("makeTestJPEG(%d, %d) failed: %v", w, h, err)
	}
	return buf.Bytes()
}

func TestSignature_EmptyOrWhitespace(t *testing.T) {
	for _, input := range []string{"", "   ", "\t\n\r  ", " \n "} {
		got, err := Signature(input)
		if err != nil {
			t.Errorf("Signature(%q) returned error: %v", input, err)
		}
		if got != nil {
			t.Errorf("Signature(%q) = %v, want nil", input, got)
		}
	}
}

func TestSignature_InvalidBase64(t *testing.T) {
	inputs := []string{
		"not-valid-base64!!!",
		"data:image/png;base64,not-valid-base64!!!",
		"data:image/png", // no comma
		"data:,",
	}
	for _, input := range inputs {
		_, err := Signature(input)
		if !errors.Is(err, ErrSignature) {
			t.Errorf("Signature(%q) error = %v, want ErrSignature", input, err)
		}
	}
}

func TestSignature_ExceedsSize(t *testing.T) {
	// Decoded length exceeding SignatureMax (1 << 20)
	tooBig := strings.Repeat("A", (SignatureMax*4/3)+100)
	_, err := Signature(tooBig)
	if !errors.Is(err, ErrSignature) {
		t.Errorf("Signature(tooBig) error = %v, want ErrSignature", err)
	}
}

func TestSignature_ValidPNG(t *testing.T) {
	rawPNG := makeTestPNG(t, 120, 60)
	b64 := base64.StdEncoding.EncodeToString(rawPNG)

	// Test plain base64
	got, err := Signature(b64)
	if err != nil {
		t.Fatalf("Signature(plain base64) error: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("DecodeConfig failed on returned PNG: %v", err)
	}
	if cfg.Width != 120 || cfg.Height != 60 {
		t.Errorf("PNG dimensions = %dx%d, want 120x60", cfg.Width, cfg.Height)
	}

	// Test data URL with whitespace
	dataURL := "  data:image/png;base64," + b64 + " \n"
	gotURL, err := Signature(dataURL)
	if err != nil {
		t.Fatalf("Signature(dataURL) error: %v", err)
	}
	if !bytes.Equal(got, gotURL) {
		t.Errorf("Signature(dataURL) output differs from plain base64")
	}
}

func TestSignature_ValidJPEG_ReencodedAsPNG(t *testing.T) {
	rawJPEG := makeTestJPEG(t, 80, 40)
	b64 := base64.StdEncoding.EncodeToString(rawJPEG)
	dataURL := "data:image/jpeg;base64," + b64

	got, err := Signature(dataURL)
	if err != nil {
		t.Fatalf("Signature(JPEG) error: %v", err)
	}
	// Verify it was re-encoded as PNG
	pngMagic := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(got, pngMagic) {
		t.Errorf("returned signature is not a PNG (missing PNG magic prefix)")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("DecodeConfig failed on returned image: %v", err)
	}
	if format != "png" {
		t.Errorf("format = %q, want png", format)
	}
	if cfg.Width != 80 || cfg.Height != 40 {
		t.Errorf("re-encoded dimensions = %dx%d, want 80x40", cfg.Width, cfg.Height)
	}
}

func TestSignatureImage_LimitsAndBoundaries(t *testing.T) {
	// Empty data
	if _, err := SignatureImage(nil); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(nil) error = %v, want ErrSignature", err)
	}
	if _, err := SignatureImage([]byte{}); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage([]byte{}) error = %v, want ErrSignature", err)
	}

	// Data exceeding SignatureMax (1 << 20)
	tooLarge := make([]byte, SignatureMax+1)
	if _, err := SignatureImage(tooLarge); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(len > SignatureMax) error = %v, want ErrSignature", err)
	}

	// Non-image garbage data
	if _, err := SignatureImage([]byte("not an image at all")); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(garbage) error = %v, want ErrSignature", err)
	}

	// Truncated image data
	validPNG := makeTestPNG(t, 10, 10)
	if _, err := SignatureImage(validPNG[:15]); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(truncated PNG) error = %v, want ErrSignature", err)
	}

	// Boundary 1x1: allowed
	png1x1 := makeTestPNG(t, 1, 1)
	if _, err := SignatureImage(png1x1); err != nil {
		t.Errorf("SignatureImage(1x1) error = %v, want nil", err)
	}

	// Boundary 2000x1: allowed (signatureSide = 2000)
	png2000x1 := makeTestPNG(t, 2000, 1)
	if _, err := SignatureImage(png2000x1); err != nil {
		t.Errorf("SignatureImage(2000x1) error = %v, want nil", err)
	}

	// Boundary 1x2000: allowed
	png1x2000 := makeTestPNG(t, 1, 2000)
	if _, err := SignatureImage(png1x2000); err != nil {
		t.Errorf("SignatureImage(1x2000) error = %v, want nil", err)
	}

	// Exceeds side limit 2001x1: rejected
	png2001x1 := makeTestPNG(t, 2001, 1)
	if _, err := SignatureImage(png2001x1); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(2001x1) error = %v, want ErrSignature", err)
	}

	// Exceeds side limit 1x2001: rejected
	png1x2001 := makeTestPNG(t, 1, 2001)
	if _, err := SignatureImage(png1x2001); !errors.Is(err, ErrSignature) {
		t.Errorf("SignatureImage(1x2001) error = %v, want ErrSignature", err)
	}
}

func TestSignatureConstants(t *testing.T) {
	if SignatureMax != 1<<20 {
		t.Errorf("SignatureMax = %d, want %d", SignatureMax, 1<<20)
	}
	if signatureSide != 2000 {
		t.Errorf("signatureSide = %d, want 2000", signatureSide)
	}
	if ErrSignature == nil || ErrSignature.Error() == "" {
		t.Errorf("ErrSignature is invalid: %v", ErrSignature)
	}
}
