package openlines

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestBotPick: an item is picked by its number, by a button's text or by its title; 0 — «Назад».
func TestBotPick(t *testing.T) {
	items := []BotItem{{Title: "Оплата"}, {Title: "Доставка"}}
	for text, want := range map[string]int{"1": 1, " 2 ": 2, "2. Доставка": 2, "1)": 1, "доставка": 2, "0": 0, "0. Назад": 0, "3": -1, "привет": -1, "12 стульев": -1} {
		if got := botPick(items, text); got != want {
			t.Errorf("botPick(%q) = %d, want %d", text, got, want)
		}
	}
	w := Widget{BotOn: true, BotMenu: []BotItem{{Title: "A", Action: "menu", Items: []BotItem{{Title: "B", Action: "text"}}}}}
	if err := cleanBot(&w); err == nil {
		t.Fatal("an item «Ответить текстом» without a text is taken")
	}
	w = Widget{BotOn: true}
	if err := cleanBot(&w); err == nil {
		t.Fatal("an enabled bot without items is taken")
	}
	w = Widget{BotMenu: []BotItem{{Title: " A ", Action: "close", Items: []BotItem{{Title: "x"}}}}}
	if err := cleanBot(&w); err != nil || w.BotOther != "menu" || w.BotMenu[0].Title != "A" || w.BotMenu[0].Items != nil {
		t.Fatalf("cleanBot = %v, %+v", err, w)
	}
}

// TestBot: the line's chat bot answers a new dialogue with its menu, the client goes through submenus and back, gets
// text answers, the operators are not told until the client is passed to them; an operator's answer, an unknown text
// (as the line says) and «Закрыть диалог» end the bot's part.
func TestBot(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, a := newUser("boss"), newUser("a")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	menu := []BotItem{
		{Title: "Оплата", Action: "menu", Text: "Как платите?", Items: []BotItem{
			{Title: "Картой", Action: "text", Text: "Картой на сайте."},
			{Title: "Счёт", Action: "queue"},
		}},
		{Title: "Часы работы", Action: "text", Text: "С 9 до 18."},
		{Title: "Оператор", Action: "queue", Text: "Зову оператора"},
		{Title: "Ничего", Action: "close", Text: "До свидания"},
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Бот", Active: true, Operators: []string{a}, BotOn: true, BotText: "Здравствуйте!",
		BotMenu: menu, WelcomeText: "welcome", CloseText: "bye"})
	must(err)
	w, err := s.Widget(ctx, wid)
	must(err)
	if !w.BotOn || w.BotOther != "menu" || len(w.BotMenu) != 4 || w.BotMenu[0].Items[1].Action != "queue" {
		t.Fatalf("line = %+v", w)
	}
	told := func() int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'openline'`, a).Scan(&n))
		return n
	}
	var last int64
	said := func(tok string) []Message { // what the visitor got since the last look
		t.Helper()
		list, err := s.VisitorMessages(ctx, wid, tok, last)
		must(err)
		out := []Message{}
		for _, m := range list {
			last = m.ID
			if m.AuthorID == "" && m.Kind != "" {
				out = append(out, m)
			}
		}
		return out
	}
	state := func(id string) (node *string, waiting bool, unread int, status string) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT bot_node, waiting_since IS NOT NULL, unread, status FROM ol_sessions WHERE id = $1`, id).Scan(&node, &waiting, &unread, &status))
		return
	}

	// a new dialogue: the bot's greeting and menu instead of the line's welcome; nobody waits, nobody is told
	sid, tok, err := s.Visit(ctx, wid, "", "")
	must(err)
	must(s.VisitorSay(ctx, wid, tok, "Добрый день"))
	got := said(tok)
	if len(got) != 1 || got[0].Kind != "bot" || !slices.Equal(got[0].Buttons, []string{"1. Оплата", "2. Часы работы", "3. Оператор", "4. Ничего"}) ||
		!strings.HasPrefix(got[0].Body, "Здравствуйте!\n\n1. Оплата\n") || got[0].Operator != "Бот" {
		t.Fatalf("the bot's first answer = %+v", got)
	}
	if node, waiting, unread, _ := state(sid); node == nil || *node != "" || waiting || unread != 0 || told() != 0 {
		t.Fatalf("with the bot: node %v, waiting %v, unread %d, told %d", node, waiting, unread, told())
	}
	if x, _, err := s.Open(ctx, boss, sid); err != nil || !x.Bot || x.Waiting {
		t.Fatalf("the dialogue = %+v, %v", x, err)
	}

	// a text answer keeps the same menu; a submenu, its answer, «Назад»
	must(s.VisitorSay(ctx, wid, tok, "2"))
	if got = said(tok); len(got) != 1 || !strings.HasPrefix(got[0].Body, "С 9 до 18.\n\n1. Оплата") || len(got[0].Buttons) != 4 {
		t.Fatalf("the text answer = %+v", got)
	}
	must(s.VisitorSay(ctx, wid, tok, "1. Оплата"))
	if got = said(tok); len(got) != 1 || !slices.Equal(got[0].Buttons, []string{"1. Картой", "2. Счёт", "0. ol.bot_back"}) || !strings.HasPrefix(got[0].Body, "Как платите?") {
		t.Fatalf("the submenu = %+v", got)
	}
	must(s.VisitorSay(ctx, wid, tok, "картой"))
	if got = said(tok); len(got) != 1 || !strings.HasPrefix(got[0].Body, "Картой на сайте.") || len(got[0].Buttons) != 3 {
		t.Fatalf("the submenu's answer = %+v", got)
	}
	if node, _, _, _ := state(sid); *node != "1" {
		t.Fatalf("node = %q", *node)
	}
	must(s.VisitorSay(ctx, wid, tok, "что?")) // not an item: the menu again
	if got = said(tok); len(got) != 1 || !strings.HasPrefix(got[0].Body, "ol.bot_unknown\n\n1. Картой") {
		t.Fatalf("an unknown text = %+v", got)
	}
	must(s.VisitorSay(ctx, wid, tok, "0"))
	if got = said(tok); len(got) != 1 || len(got[0].Buttons) != 4 {
		t.Fatalf("«Назад» = %+v", got)
	}
	if node, waiting, _, _ := state(sid); *node != "" || waiting || told() != 0 {
		t.Fatal("still with the bot: nobody waits and is told")
	}

	// «Оператор»: the item's text, the dialogue goes to the queue and the operators are told
	must(s.VisitorSay(ctx, wid, tok, "3"))
	if got = said(tok); len(got) != 1 || got[0].Body != "Зову оператора" || len(got[0].Buttons) != 0 {
		t.Fatalf("to the operator = %+v", got)
	}
	if node, waiting, unread, _ := state(sid); node != nil || !waiting || unread != 1 || told() != 1 {
		t.Fatalf("passed to the operators: node %v, waiting %v, unread %d, told %d", node, waiting, unread, told())
	}
	var notes []string
	must(pool.QueryRow(ctx, `SELECT array_agg(body ORDER BY id) FROM ol_messages WHERE session_id = $1 AND kind = 'note'`, sid).Scan(&notes))
	if !slices.Contains(notes, "ol.note_bot_pick") || notes[len(notes)-1] != "ol.note_bot_queue" {
		t.Fatalf("notes = %v", notes)
	}
	must(s.VisitorSay(ctx, wid, tok, "4")) // with the operators: no more bot answers
	if got = said(tok); len(got) != 0 {
		t.Fatalf("the bot answered after the operators had it: %+v", got)
	}

	// the visitor back after closing: the bot again; the operator answers — the bot is off
	must(s.SetStatus(ctx, a, sid, false))
	said(tok)
	must(s.VisitorSay(ctx, wid, tok, "снова я"))
	if got = said(tok); len(got) != 1 || got[0].Kind != "bot" {
		t.Fatalf("the bot after reopening = %+v", got)
	}
	must(s.Reply(ctx, a, sid, "Я тут"))
	if node, _, _, _ := state(sid); node != nil {
		t.Fatal("an operator's answer keeps the bot")
	}
	must(s.VisitorSay(ctx, wid, tok, "1"))
	if got = said(tok); len(got) != 0 {
		t.Fatalf("the bot answered after the operator: %+v", got)
	}

	// «Закрыть диалог»: the item's text, the line's closing message, the dialogue is closed
	_, tok2, err := s.Visit(ctx, wid, "", "")
	must(err)
	last = 0
	must(s.VisitorSay(ctx, wid, tok2, "привет"))
	said(tok2)
	sid2, err := s.session(ctx, wid, tok2)
	must(err)
	must(s.VisitorSay(ctx, wid, tok2, "4"))
	if got = said(tok2); len(got) != 2 || got[0].Body != "До свидания" || got[1].Kind != "close" {
		t.Fatalf("«Закрыть диалог» = %+v", got)
	}
	if node, waiting, _, status := state(sid2); node != nil || waiting || status != "closed" {
		t.Fatalf("closed by the bot: node %v, waiting %v, status %s", node, waiting, status)
	}

	// «Если клиент написал не номер пункта» — to the operators
	w.BotOther = "queue"
	_, err = s.SaveWidget(ctx, boss, w)
	must(err)
	_, tok3, err := s.Visit(ctx, wid, "", "")
	must(err)
	last = 0
	must(s.VisitorSay(ctx, wid, tok3, "привет"))
	sid3, err := s.session(ctx, wid, tok3)
	must(err)
	must(s.VisitorSay(ctx, wid, tok3, "у меня вопрос"))
	if node, waiting, _, _ := state(sid3); node != nil || !waiting {
		t.Fatal("an unknown text with «Соединить с оператором» keeps the client with the bot")
	}
	// the bot is off: a new dialogue gets the line's welcome
	w.BotOn = false
	_, err = s.SaveWidget(ctx, boss, w)
	must(err)
	_, tok4, err := s.Visit(ctx, wid, "", "")
	must(err)
	last = 0
	must(s.VisitorSay(ctx, wid, tok4, "привет"))
	if got = said(tok4); len(got) != 1 || got[0].Kind != "welcome" {
		t.Fatalf("without the bot = %+v", got)
	}
}
