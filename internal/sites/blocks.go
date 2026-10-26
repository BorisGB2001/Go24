package sites

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
)

// Block is one block of a page: its type from the library and the values of the type's fields.
type Block struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Hidden bool              `json:"hidden,omitempty"`
	Data   map[string]string `json:"data"`
	// a form block as it is shown: State — preview (cannot be sent) or sent, Err — the locale key of a refusal,
	// Stamp — FormStamp
	State, Err, Stamp string `json:"-"`
}

// Field is a field of a block type. Kind: text, textarea, url, image (a link or an uploaded picture), color,
// pairs (lines «Заголовок | текст» — the items of a menu or of advantages), show (a field of a form: "" — not asked,
// on — asked, req — required), user (an employee: the responsible of a form's leads), booking (the token of a link
// of «Онлайн-запись»: its page is shown in the block).
type Field struct{ Name, Kind string }

// BlockType is a block of the library («Добавить блок»); its title and texts by default are locale keys
// site.block.<type> and site.def.<type>.<field>.
type BlockType struct {
	Type, Icon string
	Fields     []Field
}

// BlockTypes is the library, in the order it is shown.
var BlockTypes = []BlockType{
	{"menu", "menu-2", []Field{{"logo", "text"}, {"links", "pairs"}, {"color", "color"}}},
	{"cover", "photo", []Field{{"title", "text"}, {"text", "textarea"}, {"button", "text"}, {"link", "url"}, {"image", "image"}, {"color", "color"}}},
	{"title", "bold", []Field{{"title", "text"}, {"text", "textarea"}}},
	{"text", "align-left", []Field{{"text", "textarea"}}},
	{"image", "photo", []Field{{"image", "image"}, {"caption", "text"}}},
	{"textimage", "layout-sidebar-right", []Field{{"title", "text"}, {"text", "textarea"}, {"image", "image"}}},
	{"features", "layout-kanban", []Field{{"title", "text"}, {"items", "pairs"}}},
	{"button", "link", []Field{{"button", "text"}, {"link", "url"}}},
	{"contacts", "phone", []Field{{"title", "text"}, {"phone", "text"}, {"email", "text"}, {"address", "textarea"}}},
	{"form", "send", []Field{{"title", "text"}, {"text", "textarea"}, {"ask_name", "show"}, {"ask_phone", "show"},
		{"ask_email", "show"}, {"ask_comment", "show"}, {"button", "text"}, {"thanks", "textarea"}, {"responsible", "user"}, {"color", "color"}}},
	{"booking", "calendar-event", []Field{{"title", "text"}, {"text", "textarea"}, {"booking", "booking"}, {"color", "color"}}},
	{"footer", "layout-bottombar", []Field{{"text", "textarea"}, {"color", "color"}}},
}

// TypeOf finds a block type.
func TypeOf(typ string) (BlockType, bool) {
	for _, t := range BlockTypes {
		if t.Type == typ {
			return t, true
		}
	}
	return BlockType{}, false
}

const maxField = 10000

var (
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	uuidRe  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	tokenRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// SafeURL keeps a link a visitor may follow: http(s), mailto, tel, an address of this server or an anchor; others
// (javascript: and the like) become "".
func SafeURL(u string) string {
	u = strings.TrimSpace(u)
	low := strings.ToLower(u)
	for _, p := range []string{"https://", "http://", "mailto:", "tel:", "#"} {
		if strings.HasPrefix(low, p) {
			return u
		}
	}
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return u
	}
	return ""
}

// clean keeps the fields of the block's type only, each checked by its kind.
func clean(t BlockType, data map[string]string) map[string]string {
	out := map[string]string{}
	for _, f := range t.Fields {
		v := strings.TrimSpace(strings.ReplaceAll(data[f.Name], "\r\n", "\n"))
		if r := []rune(v); len(r) > maxField {
			v = string(r[:maxField])
		}
		switch f.Kind {
		case "url", "image":
			v = SafeURL(v)
		case "color":
			if !colorRe.MatchString(v) {
				v = ""
			}
		case "show":
			if v != "on" && v != "req" {
				v = ""
			}
		case "user":
			if !uuidRe.MatchString(v) {
				v = ""
			}
		case "booking":
			if !tokenRe.MatchString(v) {
				v = ""
			}
		case "text":
			v = strings.ReplaceAll(v, "\n", " ")
		}
		if v != "" {
			out[f.Name] = v
		}
	}
	return out
}

// normalize drops blocks of unknown types, gives every block an id and cleans its fields.
func normalize(list []Block) []Block {
	out := make([]Block, 0, len(list))
	for _, b := range list {
		t, ok := TypeOf(b.Type)
		if !ok {
			continue
		}
		if b.ID == "" {
			b.ID = newID()
		}
		b.Data = clean(t, b.Data)
		out = append(out, b)
	}
	return out
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Get is the value of a field.
func (b Block) Get(field string) string { return b.Data[field] }

// Pair is one line of a pairs field: «Заголовок | текст» (a menu: «Текст | ссылка»).
type Pair struct{ A, B string }

// Pairs splits a pairs field into its lines.
func (b Block) Pairs(field string) []Pair {
	var out []Pair
	for _, line := range strings.Split(b.Data[field], "\n") {
		a, c, _ := strings.Cut(line, "|")
		if a, c = strings.TrimSpace(a), strings.TrimSpace(c); a != "" || c != "" {
			out = append(out, Pair{a, c})
		}
	}
	return out
}

// Link is the second part of a menu line as a safe link.
func (p Pair) Link() string { return SafeURL(p.B) }

// Paragraphs splits a text into paragraphs by empty lines; the lines of a paragraph stay as lines.
func (b Block) Paragraphs(field string) [][]string {
	var out [][]string
	for _, para := range strings.Split(b.Data[field], "\n\n") {
		var lines []string
		for _, l := range strings.Split(para, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			out = append(out, lines)
		}
	}
	return out
}

// ---------- addresses ----------

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidSlug: latin letters, digits and hyphens, up to 63, not starting with a hyphen.
func ValidSlug(s string) bool { return slugRe.MatchString(s) }

var translit = map[rune]string{'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f",
	'х': "h", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "sch", 'ъ': "", 'ы': "y", 'ь': "", 'э': "e", 'ю': "yu", 'я': "ya"}

// Slugify makes an address of a name: transliterated, lower case, other characters become hyphens.
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			dash = false
		default:
			if _, skip := translit[r]; !skip && !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 63 {
		s = strings.TrimRight(s[:63], "-")
	}
	return s
}
