package docgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // pictures sent for signing
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ErrNotPDF: the file is not a PDF (or a picture the server reads), or PDFs cannot be joined (no pdfunite).
var ErrNotPDF = errors.New("docgen: not a pdf")

// IsPDF tells a PDF by its first bytes.
func IsPDF(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(data, "\x00\t\r\n "), []byte("%PDF-"))
}

// ImagePDF makes a one-page A4 PDF of a picture (JPEG or PNG): fitted within the margins, centred; a landscape
// picture gets a landscape page. A colour or grey JPEG goes in as it is, the rest as JPEG made of it (transparent parts
// on white; a CMYK JPEG, often stored inverted, too).
func ImagePDF(data []byte) ([]byte, error) {
	return ImagesPDF(data)
}

// ImagesPDF is ImagePDF of several pictures: a page of its own to each, in order (scanned pages sent for signing).
func ImagesPDF(pics ...[]byte) ([]byte, error) {
	if len(pics) == 0 {
		return nil, ErrNotPDF
	}
	num := func(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
	kids := make([]string, len(pics))
	for i := range pics {
		kids[i] = strconv.Itoa(3+3*i) + " 0 R"
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	var offsets []int
	obj := func(body string, stream []byte) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s", len(offsets), body)
		if stream != nil {
			out.WriteString("\nstream\n")
			out.Write(stream)
			out.WriteString("\nendstream")
		}
		out.WriteString("\nendobj\n")
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>", nil)
	obj("<< /Type /Pages /Kids ["+strings.Join(kids, " ")+"] /Count "+strconv.Itoa(len(pics))+" >>", nil)
	for i, data := range pics {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width*cfg.Height > 60_000_000 {
			return nil, ErrNotPDF
		}
		img, format, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, ErrNotPDF
		}
		b := img.Bounds()
		space := "/DeviceRGB"
		switch img.(type) {
		case *image.Gray:
			space = "/DeviceGray"
		case *image.YCbCr:
		default:
			format = ""
		}
		if format != "jpeg" {
			space = "/DeviceRGB"
			rgb := image.NewRGBA(b)
			draw.Draw(rgb, b, image.NewUniform(color.White), image.Point{}, draw.Src)
			draw.Draw(rgb, b, img, b.Min, draw.Over)
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, rgb, &jpeg.Options{Quality: 90}); err != nil {
				return nil, err
			}
			data = buf.Bytes()
		}
		pw, ph, margin := 595.0, 842.0, 28.0 // A4 in points
		w, h := float64(b.Dx()), float64(b.Dy())
		if w > h {
			pw, ph = ph, pw
		}
		scale := min((pw-2*margin)/w, (ph-2*margin)/h)
		w, h = w*scale, h*scale
		content := "q " + num(w) + " 0 0 " + num(h) + " " + num((pw-w)/2) + " " + num((ph-h)/2) + " cm /Im1 Do Q"
		n := 3 + 3*i
		obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 "+num(pw)+" "+num(ph)+"] /Resources << /XObject << /Im1 "+
			strconv.Itoa(n+1)+" 0 R >> >> /Contents "+strconv.Itoa(n+2)+" 0 R >>", nil)
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode /Length %d >>",
			b.Dx(), b.Dy(), space, len(data)), data)
		obj("<< /Length "+strconv.Itoa(len(content))+" >>", []byte(content))
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return out.Bytes(), nil
}

// JoinPDF puts the PDFs one after another into one, by pdfunite (poppler) when the image has it.
func JoinPDF(ctx context.Context, parts ...[]byte) ([]byte, error) {
	bin, err := exec.LookPath("pdfunite")
	if err != nil {
		return nil, ErrNotPDF
	}
	dir, err := os.MkdirTemp("", "go24-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	var args []string
	for i, p := range parts {
		if !IsPDF(p) {
			return nil, ErrNotPDF
		}
		name := filepath.Join(dir, strconv.Itoa(i)+".pdf")
		if err := os.WriteFile(name, p, 0o600); err != nil {
			return nil, err
		}
		args = append(args, name)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	out := filepath.Join(dir, "out.pdf")
	if msg, err := exec.CommandContext(ctx, bin, append(args, out)...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pdfunite: %v: %s", err, bytes.TrimSpace(msg))
	}
	return os.ReadFile(out)
}
