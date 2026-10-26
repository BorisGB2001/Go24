package openlines

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"go24/internal/i18n"
)

// BotItem is an item of the line's chat bot «Интерактивное меню»: the client picks it by its number
// (a button in the widget and in Telegram); Action text answers with Text, menu opens Items (Text — its heading),
// queue passes the client to the operators, close closes the dialogue (with Text before it).
type BotItem struct {
	Title  string    `json:"title"`
	Action string    `json:"action"`
	Text   string    `json:"text,omitempty"`
	Items  []BotItem `json:"items,omitempty"`
}

const (
	botDepth = 4  // levels of submenus
	botItems = 20 // items of a menu
)

// cleanBot checks the bot's menu: titles, actions, texts and submenus.
func cleanBot(w *Widget) error {
	switch w.BotOther {
	case "":
		w.BotOther = "menu"
	case "menu", "queue":
	default:
		return i18n.E("err.ol_bot")
	}
	if w.BotMenu == nil {
		w.BotMenu = []BotItem{}
	}
	if w.BotText = strings.TrimSpace(w.BotText); len([]rune(w.BotText)) > 1000 {
		return i18n.E("err.ol_bot")
	}
	if w.BotOn && len(w.BotMenu) == 0 {
		return i18n.E("err.ol_bot")
	}
	var walk func(list []BotItem, depth int) error
	walk = func(list []BotItem, depth int) error {
		if len(list) > botItems || depth > botDepth {
			return i18n.E("err.ol_bot")
		}
		for i := range list {
			x := &list[i]
			x.Title, x.Text = strings.TrimSpace(x.Title), strings.TrimSpace(x.Text)
			if x.Title == "" || len([]rune(x.Title)) > 100 || len([]rune(x.Text)) > 1000 {
				return i18n.E("err.ol_bot")
			}
			switch x.Action {
			case "text":
				if x.Text == "" {
					return i18n.E("err.ol_bot")
				}
				x.Items = nil
			case "menu":
				if len(x.Items) == 0 {
					return i18n.E("err.ol_bot")
				}
				if err := walk(x.Items, depth+1); err != nil {
					return err
				}
			case "queue", "close":
				x.Items = nil
			default:
				return i18n.E("err.ol_bot")
			}
		}
		return nil
	}
	return walk(w.BotMenu, 1)
}

// botNode is the menu at the path ("" — the top, "2.1" — item 1 of item 2's submenu): its heading and items; ok false —
// the menu has changed and there is no such one.
func (w Widget) botNode(path string) (head string, items []BotItem, ok bool) {
	head, items = w.BotText, w.BotMenu
	if path == "" {
		return head, items, true
	}
	for _, p := range strings.Split(path, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > len(items) || items[n-1].Action != "menu" {
			return "", nil, false
		}
		x := items[n-1]
		head, items = x.Text, x.Items
		if head == "" { // a submenu without a heading: its item's title
			head = x.Title
		}
	}
	return head, items, true
}

// botButtons are a menu's items as the client sees and picks them: «1. Оплата», …, «0. Назад» in a submenu.
func (s *Store) botButtons(items []BotItem, sub bool) []string {
	out := make([]string, 0, len(items)+1)
	for i, x := range items {
		out = append(out, strconv.Itoa(i+1)+". "+x.Title)
	}
	if sub {
		out = append(out, "0. "+s.tr("ol.bot_back"))
	}
	return out
}

// botSay is the bot's message: the text and the menu's items (as lines of the text and as buttons).
func (s *Store) botSay(ctx context.Context, q dbtx, sessionID, text string, buttons []string) error {
	body := strings.TrimSpace(text)
	if len(buttons) > 0 {
		if body != "" {
			body += "\n\n"
		}
		body += strings.Join(buttons, "\n")
	}
	if body == "" {
		return nil
	}
	if buttons == nil {
		buttons = []string{}
	}
	_, err := q.Exec(ctx, `INSERT INTO ol_messages (session_id, body, kind, buttons) VALUES ($1::uuid, $2, 'bot', $3)`, sessionID, body, buttons)
	return err
}

// botMenu shows the client the menu at the path, text before it ("" — the menu's own heading).
func (s *Store) botMenu(ctx context.Context, q dbtx, w Widget, sessionID, path, text string) error {
	head, items, _ := w.botNode(path)
	if text == "" {
		text = head
	}
	_, err := q.Exec(ctx, `UPDATE ol_sessions SET bot_node = $2 WHERE id::text = $1`, sessionID, path)
	if err != nil {
		return err
	}
	return s.botSay(ctx, q, sessionID, text, s.botButtons(items, path != ""))
}

var reBotPick = regexp.MustCompile(`^\s*(\d{1,2})\s*(?:[.)]|$)`)

// botPick is the item the client's text picks in the menu: its number («2», «2. Оплата» of a button) or its title;
// 0 — «Назад»; -1 — nothing.
func botPick(items []BotItem, text string) int {
	if m := reBotPick.FindStringSubmatch(text); m != nil {
		if n, _ := strconv.Atoi(m[1]); n <= len(items) {
			return n
		}
		return -1
	}
	text = strings.TrimSpace(text)
	for i, x := range items {
		if strings.EqualFold(x.Title, text) {
			return i + 1
		}
	}
	return -1
}

// botStep is the bot's answer to the client's text in the menu at the path; it says what is next: «queue» — the
// client goes to the operators, «close» — the dialogue is closed, "" — the client stays with the bot.
func (s *Store) botStep(ctx context.Context, q dbtx, w Widget, sessionID, path, text string) (string, error) {
	_, items, ok := w.botNode(path)
	if !ok || !w.BotOn || len(w.BotMenu) == 0 { // the menu has changed or the bot is off: from the top, or to the operators
		if w.BotOn && len(w.BotMenu) > 0 {
			return "", s.botMenu(ctx, q, w, sessionID, "", "")
		}
		return "queue", nil
	}
	n := botPick(items, text)
	switch {
	case n == 0 && path != "": // «Назад»
		up := ""
		if i := strings.LastIndex(path, "."); i > 0 {
			up = path[:i]
		}
		return "", s.botMenu(ctx, q, w, sessionID, up, "")
	case n <= 0 && w.BotOther == "queue":
		return "queue", s.botSay(ctx, q, sessionID, s.tr("ol.bot_to_queue"), nil)
	case n <= 0:
		return "", s.botMenu(ctx, q, w, sessionID, path, s.tr("ol.bot_unknown"))
	}
	x := items[n-1]
	if err := s.note(ctx, q, sessionID, "ol.note_bot_pick", x.Title); err != nil {
		return "", err
	}
	switch x.Action {
	case "menu":
		next := strconv.Itoa(n)
		if path != "" {
			next = path + "." + next
		}
		return "", s.botMenu(ctx, q, w, sessionID, next, "")
	case "text": // the answer, and the same menu to go on
		return "", s.botSay(ctx, q, sessionID, x.Text, s.botButtons(items, path != ""))
	case "queue":
		text := x.Text
		if text == "" {
			text = s.tr("ol.bot_to_queue")
		}
		return "queue", s.botSay(ctx, q, sessionID, text, nil)
	}
	return "close", s.botSay(ctx, q, sessionID, x.Text, nil)
}
