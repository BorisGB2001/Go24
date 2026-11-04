package matrixsync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestChatBotsV2: bots of imbot.v2 — a «fetch» bot keeps its events (a message, an edit, one taken back, a command, a
// chat opened with it, a reaction on its message, its own removal) in the form of imbot.v2 until Event.get takes them by offset; a «webhook»
// bot's events go to the deliveries of its webhookUrl; a hidden bot is in no people picker; a file of the chat has its
// FILE_ID and the bot reads it (GO24DEV-963).
func TestChatBotsV2(t *testing.T) {
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
	newUser := func(username string) (string, float64) {
		var id string
		var num float64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, first_name, username) VALUES ('sub-' || $1, $1, $1, $1) RETURNING id, num`,
			username).Scan(&id, &num))
		return id, num
	}
	ann, annNum := newUser("ann")
	bob, _ := newUser("bob")
	var app string
	must(pool.QueryRow(ctx, `INSERT INTO apps (name, client_id, client_secret, application_token, installed_at, created_by)
		VALUES ('Helper', 'local.1', 's', 'at', now(), $1) RETURNING id`, ann).Scan(&app))
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))

	bot, err := s.RegisterBot(ctx, app, ann, Bot{Code: "support", Name: "Support", EventMode: "fetch", Reactions: true, Hidden: true})
	must(err)
	if !bot.V2() || bot.V2Type() != "bot" || !bot.Hidden {
		t.Fatalf("the bot %+v", bot)
	}
	if list, err := s.ChatBots(ctx, bob); err != nil || len(list) != 0 {
		t.Fatalf("a hidden bot in the picker: %v %v", list, err)
	}
	events := func(offset int64, limit int) ([]BotEvent, int64, bool) {
		t.Helper()
		list, next, more, err := s.BotEvents(ctx, bot.ID, offset, limit)
		must(err)
		return list, next, more
	}
	data := func(e BotEvent) map[string]any {
		t.Helper()
		var d map[string]any
		must(json.Unmarshal(e.Data, &d))
		return d
	}

	room, err := s.DirectChat(ctx, ann, bot.ID)
	must(err)
	key := bot.mxid
	join := matrix.Event{EventID: "$join", Type: "m.room.member", Sender: bot.mxid, StateKey: &key, Content: map[string]any{"membership": "join"}}
	join.Unsigned.PrevContent = map[string]any{"membership": "invite"}
	must(s.botEvent(ctx, bot.ID, room, join))
	_, err = s.Send(ctx, ann, room, "hello, bot", "")
	must(err)
	e := fake.sent[len(fake.sent)-1]
	msg := matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content, TS: 1_700_000_000_000}
	must(s.botEvent(ctx, bot.ID, room, msg))
	must(s.botEvent(ctx, bot.ID, room, matrix.Event{EventID: "$edit", Type: "m.room.message", Sender: e.Sender,
		Content: map[string]any{"msgtype": "m.text", "body": "* hello again", "m.new_content": map[string]any{"msgtype": "m.text", "body": "hello again"},
			"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": e.ID}}}))
	must(s.botEvent(ctx, bot.ID, room, matrix.Event{EventID: "$gone", Type: "m.room.redaction", Sender: e.Sender, Redacts: e.ID}))

	list, next, more := events(0, 2)
	if len(list) != 2 || !more || next != list[1].ID+1 || list[0].Type != BotV2JoinChat || list[1].Type != BotV2MessageAdd {
		t.Fatalf("the first page: %+v next %d more %v", list, next, more)
	}
	j := data(list[0])
	chat := j["chat"].(map[string]any)
	if j["dialogId"] != strconv.Itoa(int(annNum)) || chat["type"] != "private" || chat["dialogId"] != j["dialogId"] ||
		j["user"].(map[string]any)["id"] != annNum || j["bot"].(map[string]any)["id"] != float64(bot.Num) || j["language"] != "ru" {
		t.Fatalf("join chat: %v", j)
	}
	m := data(list[1])
	var msgNum float64
	must(pool.QueryRow(ctx, `SELECT num FROM portal_messages WHERE event_id = $1`, e.ID).Scan(&msgNum))
	mm := m["message"].(map[string]any)
	if mm["id"] != msgNum || mm["text"] != "hello, bot" || mm["authorId"] != annNum || mm["chatId"] != chat["id"] ||
		m["user"].(map[string]any)["type"] != "employee" || m["bot"].(map[string]any)["eventMode"] != "fetch" {
		t.Fatalf("message add: %v", m)
	}

	list, next2, more := events(next, 100) // the first two are taken now
	if len(list) != 2 || more || list[0].Type != BotV2MessageUpdate || list[1].Type != BotV2MessageDelete {
		t.Fatalf("the second page: %+v", list)
	}
	if u := data(list[0])["message"].(map[string]any); u["id"] != msgNum || u["text"] != "hello again" {
		t.Fatalf("message update: %v", u)
	}
	if d := data(list[1]); d["messageId"] != msgNum {
		t.Fatalf("message delete: %v", d)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_bot_events WHERE bot_id = $1`, bot.ID).Scan(&left))
	if left != 2 {
		t.Fatalf("events kept after the offset: %d", left)
	}
	if list, n, _ := events(next2, 100); len(list) != 0 || n != next2 {
		t.Fatalf("nothing new: %+v next %d (was %d)", list, n, next2)
	}

	// a command of the bot, without a handler: ONIMBOTV2COMMANDADD
	_, err = s.RegisterCommand(ctx, app, bot.ID, Command{Command: "help"})
	must(err)
	_, err = s.Send(ctx, ann, room, "/help billing", "")
	must(err)
	list, _, _ = events(next2, 100)
	if len(list) != 1 || list[0].Type != BotV2CommandAdd {
		t.Fatalf("the command: %+v", list)
	}
	if c := data(list[0])["command"].(map[string]any); c["command"] != "/help" || c["params"] != "billing" || c["context"] != "textarea" {
		t.Fatalf("the command's data: %v", c)
	}

	// a reaction on the bot's message, put and taken back: ONIMBOTV2REACTIONCHANGE add and delete
	_, err = s.BotSend(ctx, bot.ID, room, "hi there", "", BotParts{})
	must(err)
	mine := fake.sent[len(fake.sent)-1]
	must(s.SetReaction(ctx, ann, room, mine.ID, "🔥", true))
	r := fake.sent[len(fake.sent)-1]
	must(s.botEvent(ctx, bot.ID, room, matrix.Event{EventID: r.ID, Type: "m.reaction", Sender: r.Sender, Content: r.Content}))
	must(s.botEvent(ctx, bot.ID, room, matrix.Event{EventID: "$unreact", Type: "m.room.redaction", Sender: r.Sender, Redacts: r.ID}))
	list, _, _ = events(next2, 100)
	if len(list) != 3 || list[1].Type != BotV2Reaction || list[2].Type != BotV2Reaction {
		t.Fatalf("the reactions: %+v", list)
	}
	add, del := data(list[1]), data(list[2])
	if add["reaction"] != "fire" || add["action"] != "add" || add["message"].(map[string]any)["text"] != "hi there" ||
		add["user"].(map[string]any)["id"] != annNum || del["action"] != "delete" || del["reaction"] != "fire" {
		t.Fatalf("the reactions' data: %v %v", add, del)
	}

	// the messages around one: the bot's message between ann's command and nothing after it
	evs, authors, prev, more2, err := s.BotAround(ctx, bot.ID, room, mine.ID, 1)
	must(err)
	if len(evs) != 2 || evs[1].EventID != mine.ID || evs[0].Content["body"] != "/help billing" || authors[evs[0].Sender] != ann ||
		!prev || more2 {
		t.Fatalf("around: %+v %v prev %v next %v", evs, authors, prev, more2)
	}

	// files (GO24DEV-963): the bot sends one, ann's file comes with params.FILE_ID — its message's number —, the bot
	// reads it
	_, err = s.SendFile(ctx, bot.ID, room, "", "report.txt", "text/plain", strings.NewReader("from the bot"), 12, nil)
	must(err)
	if f := fake.sent[len(fake.sent)-1]; f.Sender != bot.mxid || fileOf(f.Content) == nil {
		t.Fatalf("the bot's file: %+v", f)
	}
	_, err = s.SendFile(ctx, ann, room, "", "photo.txt", "text/plain", strings.NewReader("from ann"), 8, nil)
	must(err)
	e = fake.sent[len(fake.sent)-1]
	fm, err := s.V2Message(ctx, room, matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}, ann)
	must(err)
	if p := fm["params"].(map[string]any); p["FILE_NAME"] != "photo.txt" || len(p["FILE_ID"].([]int64)) != 1 || p["FILE_ID"].([]int64)[0] != fm["id"] {
		t.Fatalf("a file's message: %v", fm)
	}
	must(s.botEvent(ctx, bot.ID, room, matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}))
	var fe string
	must(pool.QueryRow(ctx, `SELECT data::text FROM im_bot_events WHERE bot_id = $1 ORDER BY id DESC LIMIT 1`, bot.ID).Scan(&fe))
	var fd map[string]any
	must(json.Unmarshal([]byte(fe), &fd))
	if p := fd["message"].(map[string]any)["params"].(map[string]any); p["FILE_NAME"] != "photo.txt" || p["FILE_ID"].([]any)[0] != float64(fm["id"].(int64)) {
		t.Fatalf("the event of a file: %v", fd["message"])
	}
	f, body, err := s.OpenFile(ctx, bot.ID, room, e.ID)
	must(err)
	got, _ := io.ReadAll(body)
	body.Close()
	if f.Name != "photo.txt" || string(got) != "from ann" {
		t.Fatalf("the bot reads ann's file: %+v %q", f, got)
	}
	if _, _, err := s.OpenFile(ctx, bob, room, e.ID); err == nil {
		t.Fatal("bob reads a file of a chat he is not in")
	}

	// the input field switched off by the bot (GO24DEV-964): textFieldEnabled of the chat
	_, err = pool.Exec(ctx, `UPDATE portal_chats SET text_field = false WHERE room_id = $1`, room)
	must(err)
	if c, err := s.V2Chat(ctx, bot, room); err != nil || c["textFieldEnabled"] != "N" {
		t.Fatalf("the chat with its field off: %v %v", c, err)
	}

	// the chat opened by a link with BOT_CONTEXT (GO24DEV-965): ONIMBOTV2CONTEXTGET; the bot found by its number
	if lb, err := s.LiveBot(ctx, bot.Num); err != nil || lb.ID != bot.ID {
		t.Fatalf("the bot by its number: %+v %v", lb, err)
	}
	must(s.BotContext(ctx, bot, ann, room, map[string]any{"action": "openTask", "taskId": 456}))
	var cx string
	must(pool.QueryRow(ctx, `SELECT data::text FROM im_bot_events WHERE bot_id = $1 AND type = $2`, bot.ID, BotV2ContextGet).Scan(&cx))
	var cd map[string]any
	must(json.Unmarshal([]byte(cx), &cd))
	if cd["dialogId"] != strconv.Itoa(int(annNum)) || cd["context"].(map[string]any)["taskId"] != float64(456) ||
		cd["user"].(map[string]any)["id"] != annNum || cd["chat"].(map[string]any)["type"] != "private" {
		t.Fatalf("the context: %v", cd)
	}

	// a «webhook» bot: its events go to its address, the bot as {id, code}
	const hook = "https://helper.example/v2"
	for _, ev := range BotV2Events {
		_, err := pool.Exec(ctx, `INSERT INTO app_events (app_id, event, handler_url, user_id, portal) VALUES ($1, $2, $3, $4, true)`, app, ev, hook, ann)
		must(err)
	}
	wb, err := s.RegisterBot(ctx, app, ann, Bot{Code: "hooked", Name: "Hooked", EventMode: "webhook", WebhookURL: hook})
	must(err)
	if list, err := s.ChatBots(ctx, bob); err != nil || len(list) != 1 || list[0].ID != wb.ID {
		t.Fatalf("the picker: %v %v", list, err)
	}
	wroom, err := s.DirectChat(ctx, bob, wb.ID)
	must(err)
	_, err = s.Send(ctx, bob, wroom, "ping", "")
	must(err)
	e = fake.sent[len(fake.sent)-1]
	must(s.botEvent(ctx, wb.ID, wroom, matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}))
	var raw string
	must(pool.QueryRow(ctx, `SELECT data::text FROM app_event_deliveries WHERE event = $1`, BotV2MessageAdd).Scan(&raw))
	var d map[string]any
	must(json.Unmarshal([]byte(raw), &d))
	if b := d["bot"].(map[string]any); b["id"] != float64(wb.Num) || b["code"] != "hooked" || len(b) != 2 ||
		d["message"].(map[string]any)["text"] != "ping" {
		t.Fatalf("the webhook's event: %v", d)
	}

	// the bot taken off: its last event
	must(s.DeleteBot(ctx, app, bot.ID))
	var last string
	must(pool.QueryRow(ctx, `SELECT type FROM im_bot_events WHERE bot_id = $1 ORDER BY id DESC LIMIT 1`, bot.ID).Scan(&last))
	if last != BotV2Delete {
		t.Fatalf("the last event %s", last)
	}
}
