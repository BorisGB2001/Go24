package matrixsync

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"go24/internal/i18n"
)

// Attachments and menus of chat bots' messages, as ATTACH and MENU: under the text — blocks of a card
// (a text, a link with its picture, pictures, files, a line, a grid of names and values, a person) with a colored
// stripe; in the menu of the message — the bot's own items, a command of the bot or an action of the messenger as
// its buttons are.

// Attach is the card under a bot's message.
type Attach struct {
	Color  string        `json:"color,omitempty"`
	Blocks []AttachBlock `json:"blocks"`
	Only   bool          `json:"only,omitempty"` // the message is the card alone: its text is not shown
}

func (a *Attach) only() bool { return a != nil && a.Only }

// AttachBlock is one block of a card: Kind message | link | image | file | delimiter | grid | user.
type AttachBlock struct {
	Kind    string       `json:"kind"`
	Text    string       `json:"text,omitempty"` // message: the text (BB codes); link: its description
	Name    string       `json:"name,omitempty"`
	Link    string       `json:"link,omitempty"`
	Preview string       `json:"preview,omitempty"` // link: its picture; user: the avatar
	Size    int          `json:"size,omitempty"`    // delimiter: its width
	Color   string       `json:"color,omitempty"`
	Items   []AttachItem `json:"items,omitempty"` // image, file, grid
}

// AttachItem is a picture, a file or a cell of a grid (Display BLOCK, LINE or ROW).
type AttachItem struct {
	Name    string `json:"name,omitempty"`
	Value   string `json:"value,omitempty"`
	Link    string `json:"link,omitempty"`
	Preview string `json:"preview,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Display string `json:"display,omitempty"`
	Color   string `json:"color,omitempty"`
}

var errAttach = i18n.E("err.imbot_attach")

// ParseAttach reads ATTACH as an application gives it: an array of blocks or {"ID", "COLOR", "BLOCKS"}, keys in any
// case, each block {"MESSAGE": …}, {"LINK": {…}}, {"IMAGE": […]}, {"FILE": […]}, {"DELIMITER": {…}}, {"GRID": […]}
// or {"USER": {…}}; blocks of other kinds (HTML) are left out. nil — none given; "N" or an empty one — an empty card
// (it takes the card away).
func ParseAttach(raw json.RawMessage) (*Attach, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, errAttach
	}
	a := &Attach{Blocks: []AttachBlock{}}
	var blocks []any
	switch t := v.(type) {
	case string, bool:
		return a, nil
	case []any:
		blocks = t
	case map[string]any:
		m := atLower(t)
		a.Color = atColor(m["color"])
		blocks, _ = m["blocks"].([]any)
	default:
		return nil, errAttach
	}
	if len(blocks) > 50 {
		return nil, errAttach
	}
	for _, b := range blocks {
		m, ok := b.(map[string]any)
		if !ok {
			return nil, errAttach
		}
		for k, v := range atLower(m) {
			blk, err := attachBlock(k, v)
			if err != nil {
				return nil, err
			}
			if blk.Kind != "" {
				a.Blocks = append(a.Blocks, blk)
			}
		}
	}
	return a, nil
}

func attachBlock(kind string, v any) (AttachBlock, error) {
	b := AttachBlock{Kind: kind}
	switch kind {
	case "message":
		if b.Text = strings.TrimSpace(atText(v)); b.Text == "" || len([]rune(b.Text)) > 5000 {
			return b, errAttach
		}
		b.Text = strings.NewReplacer("[BR]", "\n", "[br]", "\n", "#BR#", "\n").Replace(b.Text)
	case "link", "user":
		m := atLower(atObject(v))
		b.Name, b.Text, b.Color = atShort(m["name"]), atShort(m["desc"]), atColor(m["color"])
		var ok bool
		if b.Link, ok = atURL(m["link"]); !ok {
			return b, errAttach
		}
		pic := m["preview"]
		if kind == "user" {
			pic = m["avatar"]
		}
		if b.Preview, ok = atURL(pic); !ok {
			return b, errAttach
		}
		if b.Name == "" && b.Link == "" {
			return b, errAttach
		}
	case "delimiter":
		m := atLower(atObject(v))
		b.Size, b.Color = min(max(int(atNum(m["size"])), 0), 2000), atColor(m["color"])
	case "image", "file", "grid":
		for _, it := range atList(v) {
			m := atLower(atObject(it))
			i := AttachItem{Name: atShort(m["name"]), Value: strings.TrimSpace(atText(m["value"])), Color: atColor(m["color"]),
				Size: int64(atNum(m["size"])), Width: int(atNum(m["width"])), Height: int(atNum(m["height"]))}
			var ok bool
			if i.Link, ok = atURL(m["link"]); !ok {
				return b, errAttach
			}
			if i.Preview, ok = atURL(m["preview"]); !ok {
				return b, errAttach
			}
			switch kind {
			case "image":
				if i.Link == "" {
					return b, errAttach
				}
				if i.Preview == "" {
					i.Preview = i.Link
				}
			case "file":
				if i.Link == "" {
					return b, errAttach
				}
				if i.Name == "" {
					i.Name = i.Link
				}
			case "grid":
				if i.Display = strings.ToUpper(atText(m["display"])); i.Display == "COLUMN" {
					i.Display = "ROW"
				}
				if i.Display != "LINE" && i.Display != "ROW" {
					i.Display = "BLOCK"
				}
				if len([]rune(i.Value)) > 2000 {
					return b, errAttach
				}
			}
			b.Items = append(b.Items, i)
		}
		if len(b.Items) == 0 || len(b.Items) > 50 {
			return b, errAttach
		}
	default:
		return AttachBlock{}, nil
	}
	return b, nil
}

// atLower is the object with its keys in lower case.
func atLower(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

func atObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// atList is an array, or one object as an array of it.
func atList(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case map[string]any:
		return []any{t}
	}
	return nil
}

func atText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprint(t)
	}
	return ""
}

func atShort(v any) string {
	s := strings.TrimSpace(atText(v))
	if r := []rune(s); len(r) > 500 {
		s = string(r[:500])
	}
	return s
}

func atNum(v any) float64 {
	switch t := v.(type) {
	case float64:
		return max(t, 0)
	case string:
		var f float64
		_, _ = fmt.Sscan(t, &f)
		return max(f, 0)
	}
	return 0
}

func atColor(v any) string {
	if s := strings.TrimSpace(atText(v)); colorRe.MatchString(s) {
		return s
	}
	return ""
}

// atURL is an http(s) address; "" is fine, anything else is not.
func atURL(v any) (string, bool) {
	s := strings.TrimSpace(atText(v))
	if s == "" {
		return "", true
	}
	u, err := url.Parse(s)
	return s, err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// attachOf is the card a message's content carries.
func attachOf(content map[string]any) *Attach {
	raw, ok := content["go24.attach"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var a Attach
	if json.Unmarshal(b, &a) != nil || len(a.Blocks) == 0 {
		return nil
	}
	return &a
}

// menuOf is the bot's items of the menu of a message.
func menuOf(content map[string]any) []Button {
	raw, ok := content["go24.menu"]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var m []Button
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// NormMenu checks the items of a message's menu the application gives: as buttons, without colors and lines.
func NormMenu(m []Button) ([]Button, error) {
	if len(m) > 30 {
		return nil, i18n.E("err.imbot_keyboard")
	}
	out, err := NormKeyboard(m)
	if err != nil {
		return nil, err
	}
	items := out[:0]
	for _, b := range out {
		if b.Type == "NEWLINE" {
			continue
		}
		b.BgColor, b.TextColor, b.Display, b.Block = "", "", "", false
		items = append(items, b)
	}
	return items, nil
}

// attachText is the card as plain text: the body of a message that is the card alone, for other Matrix clients and
// the previews of the chat list.
func attachText(a *Attach) string {
	var parts []string
	for _, b := range a.Blocks {
		switch b.Kind {
		case "message":
			parts = append(parts, b.Text)
		case "link", "user":
			parts = append(parts, strings.TrimSpace(b.Name+" "+b.Link))
		case "image", "file", "grid":
			for _, i := range b.Items {
				parts = append(parts, strings.TrimSpace(strings.TrimSpace(i.Name+" "+i.Value)+" "+i.Link))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// BotParts are what a bot's message carries besides its text; a nil one is kept as it is when the message changes
// (an empty one takes it away).
type BotParts struct {
	Keyboard []Button
	Menu     []Button
	Attach   *Attach
}

// put writes the parts into a message's content, taking those of old where they are not given.
func (p BotParts) put(content, old map[string]any) {
	if p.Keyboard == nil {
		p.Keyboard = keyboardOf(old)
	}
	if p.Menu == nil {
		p.Menu = menuOf(old)
	}
	if p.Attach == nil {
		p.Attach = attachOf(old)
	}
	if len(p.Keyboard) > 0 {
		content["go24.keyboard"] = p.Keyboard
	}
	if len(p.Menu) > 0 {
		content["go24.menu"] = p.Menu
	}
	if p.Attach != nil && len(p.Attach.Blocks) > 0 {
		content["go24.attach"] = p.Attach
	}
}
