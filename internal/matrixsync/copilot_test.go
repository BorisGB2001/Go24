package matrixsync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestCoPilot: CoPilot in the messenger — the bot made by go24 itself, a private chat with it (greeting, answers with
// the chat's history, as the one who wrote), a group chat (only what mentions it, people named), a thread, a failure
// told in words, no CoPilot for guests; app bots' lists keep it apart; a «Чат с CoPilot» of a collab (every message
// answered, the collab told of as the one who asks sees it; not made by a guest, guests not answered).
func TestCoPilot(t *testing.T) {
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
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	var mu sync.Mutex
	type asked struct {
		user, about string
		turns       []Turn
	}
	var asks []asked
	fail := false
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co",
		CoPilotName: "CoPilot", CoPilotHello: "Здравствуйте, я CoPilot", CoPilotCollabHello: "Я знаю этот коллаб",
		CoPilotAsk: func(ctx context.Context, userID, about string, turns []Turn) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			asks = append(asks, asked{userID, about, turns})
			if fail {
				return "", errors.New("no connection")
			}
			return "Ответ №" + string(rune('0'+len(asks))), nil
		},
		CoPilotError: func(err error) string { return "⚠ " + err.Error() }})
	must(s.Reconcile(ctx))

	if id, err := s.CoPilotID(ctx); err != nil || id != "" {
		t.Fatalf("CoPilot before it is made: %q %v", id, err)
	}
	must(s.EnsureCoPilot(ctx))
	must(s.EnsureCoPilot(ctx)) // again: the same bot
	id, err := s.CoPilotID(ctx)
	must(err)
	mx := "@copilot:t.lan"
	if u := fake.users[mx]; id == "" || u == nil || u.DisplayName != "CoPilot" {
		t.Fatalf("CoPilot %q, account %+v", id, u)
	}
	if list, err := s.Bots(ctx, "app"); err != nil || len(list) != 0 {
		t.Fatalf("an application's bots: %v %v", list, err)
	}
	if list, err := s.ChatBots(ctx, ann); err != nil || len(list) != 1 || list[0].Builtin != CoPilot {
		t.Fatalf("the bots ann may write to: %+v %v", list, err)
	}
	if _, err := s.CoPilotChat(ctx, eve); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("a guest's CoPilot: %v", err)
	}
	must(pool.QueryRow(ctx, `SELECT 1 FROM im_bots WHERE builtin = 'copilot' AND closed_at IS NULL`).Scan(new(int)))
	must(s.closeBots(ctx))
	if u := fake.users[mx]; u.Deactivated {
		t.Fatal("CoPilot is closed as a gone app's bot")
	}

	fromCoPilot := func(room string) []sentEvent {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		var out []sentEvent
		for _, e := range fake.sent {
			if e.Room == room && e.Sender == mx && e.Content["msgtype"] != nil {
				out = append(out, e)
			}
		}
		return out
	}
	wait := func(room string, n int) []sentEvent {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if list := fromCoPilot(room); len(list) >= n {
				return list
			}
		}
		t.Fatalf("CoPilot wrote %d messages to %s, want %d", len(fromCoPilot(room)), room, n)
		return nil
	}
	last := func(room, sender string) matrix.Event {
		t.Helper()
		fake.mu.Lock()
		defer fake.mu.Unlock()
		e := fake.sent[len(fake.sent)-1]
		if e.Room != room || e.Sender != sender {
			t.Fatalf("last sent %+v", e)
		}
		return matrix.Event{EventID: e.ID, Type: "m.room.message", Sender: e.Sender, Content: e.Content}
	}

	// the private chat: opened by ann, CoPilot greets her once it joins, answers each message with the history
	room, err := s.CoPilotChat(ctx, ann)
	must(err)
	if again, err := s.CoPilotChat(ctx, ann); err != nil || again != room {
		t.Fatalf("the chat again: %q %v", again, err)
	}
	key := mx
	join := matrix.Event{EventID: "$join", Type: "m.room.member", Sender: mx, StateKey: &key, Content: map[string]any{"membership": "join"}}
	join.Unsigned.PrevContent = map[string]any{"membership": "invite"}
	must(s.botEvent(ctx, id, room, join))
	if list := fromCoPilot(room); len(list) != 1 || list[0].Content["body"] != "Здравствуйте, я CoPilot" {
		t.Fatalf("the greeting: %+v", list)
	}
	_, err = s.Send(ctx, ann, room, "Напиши слоган", "")
	must(err)
	must(s.botEvent(ctx, id, room, last(room, "@ann:t.lan")))
	if list := wait(room, 2); list[1].Content["body"] != "Ответ №1" {
		t.Fatalf("the answer: %+v", list[1])
	}
	_, err = s.Send(ctx, ann, room, "Короче", "")
	must(err)
	must(s.botEvent(ctx, id, room, last(room, "@ann:t.lan")))
	wait(room, 3)
	mu.Lock()
	got := asks[1]
	mu.Unlock()
	want := []Turn{{Text: "Напиши слоган"}, {Bot: true, Text: "Ответ №1"}, {Text: "Короче"}}
	if got.user != ann || !slices.Equal(got.turns, want) {
		t.Fatalf("asked as %s: %+v", got.user, got.turns)
	}
	if fake.typing[room+" "+mx] {
		t.Fatal("CoPilot is still typing")
	}

	// a group chat: only a mention is answered, as the one who wrote, the people named
	group, err := s.CreateGroup(ctx, ann, "Команда", []string{bob, id}, false)
	must(err)
	_, err = s.Send(ctx, ann, group, "всем привет", "")
	must(err)
	must(s.botEvent(ctx, id, group, last(group, "@ann:t.lan")))
	_, err = s.Send(ctx, bob, group, "@CoPilot, итоги?", "")
	must(err)
	ev := last(group, "@bob:t.lan")
	if !mentions(ev.Content, mx) {
		t.Fatalf("CoPilot is not mentioned: %v", ev.Content)
	}
	must(s.botEvent(ctx, id, group, ev))
	wait(group, 1)
	time.Sleep(100 * time.Millisecond)
	if n := len(fromCoPilot(group)); n != 1 {
		t.Fatalf("CoPilot answered %d times in the group", n)
	}
	mu.Lock()
	got = asks[len(asks)-1]
	mu.Unlock()
	if got.user != bob || len(got.turns) != 1 || !strings.HasPrefix(got.turns[0].Text, "ann: всем привет\n\nbob: @CoPilot") {
		t.Fatalf("asked in the group as %s: %+v", got.user, got.turns)
	}

	// a thread is answered in the thread; a failure comes back in words
	root := ev.EventID
	mu.Lock()
	fail = true
	mu.Unlock()
	_, err = s.Send(ctx, bob, group, "@CoPilot а подробнее?", root)
	must(err)
	must(s.botEvent(ctx, id, group, last(group, "@bob:t.lan")))
	list := wait(group, 2)
	rel, _ := list[1].Content["m.relates_to"].(map[string]any)
	if list[1].Content["body"] != "⚠ no connection" || list[1].Content["msgtype"] != "m.notice" || rel["rel_type"] != "m.thread" || rel["event_id"] != root {
		t.Fatalf("the answer in the thread: %+v", list[1].Content)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	_, err = s.Send(ctx, bob, group, "@CoPilot ещё раз", root)
	must(err)
	must(s.botEvent(ctx, id, group, last(group, "@bob:t.lan")))
	wait(group, 3)
	mu.Lock()
	got = asks[len(asks)-1]
	mu.Unlock()
	if len(got.turns) != 1 || strings.Contains(got.turns[0].Text, "no connection") || !strings.Contains(got.turns[0].Text, "ещё раз") ||
		strings.Contains(got.turns[0].Text, "всем привет") || !strings.Contains(got.turns[0].Text, "итоги?") ||
		!strings.Contains(got.turns[0].Text, "а подробнее?") {
		t.Fatalf("the thread's history: %+v", got.turns)
	}
	if got.about != "" {
		t.Fatalf("a plain group chat told of a collab: %q", got.about)
	}

	// «Чат с CoPilot» of a collab: ann's collab with bob and the guest eve; a task of it bob may not see
	ts := s.Tasks
	ckey, err := ts.CreateCollab(ctx, ann, "Сайт клиента", "Новый сайт к весне", []string{eve})
	must(err)
	c, err := ts.CollabByKey(ctx, ann, ckey)
	must(err)
	must(ts.SetMember(tasks.WithViewer(ctx, ann, false), c, bob, "member"))
	late := time.Now().Add(-48 * time.Hour)
	k1, err := ts.CreateTask(ctx, ann, c, tasks.NewTask{Title: "Макет главной", ResponsibleID: bob, Deadline: &late})
	must(err)
	_, err = ts.CreateTask(ctx, ann, c, tasks.NewTask{Title: "Договор", ResponsibleID: ann})
	must(err)
	actx := tasks.WithViewer(ctx, ann, false)
	if _, err := s.CreateCollabCoPilot(tasks.WithViewer(ctx, eve, true), eve, c.ID, "Чат с CoPilot", nil); err == nil {
		t.Fatal("a guest made a «Чат с CoPilot»")
	}
	cchat, err := s.CreateCollabCoPilot(actx, ann, c.ID, "Чат с CoPilot", nil)
	must(err)
	if s.CoPilotCollab(ctx, cchat) != c.ID || s.CoPilotCollab(ctx, group) != "" {
		t.Fatal("CoPilotCollab")
	}
	if chats, err := s.CollabChats(ctx, ann, c.ID); err != nil || len(chats) != 1 || !chats[0].CoPilot || chats[0].Name != "Чат с CoPilot" {
		t.Fatalf("the collab's chats: %+v %v", chats, err)
	}
	must(s.botEvent(ctx, id, cchat, join))
	if list := fromCoPilot(cchat); len(list) != 1 || list[0].Content["body"] != "Я знаю этот коллаб" {
		t.Fatalf("the greeting in the collab: %+v", list)
	}
	_, err = s.Send(ctx, ann, cchat, "Что просрочено?", "") // no mention: answered all the same
	must(err)
	must(s.botEvent(ctx, id, cchat, last(cchat, "@ann:t.lan")))
	wait(cchat, 2)
	mu.Lock()
	got = asks[len(asks)-1]
	mu.Unlock()
	for _, w := range []string{"Сайт клиента", "Новый сайт к весне", "ann (owner)", "eve (", "guest", k1 + " «Макет главной»", "responsible bob", "(OVERDUE)", "«Договор»", "2 open (1 overdue)"} {
		if !strings.Contains(got.about, w) {
			t.Fatalf("the collab told without %q:\n%s", w, got.about)
		}
	}
	if got.user != ann || len(got.turns) != 1 || got.turns[0].Text != "ann: Что просрочено?" {
		t.Fatalf("asked in the collab as %s: %+v", got.user, got.turns)
	}
	// a guest's message is not answered
	n := len(fromCoPilot(cchat))
	ev = matrix.Event{EventID: "$guest", Type: "m.room.message", Sender: "@eve:t.lan", Content: map[string]any{"msgtype": "m.text", "body": "А мне?"}}
	must(s.botEvent(ctx, id, cchat, ev))
	time.Sleep(100 * time.Millisecond)
	if len(fromCoPilot(cchat)) != n {
		t.Fatal("CoPilot answered a guest")
	}
}

// TestMergeTurns: the turns alternate and start with a person's.
func TestMergeTurns(t *testing.T) {
	got := mergeTurns([]Turn{{Bot: true, Text: "привет"}, {Text: "a"}, {Text: "b"}, {Bot: true, Text: "c"}, {Bot: true, Text: "d"}, {Text: "e"}})
	want := []Turn{{Text: "a\n\nb"}, {Bot: true, Text: "c\n\nd"}, {Text: "e"}}
	if !slices.Equal(got, want) {
		t.Fatalf("%+v", got)
	}
}
