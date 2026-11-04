package docgen

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// Stamp is a picture put on a page of a PDF — a block of «Подпись» (a signature, a stamp, a name, a date): Page from
// 1, the box X, Y (from the page's top left corner), W, H in fractions of the page's size; the picture is fitted
// into the box keeping its proportions, at the box's left and middle.
type Stamp struct {
	Page       int
	X, Y, W, H float64
	Image      []byte // PNG or JPEG
}

// PageSize is the size of a page of a PDF, in points.
type PageSize struct{ W, H float64 }

var pageSizeRe = regexp.MustCompile(`(?m)^Page\s+(\d+)\s+size:\s+([\d.]+)\s+x\s+([\d.]+)`)

// pdfTool runs a poppler or qpdf tool on the PDF written to a temporary folder (in.pdf), args — after the tool's
// name with "IN" for the file; out — the name of the file it makes there, to read (or "" — its output).
func pdfTool(ctx context.Context, pdf []byte, tool, out string, args ...string) ([]byte, error) {
	bin, err := exec.LookPath(tool)
	if err != nil || !IsPDF(pdf) {
		return nil, ErrNotPDF
	}
	dir, err := os.MkdirTemp("", "go24-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(in, pdf, 0o600); err != nil {
		return nil, err
	}
	for i, a := range args {
		switch a {
		case "IN":
			args[i] = in
		case "DIR":
			args[i] = dir
		}
		if strings.HasPrefix(a, "DIR/") {
			args[i] = filepath.Join(dir, a[4:])
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	msg, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", tool, err, bytes.TrimSpace(msg))
	}
	if out == "" {
		return msg, nil
	}
	return os.ReadFile(filepath.Join(dir, out))
}

// PDFPages are the sizes of the pages of a PDF (pdfinfo of poppler).
func PDFPages(ctx context.Context, pdf []byte) ([]PageSize, error) {
	info, err := pdfTool(ctx, pdf, "pdfinfo", "", "-f", "1", "-l", "100000", "IN")
	if err != nil {
		return nil, err
	}
	var out []PageSize
	for _, m := range pageSizeRe.FindAllStringSubmatch(string(info), -1) {
		w, _ := strconv.ParseFloat(m[2], 64)
		h, _ := strconv.ParseFloat(m[3], 64)
		out = append(out, PageSize{w, h})
	}
	if len(out) == 0 {
		return nil, ErrNotPDF
	}
	return out, nil
}

// PDFPagePNG is a page of a PDF (from 1) as a PNG picture width pixels wide (pdftoppm of poppler).
func PDFPagePNG(ctx context.Context, pdf []byte, page, width int) ([]byte, error) {
	n := strconv.Itoa(page)
	return pdfTool(ctx, pdf, "pdftoppm", "page.png", "-f", n, "-l", n, "-png", "-singlefile",
		"-scale-to-x", strconv.Itoa(width), "-scale-to-y", "-1", "IN", "DIR/page")
}

// StampPDF puts the pictures on the pages of the PDF: a PDF of the same pages with them only is laid over the
// document by qpdf. Stamps of pages it has not are left out.
func StampPDF(ctx context.Context, pdf []byte, stamps []Stamp) ([]byte, error) {
	if len(stamps) == 0 {
		return pdf, nil
	}
	if _, err := exec.LookPath("qpdf"); err != nil {
		return nil, ErrNotPDF
	}
	pages, err := PDFPages(ctx, pdf)
	if err != nil {
		return nil, err
	}
	over, err := overlayPDF(pages, stamps)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "go24-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ov := filepath.Join(dir, "over.pdf")
	if err := os.WriteFile(ov, over, 0o600); err != nil {
		return nil, err
	}
	return pdfTool(ctx, pdf, "qpdf", "out.pdf", "--warning-exit-0", "IN", "--overlay", ov, "--", "DIR/out.pdf")
}

// overlayPDF is a PDF of the pages' sizes with the stamps' pictures on them (a picture with transparency — with its
// soft mask).
func overlayPDF(pages []PageSize, stamps []Stamp) ([]byte, error) {
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
	var objs []string // object i+1
	add := func(o string) int { objs = append(objs, o); return len(objs) }
	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("") // the pages, below
	var kids []string
	for i, ps := range pages {
		var content, res strings.Builder
		for j, st := range stamps {
			if st.Page != i+1 {
				continue
			}
			img, _, err := image.Decode(bytes.NewReader(st.Image))
			if err != nil {
				return nil, err
			}
			id, err := imageObj(img, add)
			if err != nil {
				return nil, err
			}
			b := img.Bounds()
			bw, bh := st.W*ps.W, st.H*ps.H
			scale := min(bw/float64(b.Dx()), bh/float64(b.Dy()))
			w, h := float64(b.Dx())*scale, float64(b.Dy())*scale
			x, y := st.X*ps.W, ps.H-st.Y*ps.H-bh+(bh-h)/2
			name := "Im" + strconv.Itoa(j)
			content.WriteString("q " + num(w) + " 0 0 " + num(h) + " " + num(x) + " " + num(y) + " cm /" + name + " Do Q\n")
			fmt.Fprintf(&res, "/%s %d 0 R ", name, id)
		}
		c := content.String()
		cid := add("<< /Length " + strconv.Itoa(len(c)) + " >>\nstream\n" + c + "endstream")
		pid := add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 " + num(ps.W) + " " + num(ps.H) + "] /Resources << /XObject << " +
			res.String() + ">> >> /Contents " + strconv.Itoa(cid) + " 0 R >>")
		kids = append(kids, strconv.Itoa(pid)+" 0 R")
	}
	objs[1] = "<< /Type /Pages /Kids [" + strings.Join(kids, " ") + "] /Count " + strconv.Itoa(len(pages)) + " >>"
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return out.Bytes(), nil
}

// imageObj adds the picture as an image object (RGB, its alpha — a soft mask), compressed; its number.
func imageObj(img image.Image, add func(string) int) (int, error) {
	b := img.Bounds()
	rgb := make([]byte, 0, b.Dx()*b.Dy()*3)
	alpha := make([]byte, 0, b.Dx()*b.Dy())
	opaque := true
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			rgb = append(rgb, c.R, c.G, c.B)
			alpha = append(alpha, c.A)
			opaque = opaque && c.A == 255
		}
	}
	flate := func(data []byte) (string, error) {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		if _, err := w.Write(data); err != nil {
			return "", err
		}
		if err := w.Close(); err != nil {
			return "", err
		}
		return z.String(), nil
	}
	head := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent 8 /Filter /FlateDecode", b.Dx(), b.Dy())
	mask := ""
	if !opaque {
		a, err := flate(alpha)
		if err != nil {
			return 0, err
		}
		id := add("<< " + head + " /ColorSpace /DeviceGray /Length " + strconv.Itoa(len(a)) + " >>\nstream\n" + a + "\nendstream")
		mask = " /SMask " + strconv.Itoa(id) + " 0 R"
	}
	c, err := flate(rgb)
	if err != nil {
		return 0, err
	}
	return add("<< " + head + " /ColorSpace /DeviceRGB" + mask + " /Length " + strconv.Itoa(len(c)) + " >>\nstream\n" + c + "\nendstream"), nil
}

// TextPNG is a line of text as a transparent PNG picture height pixels high, dark blue as a pen's (the name and the
// date blocks of «Подпись»), in the Go font, which has the Cyrillic letters.
func TextPNG(text string, height int) ([]byte, error) {
	ft, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(ft, &opentype.FaceOptions{Size: float64(height) * 0.7, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, err
	}
	defer face.Close()
	width := max(font.MeasureString(face, text).Ceil(), 1) + height/4
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	d := font.Drawer{Dst: img, Src: image.NewUniform(color.NRGBA{0x1a, 0x2b, 0x6d, 0xff}), Face: face,
		Dot: fixed.P(height/8, height*3/4)}
	d.DrawString(text)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
