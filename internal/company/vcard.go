package company

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"image"
	"image/color"
	_ "image/gif" // the photos of the cards: GIF, JPEG and PNG are read
	"image/jpeg"
	_ "image/png"
	"io"
	"strings"
)

// ---------- the employee as a vCard: «Экспорт в Outlook» and the address book over CardDAV ----------

// CardOptions is what a card takes besides the profile.
type CardOptions struct {
	Org        string // the company's name (ORG: the company; the department)
	InnerLabel string // the label of the inner phone (iPhone and Mac show it beside the number)
	Photo      []byte // JPEG of the photo, already made small (CardPhoto); nil — none
}

// VCard is the employee's card in vCard 3.0, which Outlook, iPhone and Mac, Android and Thunderbird all read.
func VCard(p Profile, o CardOptions) string {
	var b strings.Builder
	line := func(s string) {
		b.WriteString(foldLine(s))
		b.WriteString("\r\n")
	}
	line("BEGIN:VCARD")
	line("VERSION:3.0")
	line("PRODID:-//go24//Employees//EN")
	line("UID:" + CardUID(p.ID))
	first, last := p.EditFirst(), p.EditLast()
	line("N:" + vcEsc(last) + ";" + vcEsc(first) + ";" + vcEsc(p.SecondName) + ";;")
	line("FN:" + vcEsc(p.FullName()))
	org := vcEsc(o.Org)
	if len(p.Departments) > 0 {
		org += ";" + vcEsc(p.Departments[0].Name)
	}
	if org != "" {
		line("ORG:" + org)
	}
	if p.Position != "" {
		line("TITLE:" + vcEsc(p.Position))
	}
	if p.Email != "" {
		line("EMAIL;TYPE=INTERNET,WORK:" + vcEsc(p.Email))
	}
	if p.WorkPhone != "" {
		line("TEL;TYPE=WORK,VOICE:" + vcEsc(p.WorkPhone))
	}
	if p.MobilePhone != "" {
		line("TEL;TYPE=CELL,VOICE:" + vcEsc(p.MobilePhone))
	}
	if p.InnerPhone != "" {
		line("item1.TEL;TYPE=WORK:" + vcEsc(p.InnerPhone))
		if o.InnerLabel != "" {
			line("item1.X-ABLabel:" + vcEsc(o.InnerLabel))
		}
	}
	if p.City != "" {
		line("ADR;TYPE=WORK:;;;" + vcEsc(p.City) + ";;;")
	}
	if p.Birthday != nil {
		if p.BirthdayYear {
			line("BDAY;VALUE=DATE:" + p.Birthday.Format("2006-01-02"))
		} else { // the year hidden: Apple's way (1604 — «no year»), others read the day and month
			line("BDAY;X-APPLE-OMIT-YEAR=1604:1604-" + p.Birthday.Format("01-02"))
		}
	}
	if p.Website != "" {
		line("URL:" + vcEsc(p.Website))
	}
	if t := strings.TrimPrefix(strings.TrimSpace(p.Telegram), "@"); t != "" && !strings.Contains(t, "/") {
		line("X-SOCIALPROFILE;TYPE=telegram:https://t.me/" + vcEsc(t))
	}
	if len(o.Photo) > 0 {
		line("PHOTO;ENCODING=b;TYPE=JPEG:" + base64.StdEncoding.EncodeToString(o.Photo))
	}
	line("END:VCARD")
	return b.String()
}

// CardUID is the UID of an employee's card: the same in every file and every address book.
func CardUID(userID string) string { return "go24-user-" + userID }

// CardETag is the version of a card: it changes with any of its fields and with a new photo (its key), so the
// photo itself is not read to tell whether the card changed.
func CardETag(p Profile, o CardOptions) string {
	o.Photo = nil
	h := sha1.Sum([]byte(VCard(p, o) + p.PhotoKey))
	return `"` + hex.EncodeToString(h[:10]) + `"`
}

// CardsCTag is the version of an address book with these cards' versions.
func CardsCTag(etags []string) string {
	h := sha1.New()
	for _, e := range etags {
		h.Write([]byte(e))
	}
	return hex.EncodeToString(h.Sum(nil)[:10])
}

// cardPhotoSide is the longest side of a card's photo: enough for a contact, small for a phone to sync.
const cardPhotoSide = 256

// CardPhoto makes a photo (JPEG, PNG or GIF) a small JPEG for a card; ok is false for what cannot be read (WebP).
func CardPhoto(r io.Reader) ([]byte, bool) {
	img, _, err := image.Decode(io.LimitReader(r, 10<<20))
	if err != nil {
		return nil, false
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, shrink(img, cardPhotoSide), &jpeg.Options{Quality: 85}); err != nil {
		return nil, false
	}
	return out.Bytes(), true
}

// shrink scales the picture down (each pixel — the mean of the pixels it covers) so its longest side is at most side.
func shrink(img image.Image, side int) image.Image {
	bnd := img.Bounds()
	w, h := bnd.Dx(), bnd.Dy()
	if w <= side && h <= side {
		return img
	}
	nw, nh := side, h*side/w
	if h > w {
		nw, nh = w*side/h, side
	}
	nw, nh = max(nw, 1), max(nh, 1)
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := bnd.Min.Y+y*h/nh, bnd.Min.Y+(y+1)*h/nh
		for x := 0; x < nw; x++ {
			x0, x1 := bnd.Min.X+x*w/nw, bnd.Min.X+(x+1)*w/nw
			var r, g, b, n uint64
			for sy := y0; sy < max(y1, y0+1); sy++ {
				for sx := x0; sx < max(x1, x0+1); sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					cr, cg, cb = cr+0xffff-ca, cg+0xffff-ca, cb+0xffff-ca // on white, as on a card (RGBA is premultiplied)
					r, g, b, n = r+uint64(cr), g+uint64(cg), b+uint64(cb), n+1
				}
			}
			out.Set(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(b / n), A: 0xffff})
		}
	}
	return out
}

// vcEsc escapes a text value of a vCard.
func vcEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// foldLine folds a content line at 75 octets, never inside a UTF-8 character (RFC 6350 3.2).
func foldLine(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		l := len(string(r))
		if n+l > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += l
	}
	return b.String()
}
