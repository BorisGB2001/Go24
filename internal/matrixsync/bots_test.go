package matrixsync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestChatBots: a bot of an application in the messenger — a private chat with it, a group chat, its events queued
// to the application's handlers, its own messages, unregistering and the application taken off.
func TestChatBots(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	newUser := func(username string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username, guest) VALUES ('sub-' || $1, $1, $1, $2) RETURNING id`,
			username, guest).Scan(&id))
		return id
	}
	ann, bob, eve := newUser("ann", false), newUser("bob", false), newUser("eve", true)
	var app string
	must(pool.QueryRow(ctx, `INSERT INTO apps (name, client_id, client_secret, application_token, installed_at, created_by)
		VALUES ('Helper', 'local.1', 's', 'at', now(), $1) RETURNING id`, ann).Scan(&app))
	const hook = "https://helper.example/bot"
	for _, ev := range []string{BotMessageAdd, BotJoinChat, BotDelete} {
		_, err := pool.Exec(ctx, `INSERT INTO app_events (app_id, event, handler_url, user_id) VALUES ($1, $2, $3, $4)`, app, ev, hook, ann)
		must(err)
	}
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	queued := 0
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co", BotQueued: func() { queued++ }})
	must(s.Reconcile(ctx))

	bot, err := s.RegisterBot(ctx, app, ann, Bot{Code: "helper", Name: "Помощник", WorkPosition: "Бот поддержки",
		MessageHandler: hook, WelcomeHandler: hook, DeleteHandler: hook})
	must(err)
	if u := fake.users[bot.mxid]; u == nil || u.DisplayName != "Помощник" || bot.Type != "B" {
		t.Fatalf("bot %+v, account %+v", bot, u)
	}
	if _, err := s.RegisterBot(ctx, app, ann, Bot{Code: "HELPER", Name: "Другой"}); !errors.Is(err, errBotCode) {
		t.Fatalf("the same code again: %v", err)
	}
	if _, err := s.RegisterBot(ctx, app, ann, Bot{Code: "x", Name: "X", Type: "Z"}); err == nil {
		t.Fatal("an unknown type is taken")
	}
	if b, err := s.AppBot(ctx, app, "helper"); err != nil || b.ID != bot.ID {
		t.Fatalf("by code: %+v %v", b, err)
	}

	// a private chat with the bot: opened by ann, the bot is in it, ann sees it as a chat with a bot
	room, err := s.DirectChat(ctx, ann, bot.ID)
	must(err)
	if !fake.rooms[room][bot.mxid] {
		t.Fatalf("the bot is not in its chat: %v", fake.rooms[room])
	}
	if _, err := s.DirectChat(ctx, eve, bot.ID); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("a guest writes to a bot: %v", err)
	}
	if list, err := s.ChatBots(ctx, eve); err != nil || len(list) != 0 {
		t.Fatalf("a guest's bots: %v %v", list, err)
	}
	deliveries := func(event string) []map[string]any {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT data::text FROM app_event_deliveries WHERE event = $1 ORDER BY id`, event)
		must(err)
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var raw string
			must(rows.Scan(&raw))
			var d map[string]any
			must(json.Unmarshal([]byte(raw), &d))
			out = append(out, d)
		}
		return out
	}
	key := bot.mxid
	join := matrix.Event{EventID: "$join", Type: "m.room.member", Sender: bot.mxid, StateKey: &key, Content: map[string]any{"membership": "join"}}
	join.Unsigned.PrevContent = map[string]any{"membership": "invite"}
	must(s.botEvent(ctx, bot.ID, room, join))
	if d := deliveries(BotJoinChat); len(d) != 1 || d[0]["PARAMS"].(map[string]any)["DIALOG_ID"] != ann ||
		d[0]["PARAMS"].(map[string]any)["CHAT_TYPE"] != "P" || d[0]["USER"].(map[string]any)["ID"] != ann ||
		d[0]["BOT"].(map[string]any)[bot.ID] == nil || queued != 1 {
		t.Fatalf("join chat: %v (queued %d)", d, queued)
	}
	own := join
	own.Unsigned.PrevContent = nil // the bot's own new chat: no welcome
	must(s.botEvent(ctx, bot.ID, room, own))
	if d := deliveries(BotJoinChat); len(d) != 1 {
		t.Fatalf("a chat the bot made is welcomed: %v", d)
	}

	sent := func(room, sender string) matrix.Event {
		t.Helper()
		e := fake.sent[len(fake.sent)-1]
		if e.Room != room || e.Sender != sender {
			t.Fatalf("last sent %+v", e)
		}
		return matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}
	}
	_, err = s.Send(ctx, ann, room, "привет, бот", "")
	must(err)
	must(s.botEvent(ctx, bot.ID, room, sent(room, "@ann:t.lan")))
	d := deliveries(BotMessageAdd)
	if len(d) != 1 {
		t.Fatalf("message add: %v", d)
	}
	p := d[0]["PARAMS"].(map[string]any)
	if p["MESSAGE"] != "привет, бот" || p["DIALOG_ID"] != ann || p["FROM_USER_ID"] != ann || p["TO_USER_ID"] != bot.ID || p["MESSAGE_ID"] == "" {
		t.Fatalf("message add params: %v", p)
	}

	// the bot answers in the dialog of ann (by her ID): the same chat, ann sees it as the bot's
	dialog, err := s.BotDialog(ctx, bot.ID, ann)
	must(err)
	if dialog != room {
		t.Fatalf("the bot's dialog with ann %q, the chat %q", dialog, room)
	}
	answer, err := s.Send(ctx, bot.ID, dialog, "Здравствуйте!", "")
	must(err)
	must(s.botEvent(ctx, bot.ID, room, sent(room, bot.mxid))) // its own message is no event
	if len(deliveries(BotMessageAdd)) != 1 {
		t.Fatal("the bot hears itself")
	}
	if _, err := s.Edit(ctx, bot.ID, room, answer.ID, "Добрый день!"); err != nil {
		t.Fatal(err)
	}
	msgs, _, err := s.Messages(ctx, ann, room, "")
	must(err)
	var fromBot *Message
	for i := range msgs {
		if msgs[i].SenderMXID == bot.mxid {
			fromBot = &msgs[i]
		}
	}
	if fromBot == nil || !fromBot.SenderBot || fromBot.SenderName != "Помощник" || fromBot.SenderID != bot.ID {
		t.Fatalf("ann's messages %+v", msgs)
	}
	rooms, err := s.Rooms(ctx, ann)
	must(err)
	if i := indexRoom(rooms, room); i < 0 || rooms[i].Kind != "dm" || !rooms[i].PeerBot || rooms[i].Name != "Помощник" {
		t.Fatalf("ann's chats %+v", rooms)
	}

	// a group chat: the bot hears only what mentions it
	group, err := s.CreateGroup(ctx, ann, "Команда", []string{bob, bot.ID}, false)
	must(err)
	if !fake.rooms[group][bot.mxid] {
		t.Fatalf("the bot is not in the group: %v", fake.rooms[group])
	}
	_, err = s.Send(ctx, bob, group, "всем привет", "")
	must(err)
	must(s.botEvent(ctx, bot.ID, group, sent(group, "@bob:t.lan")))
	if len(deliveries(BotMessageAdd)) != 1 {
		t.Fatal("the bot hears a message of the group not to it")
	}
	_, err = s.Send(ctx, bob, group, "@Помощник, какая погода?", "")
	must(err)
	ev := sent(group, "@bob:t.lan")
	if !mentions(ev.Content, bot.mxid) {
		t.Fatalf("the bot is not mentioned: %v", ev.Content)
	}
	must(s.botEvent(ctx, bot.ID, group, ev))
	d = deliveries(BotMessageAdd)
	if len(d) != 2 || d[1]["PARAMS"].(map[string]any)["DIALOG_ID"] != group || d[1]["PARAMS"].(map[string]any)["CHAT_TYPE"] != "C" ||
		d[1]["PARAMS"].(map[string]any)["FROM_USER_ID"] != bob {
		t.Fatalf("a mention in the group: %v", d)
	}
	g, err := s.GroupInfo(ctx, ann, group)
	must(err)
	var botMember bool
	for _, m := range g.Members {
		botMember = botMember || m.Bot && m.ID == bot.ID
	}
	if !botMember {
		t.Fatalf("group members %+v", g.Members)
	}

	// commands: «/weather Москва» in the bot's chat goes to the command, not to ONIMBOTMESSAGEADD
	_, err = pool.Exec(ctx, `INSERT INTO app_events (app_id, event, handler_url, user_id) VALUES ($1, $2, $3, $4)`, app, BotCommand, hook, ann)
	must(err)
	cmd, err := s.RegisterCommand(ctx, app, bot.ID, Command{Command: "/weather", Handler: hook,
		Lang: map[string]CommandText{"ru": {Title: "Погода", Params: "город"}}})
	must(err)
	if _, err := s.RegisterCommand(ctx, app, bot.ID, Command{Command: "WEATHER", Handler: hook}); !errors.Is(err, errCommand) {
		t.Fatalf("the same command again: %v", err)
	}
	if _, err := s.RegisterCommand(ctx, app, bot.ID, Command{Command: "two words", Handler: hook}); err == nil {
		t.Fatal("a command with a space is taken")
	}
	_, err = s.RegisterCommand(ctx, app, bot.ID, Command{Command: "secret", Hidden: true, Handler: hook})
	must(err)
	if list, err := s.ChatCommands(ctx, ann, room, "en-US"); err != nil || len(list) != 1 || list[0].Command != "weather" ||
		list[0].Title != "Погода" || list[0].Params != "город" || list[0].Bot != "Помощник" {
		t.Fatalf("the commands on «/»: %+v %v", list, err)
	}
	if list, err := s.ChatCommands(ctx, bob, group, "ru-RU"); err != nil || len(list) != 1 {
		t.Fatalf("the commands of the group: %+v %v", list, err)
	}
	_, err = s.Send(ctx, ann, room, "/weather Москва", "")
	must(err)
	ev = sent(room, "@ann:t.lan")
	must(s.botEvent(ctx, bot.ID, room, ev))
	if len(deliveries(BotMessageAdd)) != 2 {
		t.Fatal("a command goes as a message to the bot too")
	}
	cmdOf := func(d map[string]any) map[string]any { return d["COMMAND"].(map[string]any)[cmd.ID].(map[string]any) }
	d = deliveries(BotCommand)
	if len(d) != 1 || cmdOf(d[0])["COMMAND_PARAMS"] != "Москва" || cmdOf(d[0])["COMMAND_CONTEXT"] != "TEXTAREA" ||
		cmdOf(d[0])["MESSAGE_ID"] != ev.EventID || d[0]["PARAMS"].(map[string]any)["DIALOG_ID"] != ann {
		t.Fatalf("command add: %v", d)
	}
	if _, err := s.Send(ctx, ann, room, "/unknown", ""); err != nil || len(deliveries(BotCommand)) != 1 {
		t.Fatalf("an unknown command: %v", err)
	}

	// the answer with buttons: in the chat of the command
	at, _, err := s.CommandRoom(ctx, app, cmd, ev.EventID)
	if err != nil || at != room {
		t.Fatalf("the room of the answer %q %v", at, err)
	}
	if _, err := NormKeyboard([]Button{{Text: "x", Link: "javascript:alert(1)"}}); err == nil {
		t.Fatal("a script link is taken")
	}
	kb, err := NormKeyboard([]Button{{Text: "Ещё", Command: "/weather", CommandParams: "Москва", Block: true}, {Type: "newline"},
		{Text: "Сайт", Link: "https://example.com", BgColor: "#29619b"}, {Text: "Скопировать", Action: "copy", ActionValue: "42", BgColor: "red;x"}})
	must(err)
	if kb[0].Command != "weather" || kb[1].Type != "NEWLINE" || kb[2].BgColor != "#29619b" || kb[3].BgColor != "" || kb[3].Action != "COPY" {
		t.Fatalf("keyboard %+v", kb)
	}
	menu, err := NormMenu([]Button{{Text: "Прогноз", Command: "weather", CommandParams: "неделя", BgColor: "#fff"}, {Type: "NEWLINE"},
		{Text: "Сайт", Link: "https://example.com"}})
	must(err)
	if len(menu) != 2 || menu[0].BgColor != "" {
		t.Fatalf("menu %+v", menu)
	}
	ans, err := s.BotSend(ctx, bot.ID, room, "Солнечно", "", BotParts{Keyboard: kb, Menu: menu})
	must(err)
	msgs, _, err = s.Messages(ctx, ann, room, "")
	must(err)
	if i := slices.IndexFunc(msgs, func(m Message) bool { return m.ID == ans.ID }); i < 0 || len(msgs[i].Keyboard) != 2 ||
		len(msgs[i].Keyboard[1]) != 2 || msgs[i].Keyboard[0][0].Text != "Ещё" || len(msgs[i].Menu) != 2 {
		t.Fatalf("the buttons ann sees: %+v", msgs)
	}
	must(s.PressButton(ctx, ann, room, ans.ID, "weather", "Москва", false))
	if d := deliveries(BotCommand); len(d) != 2 || cmdOf(d[1])["COMMAND_CONTEXT"] != "KEYBOARD" || cmdOf(d[1])["MESSAGE_ID"] != ans.ID {
		t.Fatalf("a button pressed: %v", d)
	}
	if err := s.PressButton(ctx, ann, room, ans.ID, "weather", "Питер", false); err == nil {
		t.Fatal("a button the message has not is pressed")
	}
	if err := s.PressButton(ctx, ann, room, ans.ID, "weather", "Москва", true); err == nil {
		t.Fatal("a button is pressed as an item of the menu")
	}
	must(s.PressButton(ctx, ann, room, ans.ID, "weather", "неделя", true))
	if d := deliveries(BotCommand); len(d) != 3 || cmdOf(d[2])["COMMAND_CONTEXT"] != "MENU" || cmdOf(d[2])["COMMAND_PARAMS"] != "неделя" {
		t.Fatalf("an item of the menu pressed: %v", d)
	}
	if err := s.PressButton(ctx, ann, room, answer.ID, "weather", "Москва", false); err == nil {
		t.Fatal("a button of a message without buttons is pressed")
	}
	// the buttons taken away, the text stays
	_, err = s.BotEdit(ctx, bot.ID, room, ans.ID, "", BotParts{Keyboard: []Button{}})
	must(err)
	if nc := fake.sent[len(fake.sent)-1].Content["m.new_content"].(map[string]any); nc["body"] != "Солнечно" || nc["go24.keyboard"] != nil ||
		nc["go24.menu"] == nil {
		t.Fatalf("the buttons taken away: %v", nc)
	}

	// a card alone: its text for other clients, not shown; it stays alone while no text is given
	card, err := ParseAttach([]byte(`{"COLOR":"#29619b","BLOCKS":[{"MESSAGE":"Погода на неделю"},{"GRID":[{"NAME":"Пн","VALUE":"+5","DISPLAY":"LINE"}]}]}`))
	must(err)
	cm, err := s.BotSend(ctx, bot.ID, room, "", "", BotParts{Attach: card})
	must(err)
	if cm.Attach == nil || !cm.Attach.Only || fake.sent[len(fake.sent)-1].Content["body"] != "Погода на неделю\nПн +5" {
		t.Fatalf("a card alone: %+v %v", cm.Attach, fake.sent[len(fake.sent)-1].Content)
	}
	_, err = s.BotEdit(ctx, bot.ID, room, cm.ID, "", BotParts{Keyboard: kb})
	must(err)
	msgs, _, err = s.Messages(ctx, ann, room, "")
	must(err)
	if i := slices.IndexFunc(msgs, func(m Message) bool { return m.ID == cm.ID }); i < 0 || msgs[i].Attach == nil || !msgs[i].Attach.Only ||
		len(msgs[i].Attach.Blocks) != 2 || msgs[i].Attach.Color != "#29619b" || len(msgs[i].Keyboard) == 0 {
		t.Fatalf("the card ann sees: %+v", msgs)
	}
	_, err = s.BotEdit(ctx, bot.ID, room, cm.ID, "Прогноз", BotParts{})
	must(err)
	if nc := fake.sent[len(fake.sent)-1].Content["m.new_content"].(map[string]any); nc["body"] != "Прогноз" || attachOf(nc).only() {
		t.Fatalf("a text to the card: %v", nc)
	}
	must(s.DeleteCommand(ctx, app, bot.ID, "weather"))
	if list, err := s.Commands(ctx, app, bot.ID); err != nil || len(list) != 1 || list[0].Command != "secret" {
		t.Fatalf("commands after unregister %+v %v", list, err)
	}

	// unregistered: its handler hears it, the worker closes its account
	must(s.DeleteBot(ctx, app, bot.ID))
	if d := deliveries(BotDelete); len(d) != 1 || d[0]["BOT_ID"] != bot.ID {
		t.Fatalf("bot delete: %v", d)
	}
	if list, err := s.Bots(ctx, app); err != nil || len(list) != 0 {
		t.Fatalf("bots after unregister %v %v", list, err)
	}
	must(s.closeBots(ctx))
	if !fake.users[bot.mxid].Deactivated {
		t.Fatal("the bot's account stays")
	}
	if who, err := s.people(ctx, []string{bot.mxid}); err != nil || who[bot.mxid].Name != "Помощник" || !who[bot.mxid].Bot {
		t.Fatalf("a bot that is gone has lost its name: %+v %v", who, err)
	}
	if _, err := s.DirectChat(ctx, ann, bot.ID); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("a chat with a bot that is gone: %v", err)
	}

	// the application taken off: its bots go with it
	other, err := s.RegisterBot(ctx, app, ann, Bot{Code: "second", Name: "Второй"})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE apps SET installed_at = NULL WHERE id = $1`, app)
	must(err)
	var gone bool
	must(pool.QueryRow(ctx, `SELECT app_id IS NULL FROM im_bots WHERE id = $1`, other.ID).Scan(&gone))
	if !gone {
		t.Fatal("the bot of an application taken off stays")
	}
}
