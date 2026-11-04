package matrixsync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestIMV2Events (GO24DEV-966): an employee subscribed gets the events of im.v2 of their chats — a message, its edit, a
// reaction put and taken back, a message taken back, someone joining — in order, by offset; unsubscribed, none are
// kept.
func TestIMV2Events(t *testing.T) {
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
	ann, _ := newUser("ann")
	bob, bobNum := newUser("bob")
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	me, tok, err := s.token(ctx, ann)
	must(err)
	room, err := s.DirectChat(ctx, bob, ann)
	must(err)
	last := func() matrix.Event {
		e := fake.sent[len(fake.sent)-1]
		return matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}
	}
	feed := func(ev matrix.Event) {
		t.Helper()
		must(s.imV2Event(ctx, ann, me, tok, room, ev))
	}

	_, err = s.Send(ctx, bob, room, "before", "")
	must(err)
	feed(last()) // not subscribed yet: nothing kept
	must(s.SubscribeV2(ctx, ann, true))
	_, err = s.Send(ctx, bob, room, "hello, ann", "")
	must(err)
	msg := last()
	feed(msg)
	feed(matrix.Event{EventID: "$edit", Type: "m.room.message", Sender: msg.Sender, Content: map[string]any{"msgtype": "m.text",
		"body": "* hello again", "m.new_content": map[string]any{"msgtype": "m.text", "body": "hello again"},
		"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": msg.EventID}}})
	must(s.SetReaction(ctx, bob, room, msg.EventID, "👍", true))
	r := fake.sent[len(fake.sent)-1]
	feed(matrix.Event{EventID: r.ID, Type: "m.reaction", Sender: r.Sender, Content: r.Content})
	feed(matrix.Event{EventID: "$unreact", Type: "m.room.redaction", Sender: r.Sender, Redacts: r.ID})
	feed(matrix.Event{EventID: "$gone", Type: "m.room.redaction", Sender: msg.Sender, Redacts: msg.EventID})
	key := s.M.UserID("bob")
	join := matrix.Event{EventID: "$join", Type: "m.room.member", Sender: key, StateKey: &key, Content: map[string]any{"membership": "join"}}
	feed(join)

	list, next, more, err := s.EventsV2(ctx, ann, 0, 100)
	must(err)
	types := []string{}
	for _, e := range list {
		types = append(types, e.Type)
	}
	want := []string{IMV2MessageAdd, IMV2MessageUpdate, IMV2Reaction, IMV2Reaction, IMV2MessageDelete, IMV2JoinChat}
	if more || len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events %v, want %v", types, want)
		}
	}
	data := func(e BotEvent) map[string]any {
		var d map[string]any
		must(json.Unmarshal(e.Data, &d))
		return d
	}
	add := data(list[0])
	m := add["message"].(map[string]any)
	if m["text"] != "hello, ann" || m["authorId"] != bobNum || add["user"].(map[string]any)["id"] != bobNum ||
		add["chat"].(map[string]any)["dialogId"] != strconv.Itoa(int(bobNum)) || add["chat"].(map[string]any)["type"] != "private" || add["language"] == nil {
		t.Fatalf("message add: %v", add)
	}
	if u := data(list[1])["message"].(map[string]any); u["id"] != m["id"] || u["text"] != "hello again" {
		t.Fatalf("message update: %v", u)
	}
	if a, d := data(list[2]), data(list[3]); a["reaction"] != "like" || a["action"] != "add" || d["action"] != "delete" || d["reaction"] != "like" ||
		a["message"].(map[string]any)["id"] != m["id"] {
		t.Fatalf("reactions: %v %v", a, d)
	}
	if d := data(list[4]); d["messageId"] != m["id"] {
		t.Fatalf("message delete: %v", d)
	}
	if j := data(list[5]); j["user"].(map[string]any)["id"] != bobNum || j["dialogId"] == nil {
		t.Fatalf("join: %v", j)
	}
	if list, n, _, err := s.EventsV2(ctx, ann, next, 100); err != nil || len(list) != 0 || n != next {
		t.Fatalf("taken: %v %d %v", list, n, err)
	}

	must(s.SubscribeV2(ctx, ann, false))
	feed(msg)
	var kept int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM im_v2_events`).Scan(&kept))
	if kept != 0 {
		t.Fatalf("kept after unsubscribe: %d", kept)
	}
}
