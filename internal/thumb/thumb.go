// Package thumb makes the small pictures of files the lists show, as the previews of the Disk: a picture made
// smaller, the first page of a PDF.
package thumb

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	_ "image/gif" // the pictures read: GIF, JPEG and PNG
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Side is the longest side of a thumbnail: the big tiles of «Диск» on a screen of double density.
const Side = 400

// maxPixels is the largest picture decoded (a 40-megapixel photo takes some 160 MB as it is read).
const maxPixels = 40 << 20

// ErrNone: no thumbnail can be made of the file (not a picture the server reads, a broken file, no pdftoppm).
var ErrNone = errors.New("thumb: none")

// Image is the thumbnail (JPEG) of a picture: GIF (its first frame), JPEG or PNG, on white where it is transparent.
func Image(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrNone
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrNone
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, Shrink(img, Side), &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// PDF is the thumbnail of the first page of a PDF, drawn by pdftoppm (poppler) when the image has it.
func PDF(ctx context.Context, data []byte) ([]byte, error) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil || !bytes.HasPrefix(bytes.TrimLeft(data, "\x00\t\r\n "), []byte("%PDF")) {
		return nil, ErrNone
	}
	dir, err := os.MkdirTemp("", "go24-thumb-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// -singlefile: the page is page.png, not page-1.png; -scale-to: the longest side
	cmd := exec.CommandContext(ctx, bin, "-q", "-f", "1", "-l", "1", "-singlefile", "-png", "-scale-to", "800", "-", filepath.Join(dir, "page"))
	cmd.Stdin = bytes.NewReader(data)
	if err := cmd.Run(); err != nil {
		return nil, ErrNone
	}
	page, err := os.ReadFile(filepath.Join(dir, "page.png"))
	if err != nil {
		return nil, ErrNone
	}
	return Image(page)
}

// Shrink scales the picture down so its longest side is at most side: each pixel is the mean of up to 4×4 points of
// the part of the picture it covers (enough for a thumbnail, and quick on a photo of many megapixels); transparent
// parts are put on white.
func Shrink(img image.Image, side int) image.Image {
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	nw, nh := w, h
	if w > side || h > side {
		nw, nh = side, h*side/w
		if h > w {
			nw, nh = w*side/h, side
		}
	}
	nw, nh = max(nw, 1), max(nh, 1)
	k := min(4, max(1, (w+nw-1)/nw)) // the points of a side of an output pixel
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			var r, g, b uint64
			for j := 0; j < k; j++ {
				sy := bnd.Min.Y + (y*k+j)*h/(nh*k)
				for i := 0; i < k; i++ {
					sx := bnd.Min.X + (x*k+i)*w/(nw*k)
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, b = r+uint64(cr+0xffff-ca), g+uint64(cg+0xffff-ca), b+uint64(cb+0xffff-ca) // RGBA is premultiplied
				}
			}
			n := uint64(k * k)
			out.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(b / n), A: 0xffff})
		}
	}
	return out
}
