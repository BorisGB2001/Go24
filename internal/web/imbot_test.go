package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/matrixsync"
)

// TestIMBots: a chat bot in the messenger — the private chat with it is marked «Чат-бот», has no calls and no
// «в сети»; its messages and its place among a group chat's people are marked too, without a link to a profile.
func TestIMBots(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	im := pages["ru-RU"]["im"]
	bot := matrixsync.Room{ID: "!b:x", Name: "Помощник", Kind: "dm", PeerID: "b1", PeerBot: true}
	var b strings.Builder
	if err := im.Execute(&b, page{Data: map[string]any{"Rooms": []matrixsync.Room{bot}, "Room": bot.ID, "Current": bot, "Calls": true}}); err != nil {
		t.Fatal(err)
	}
	head := b.String()
	head = head[strings.Index(head, `class="im-head"><b style="flex:1">`):]
	head = head[:strings.Index(head, `class="im-pins"`)]
	if !strings.Contains(head, "Чат-бот") || strings.Contains(head, "data-presence-text") || strings.Contains(head, "/call?") {
		t.Errorf("the head of a bot's chat:\n%s", head)
	}

	b.Reset()
	if err := im.ExecuteTemplate(&b, "immsg", matrixsync.Message{ID: "$1", SenderID: "b1", SenderName: "Помощник", SenderBot: true,
		Body: "Здравствуйте", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "Чат-бот") {
		t.Errorf("a bot's message:\n%s", b.String())
	}

	// its buttons: a link, a command for the bot, an action of the messenger
	b.Reset()
	if err := im.ExecuteTemplate(&b, "immsg", matrixsync.Message{ID: "$2", SenderID: "b1", SenderName: "Помощник", SenderBot: true,
		Body: "Погода", At: time.Now(), Keyboard: [][]matrixsync.Button{
			{{Text: "Ещё", Command: "weather", CommandParams: "Москва", Block: true, BgColor: "#29619b"}},
			{{Text: "Сайт", Link: "https://example.com"}, {Text: "Код", Action: "COPY", ActionValue: "42", Disabled: true}}}}); err != nil {
		t.Fatal(err)
	}
	kb := b.String()
	for _, want := range []string{`data-kbd="command" data-command="weather" data-params="Москва" data-block="1" style="background:#29619b;"`,
		`href="https://example.com" target="_blank"`, `data-kbd="COPY" data-value="42" disabled`} {
		if !strings.Contains(kb, want) {
			t.Errorf("no %s in a bot's message with buttons:\n%s", want, kb)
		}
	}
	if strings.Count(kb, `class="im-kbd-row"`) != 2 {
		t.Errorf("the lines of buttons:\n%s", kb)
	}

	b.Reset()
	g := &matrixsync.Group{Owner: true, CanAdd: true, Members: []matrixsync.Member{{ID: "u1", Name: "Анна", Owner: true, Me: true},
		{ID: "b1", Name: "Помощник", Bot: true, Removable: true}}}
	if err := im.ExecuteTemplate(&b, "imgroup", map[string]any{"G": g, "Room": matrixsync.Room{ID: "!g:x", Kind: "group"}}); err != nil {
		t.Fatal(err)
	}
	group := b.String()
	if strings.Contains(group, `href="/u/b1"`) || !strings.Contains(group, "Чат-бот") || strings.Count(group, "/im/group/owner") != 0 ||
		!strings.Contains(group, "/im/group/remove") {
		t.Errorf("a bot among the people of a group chat:\n%s", group)
	}
}

// TestBotText: [BR] is a new line, the other BB codes stay for msgfmt.
func TestBotText(t *testing.T) {
	if got := botText("Привет[BR][b]мир[/b][br]!"); got != "Привет\n[b]мир[/b]\n!" {
		t.Fatalf("botText = %q", got)
	}
}

// TestBotKeyboard: KEYBOARD of imbot.message.add — buttons, "N" takes them away, nothing given keeps them.
func TestBotKeyboard(t *testing.T) {
	for raw, want := range map[string]int{`[{"TEXT":"Да","COMMAND":"yes"},{"TYPE":"NEWLINE"},{"TEXT":"Нет","COMMAND":"no","BLOCK":"Y"}]`: 3,
		`"N"`: 0, `null`: -1, ``: -1} {
		kb, err := botMsg{Keyboard: json.RawMessage(raw)}.keyboard()
		if err != nil || want < 0 && kb != nil || want >= 0 && (kb == nil || len(kb) != want) {
			t.Errorf("keyboard(%s) = %+v %v", raw, kb, err)
		}
		if want == 3 && (!bool(kb[2].Block) || kb[0].Command != "yes") {
			t.Errorf("keyboard(%s) = %+v", raw, kb)
		}
	}
	if _, err := (botMsg{Keyboard: json.RawMessage(`[{"TEXT":""}]`)}).keyboard(); err == nil {
		t.Error("a button without a text is taken")
	}
}
