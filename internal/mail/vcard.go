package mail

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"mime/quotedprintable"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// ---------- personal contacts as vCards: what phones, Outlook and Google keep (RFC 6350, vCard 3.0 and 2.1) ----------

// vcLine is a content line of a card: its name (the group, «item1.», dropped), parameters (upper-case names; a bare
// one of vCard 2.1, «CELL», is a TYPE) and value (decoded from quoted-printable and its charset, still escaped).
type vcLine struct {
	name   string
	params map[string][]string
	value  string
	head   string // the line before its value: the group, name and parameters
	raw    string // the line as it came, unfolded
}

// hasType tells whether the line has the type t (TYPE=CELL, TYPE=cell,pref, PREF=1).
func (l vcLine) hasType(t string) bool {
	for _, v := range l.params["TYPE"] {
		for _, x := range strings.Split(v, ",") {
			if strings.EqualFold(strings.Trim(x, `"`), t) {
				return true
			}
		}
	}
	return t == "PREF" && len(l.params["PREF"]) > 0
}

// unfoldCard is the card's logical lines: folded lines joined (RFC 6350 3.2), and the soft line breaks of
// quoted-printable values of vCard 2.1 too.
func unfoldCard(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	var out []string
	for _, l := range strings.Split(text, "\n") {
		switch {
		case len(out) > 0 && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")):
			out[len(out)-1] += l[1:]
		case len(out) > 0 && strings.HasSuffix(out[len(out)-1], "=") && isQP(out[len(out)-1]):
			out[len(out)-1] = strings.TrimSuffix(out[len(out)-1], "=") + l
		case strings.TrimSpace(l) != "":
			out = append(out, l)
		}
	}
	return out
}

func isQP(line string) bool {
	head, _, ok := strings.Cut(line, ":")
	return ok && strings.Contains(strings.ToUpper(head), "QUOTED-PRINTABLE")
}

// parseVCLine reads a logical line; ok is false for one without a value.
func parseVCLine(line string) (vcLine, bool) {
	l := vcLine{params: map[string][]string{}, raw: line}
	quoted, colon := false, -1
	for i, r := range line {
		if r == '"' {
			quoted = !quoted
		} else if r == ':' && !quoted {
			colon = i
			break
		}
	}
	if colon < 0 {
		return l, false
	}
	parts := strings.Split(line[:colon], ";")
	l.name = strings.ToUpper(parts[0])
	if i := strings.LastIndex(l.name, "."); i >= 0 {
		l.name = l.name[i+1:]
	}
	for _, p := range parts[1:] {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			k, v = "TYPE", p
		}
		k = strings.ToUpper(strings.TrimSpace(k))
		l.params[k] = append(l.params[k], v)
	}
	l.head, l.value = line[:colon], line[colon+1:]
	if enc := l.params["ENCODING"]; len(enc) > 0 && strings.EqualFold(enc[0], "QUOTED-PRINTABLE") {
		if b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(l.value))); err == nil {
			l.value = string(b)
		}
	}
	if cs := l.params["CHARSET"]; len(cs) > 0 && !utf8.ValidString(l.value) {
		if strings.EqualFold(cs[0], "windows-1251") || strings.EqualFold(cs[0], "cp1251") {
			if b, err := charmap.Windows1251.NewDecoder().String(l.value); err == nil {
				l.value = b
			}
		}
	}
	return l, true
}

// vcUnescape is a text value without its escapes.
func vcUnescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 'n', 'N':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// vcSplit splits a structured value (N, ORG) at its unescaped semicolons, each part unescaped.
func vcSplit(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case ';':
			out = append(out, vcUnescape(s[start:i]))
			start = i + 1
		}
	}
	return append(out, vcUnescape(s[start:]))
}

// vcEscape escapes a text value of a card.
func vcEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`, "\r", "").Replace(s)
}

// vcFold folds a content line at 75 octets, never inside a UTF-8 character.
func vcFold(s string) string {
	if len(s) <= 75 {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		w := utf8.RuneLen(r)
		if n+w > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += w
	}
	return b.String()
}

// SplitCards cuts a file of cards (.vcf of a phone, Outlook or Google) into the cards, each from BEGIN:VCARD to its
// END:VCARD.
func SplitCards(text string) []string {
	var out []string
	var cur []string
	depth := 0
	for _, l := range unfoldCard(strings.TrimPrefix(text, "\ufeff")) {
		u := strings.ToUpper(strings.TrimSpace(l))
		if u == "BEGIN:VCARD" {
			depth++
		}
		if depth > 0 {
			cur = append(cur, l)
		}
		if u == "END:VCARD" && depth > 0 {
			if depth--; depth == 0 {
				out = append(out, joinCard(cur))
				cur = nil
			}
		}
	}
	return out
}

func joinCard(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(vcFold(l))
		b.WriteString("\r\n")
	}
	return b.String()
}

// ParseCard reads the contact of a card: the name (FN, else N), the address and phone (the preferred one, a mobile
// before others), the company (ORG), the UID and the version.
func ParseCard(card string) (c Contact, uid, version string) {
	var emails, tels []vcLine
	var n string
	for _, raw := range unfoldCard(card) {
		l, ok := parseVCLine(raw)
		if !ok {
			continue
		}
		switch l.name {
		case "FN":
			c.Name = strings.TrimSpace(vcUnescape(l.value))
		case "N":
			p := vcSplit(l.value)
			for len(p) < 5 {
				p = append(p, "")
			}
			n = strings.Join(strings.Fields(strings.Join([]string{p[3], p[1], p[2], p[0], p[4]}, " ")), " ")
		case "ORG":
			if c.Company == "" {
				c.Company = strings.TrimSpace(vcSplit(l.value)[0])
			}
		case "EMAIL":
			emails = append(emails, l)
		case "TEL":
			tels = append(tels, l)
		case "UID":
			uid = strings.TrimSpace(vcUnescape(l.value))
		case "VERSION":
			version = strings.TrimSpace(l.value)
		}
	}
	if c.Name == "" {
		c.Name = n
	}
	if l, ok := pickLine(emails, "PREF", "INTERNET"); ok {
		c.Email = strings.TrimSpace(vcUnescape(strings.TrimPrefix(l.value, "mailto:")))
	}
	if l, ok := pickLine(tels, "CELL", "PREF"); ok {
		c.Phone = strings.TrimSpace(vcUnescape(strings.TrimPrefix(l.value, "tel:")))
	}
	return c, uid, version
}

// pickLine is the first line of the first type of types, else the first line.
func pickLine(list []vcLine, types ...string) (vcLine, bool) {
	for _, t := range types {
		for _, l := range list {
			if l.hasType(t) {
				return l, true
			}
		}
	}
	if len(list) == 0 {
		return vcLine{}, false
	}
	return list[0], true
}

// ContactUID is the UID of a card made in go24.
func ContactUID(id string) string { return "go24-contact-" + id }

// nameParts splits a name into the given name and the family name (the last word), as N wants them.
func nameParts(name string) (given, family string) {
	f := strings.Fields(name)
	if len(f) < 2 {
		return name, ""
	}
	return strings.Join(f[:len(f)-1], " "), f[len(f)-1]
}

// MakeCard is the card of a contact made in go24 (vCard 3.0).
func MakeCard(c Contact) string {
	lines := []string{"BEGIN:VCARD", "VERSION:3.0", "PRODID:-//go24//Mail contacts//EN", "UID:" + vcEscape(c.UID)}
	lines = append(lines, nameLines(c)...)
	if c.Company != "" {
		lines = append(lines, "ORG:"+vcEscape(c.Company))
	}
	if c.Email != "" {
		lines = append(lines, "EMAIL;TYPE=INTERNET:"+vcEscape(c.Email))
	}
	if c.Phone != "" {
		lines = append(lines, "TEL;TYPE=CELL:"+vcEscape(c.Phone))
	}
	return joinCard(append(lines, "END:VCARD"))
}

// nameLines are N and FN of the contact; FN is never empty, a card wants it.
func nameLines(c Contact) []string {
	fn := c.Name
	if fn == "" {
		fn = c.Email
	}
	if fn == "" {
		fn = c.Phone
	}
	given, family := nameParts(c.Name)
	return []string{"N:" + vcEscape(family) + ";" + vcEscape(given) + ";;;", "FN:" + vcEscape(fn)}
}

// PatchCard puts the contact's fields changed in go24 into the card a program sent, so its other fields (more
// addresses and phones, the photo, birthdays) stay: the name lines, the company, the address and the phone the
// contact was read from are replaced (a new one is added, an emptied one dropped).
func PatchCard(card string, c Contact) string {
	old, _, _ := ParseCard(card)
	lines := unfoldCard(card)
	var out []string
	did := map[string]bool{}
	for _, raw := range lines {
		l, ok := parseVCLine(raw)
		if !ok {
			out = append(out, raw)
			continue
		}
		switch {
		case l.name == "END" && strings.EqualFold(strings.TrimSpace(l.value), "VCARD"):
			if !did["name"] && c.Name != old.Name {
				out = append(out, nameLines(c)...)
			}
			if !did["org"] && c.Company != "" {
				out = append(out, "ORG:"+vcEscape(c.Company))
			}
			if !did["email"] && c.Email != "" {
				out = append(out, "EMAIL;TYPE=INTERNET:"+vcEscape(c.Email))
			}
			if !did["tel"] && c.Phone != "" {
				out = append(out, "TEL;TYPE=CELL:"+vcEscape(c.Phone))
			}
			out = append(out, raw)
			continue
		case (l.name == "FN" || l.name == "N") && c.Name != old.Name:
			if !did["name"] {
				out = append(out, nameLines(c)...)
				did["name"] = true
			}
			continue
		case l.name == "ORG" && !did["org"]:
			did["org"] = true
			if c.Company != old.Company {
				if c.Company != "" {
					p := vcSplit(l.value)
					p[0] = c.Company
					for i := range p {
						p[i] = vcEscape(p[i])
					}
					out = append(out, "ORG:"+strings.Join(p, ";"))
				}
				continue
			}
		case l.name == "EMAIL" && !did["email"] && old.Email != "" && strings.EqualFold(strings.TrimSpace(vcUnescape(strings.TrimPrefix(l.value, "mailto:"))), old.Email):
			did["email"] = true
			if c.Email != old.Email {
				if c.Email != "" {
					out = append(out, l.head+":"+vcEscape(c.Email))
				}
				continue
			}
		case l.name == "TEL" && !did["tel"] && old.Phone != "" && strings.TrimSpace(vcUnescape(strings.TrimPrefix(l.value, "tel:"))) == old.Phone:
			did["tel"] = true
			if c.Phone != old.Phone {
				if c.Phone != "" {
					out = append(out, l.head+":"+vcEscape(c.Phone))
				}
				continue
			}
		}
		out = append(out, raw)
	}
	return joinCard(out)
}

// CardETag is the version of a card's text.
func CardETag(card string) string {
	h := sha1.Sum([]byte(card))
	return `"` + hex.EncodeToString(h[:10]) + `"`
}
