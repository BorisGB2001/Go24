package company

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
)

// TestVCard: the employee's card in vCard 3.0 — the name in parts, the company and department, the position, the
// e-mail, the phones (the inner one with its label), the city, the birthday (without the year when it is hidden),
// escaped values, lines folded at 75 octets; the version changes with the fields and a new photo, not the photo's
// bytes; a big photo becomes a small JPEG, a picture that is not one is not taken.
func TestVCard(t *testing.T) {
	bday := time.Date(1990, 5, 1, 0, 0, 0, 0, time.UTC)
	p := Profile{ID: "u1", LastName: "Иванова", FirstName: "Анна", SecondName: "Петровна", Position: "Бухгалтер; главный",
		Email: "anna@go24.test", WorkPhone: "+7 495 000-00-01", MobilePhone: "+7 900 000-00-02", InnerPhone: "101", City: "Москва",
		Birthday: &bday, BirthdayYear: true, Telegram: "@anna", Departments: []DeptRef{{Name: "Бухгалтерия"}}}
	o := CardOptions{Org: "ООО Ромашка", InnerLabel: "внутренний"}
	card := VCard(p, o)
	for _, want := range []string{"BEGIN:VCARD\r\nVERSION:3.0\r\n", "UID:go24-user-u1\r\n", "N:Иванова;Анна;Петровна;;\r\n",
		"FN:Иванова Анна Петровна\r\n", "ORG:ООО Ромашка;Бухгалтерия\r\n", `TITLE:Бухгалтер\; главный` + "\r\n",
		"EMAIL;TYPE=INTERNET,WORK:anna@go24.test\r\n", "TEL;TYPE=WORK,VOICE:+7 495 000-00-01\r\n", "TEL;TYPE=CELL,VOICE:+7 900 000-00-02\r\n",
		"item1.TEL;TYPE=WORK:101\r\nitem1.X-ABLabel:внутренний\r\n", "ADR;TYPE=WORK:;;;Москва;;;\r\n", "BDAY;VALUE=DATE:1990-05-01\r\n",
		"X-SOCIALPROFILE;TYPE=telegram:https://t.me/anna\r\n", "END:VCARD\r\n"} {
		if !strings.Contains(card, want) {
			t.Errorf("no %q in\n%s", want, card)
		}
	}
	if strings.Contains(card, "PHOTO") {
		t.Error("a photo without one")
	}
	p.BirthdayYear = false
	if c := VCard(p, o); !strings.Contains(c, "BDAY;X-APPLE-OMIT-YEAR=1604:1604-05-01\r\n") || strings.Contains(c, "1990") {
		t.Errorf("the hidden year shows: %s", c)
	}
	// no parts of the name yet: the display name is split
	if c := VCard(Profile{ID: "u2", Name: "Пётр Сидоров"}, CardOptions{}); !strings.Contains(c, "N:Сидоров;Пётр;;;\r\nFN:Пётр Сидоров\r\n") || strings.Contains(c, "ORG:") {
		t.Errorf("display name card: %s", c)
	}

	// long lines fold at 75 octets, never inside a letter
	p.Position = strings.Repeat("Ведущий специалист, ", 10)
	o.Photo = []byte(strings.Repeat("x", 300))
	card = VCard(p, o)
	for _, l := range strings.Split(card, "\r\n") {
		if len(l) > 75 {
			t.Errorf("line of %d octets: %q", len(l), l)
		}
	}
	unfolded := strings.ReplaceAll(card, "\r\n ", "")
	if !strings.Contains(unfolded, "TITLE:"+strings.Repeat(`Ведущий специалист\, `, 10)+"\r\n") ||
		!strings.Contains(unfolded, "PHOTO;ENCODING=b;TYPE=JPEG:"+base64.StdEncoding.EncodeToString(o.Photo)+"\r\n") {
		t.Errorf("folded card does not unfold: %s", unfolded)
	}

	// the version: the photo's key, not its bytes
	e1 := CardETag(p, o)
	if e2 := CardETag(p, CardOptions{Org: o.Org, InnerLabel: o.InnerLabel}); e2 != e1 {
		t.Error("the photo's bytes change the version")
	}
	p.PhotoKey = "photos/new.jpg"
	e3 := CardETag(p, o)
	p.Position = "Главный бухгалтер"
	if e3 == e1 || CardETag(p, o) == e3 || !strings.HasPrefix(e1, `"`) {
		t.Errorf("versions %s %s", e1, e3)
	}
	if CardsCTag([]string{e1, e3}) == CardsCTag([]string{e1}) {
		t.Error("the book's version does not change")
	}

	// the photo: made small, a JPEG
	img := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	for y := 0; y < 500; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	small, ok := CardPhoto(&buf)
	if !ok {
		t.Fatal("photo not taken")
	}
	got, err := jpeg.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatal(err)
	}
	if b := got.Bounds(); b.Dx() != 256 || b.Dy() != 128 {
		t.Errorf("photo of %v", b)
	}
	if r, g, _, _ := got.At(100, 60).RGBA(); r>>8 < 180 || g>>8 > 30 {
		t.Errorf("colour lost: %d %d", r>>8, g>>8)
	}
	if _, ok := CardPhoto(strings.NewReader("RIFF....WEBP")); ok {
		t.Error("not a picture taken")
	}
}
