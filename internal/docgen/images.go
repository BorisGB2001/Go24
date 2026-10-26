package docgen

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg" // the sizes of JPEG pictures
	_ "image/png"  // and of PNG ones
	"path"
	"regexp"
	"strings"
)

// Image is a picture a placeholder becomes — the stamp or a facsimile signature of «Моя компания», as the document
// generator has them: PNG or JPEG, as wide as Width (EMU, 914400 to an inch), its height by its proportions.
// Float: it takes no room, lies over the text from where the placeholder was, raised by half its height — a
// signature over the line «_______________», a stamp over the signature.
type Image struct {
	Data  []byte
	Width int64
	Float bool
}

// Cm is a length in EMU.
func Cm(cm float64) int64 { return int64(cm * 360000) }

// picture is a picture to put in a part of the document: its file in word/media and its relationship id.
type picture struct {
	img      Image
	ext, rel string
	cx, cy   int64
}

// pictureOf reads the picture's kind and size; nil — not a PNG nor a JPEG.
func pictureOf(img Image) *picture {
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil || cfg.Width == 0 || cfg.Height == 0 || img.Width <= 0 {
		return nil
	}
	ext := "png"
	if kind == "jpeg" {
		ext = "jpeg"
	}
	return &picture{img: img, ext: ext, cx: img.Width, cy: img.Width * int64(cfg.Height) / int64(cfg.Width)}
}

const wpNS = `xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"`

// drawing is the run's picture in the place of a placeholder that stood in the text of <w:t>: the text is closed
// before it and goes on after it. The namespaces are declared on the elements, the document's root may lack them.
func drawing(p *picture, id int) string {
	if p.img.Float {
		return fmt.Sprintf(`</w:t><w:drawing><wp:anchor %[1]s distT="0" distB="0" distL="0" distR="0" simplePos="0" relativeHeight="%[2]d" behindDoc="1" locked="0" layoutInCell="1" allowOverlap="1">`+
			`<wp:simplePos x="0" y="0"/><wp:positionH relativeFrom="character"><wp:posOffset>0</wp:posOffset></wp:positionH>`+
			`<wp:positionV relativeFrom="line"><wp:posOffset>%[3]d</wp:posOffset></wp:positionV>`+
			`<wp:extent cx="%[4]d" cy="%[5]d"/><wp:effectExtent l="0" t="0" r="0" b="0"/><wp:wrapNone/><wp:docPr id="%[6]d" name="go24 %[6]d"/><wp:cNvGraphicFramePr/>`+
			`%[7]s</wp:anchor></w:drawing><w:t xml:space="preserve">`, wpNS, 251659264+id, -p.cy/2, p.cx, p.cy, id, graphic(p, id))
	}
	return fmt.Sprintf(`</w:t><w:drawing><wp:inline %[1]s distT="0" distB="0" distL="0" distR="0">`+
		`<wp:extent cx="%[2]d" cy="%[3]d"/><wp:docPr id="%[4]d" name="go24 %[4]d"/>%[5]s</wp:inline></w:drawing><w:t xml:space="preserve">`,
		wpNS, p.cx, p.cy, id, graphic(p, id))
}

// graphic is the picture itself, of the drawing.
func graphic(p *picture, id int) string {
	return fmt.Sprintf(`<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">`+
		`<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:nvPicPr><pic:cNvPr id="%[3]d" name="go24-%[3]d.%[5]s"/><pic:cNvPicPr/></pic:nvPicPr>`+
		`<pic:blipFill><a:blip xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" r:embed="%[4]s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`+
		`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%[1]d" cy="%[2]d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`+
		`</pic:pic></a:graphicData></a:graphic>`, p.cx, p.cy, id, p.rel, p.ext)
}

// relsOf is the relationships part of a part of the document: word/_rels/document.xml.rels.
func relsOf(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

const relsEmpty = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
	`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"></Relationships>`

// addRels adds the pictures' relationships to the part's relationships (made when there were none).
func addRels(rels string, pics []*picture) string {
	if rels == "" {
		rels = relsEmpty
	}
	var add strings.Builder
	for _, p := range pics {
		fmt.Fprintf(&add, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/%s.%s"/>`,
			p.rel, p.rel, p.ext)
	}
	return strings.Replace(rels, "</Relationships>", add.String()+"</Relationships>", 1)
}

var defaultExt = regexp.MustCompile(`(?i)<Default[^>]*Extension="([a-z]+)"`)

// addTypes makes the content types know the pictures' kinds.
func addTypes(types string, exts map[string]bool) string {
	have := map[string]bool{}
	for _, m := range defaultExt.FindAllStringSubmatch(types, -1) {
		have[strings.ToLower(m[1])] = true
	}
	var add strings.Builder
	for _, ext := range []string{"png", "jpeg"} {
		if exts[ext] && !have[ext] {
			fmt.Fprintf(&add, `<Default Extension="%s" ContentType="image/%s"/>`, ext, ext)
		}
	}
	return strings.Replace(types, "</Types>", add.String()+"</Types>", 1)
}
