package notifyout

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeTelegram is the Bot API of one bot: token «good», messages queued by the test, chat 666 has blocked the bot.
type fakeTelegram struct {
	mu      sync.Mutex
	updates []map[string]any
	sent    []map[string]any
}

func (f *fakeTelegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	if token != "good" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	var args map[string]any
	_ = json.NewDecoder(r.Body).Decode(&args)
	answer := func(result any) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
	switch method {
	case "getMe":
		answer(map[string]any{"id": 1, "is_bot": true, "username": "go24_test_bot"})
	case "getUpdates":
		from := int64(args["offset"].(float64))
		var out []map[string]any
		for _, u := range f.updates {
			if int64(u["update_id"].(int)) >= from {
				out = append(out, u)
			}
		}
		answer(out)
	case "sendMessage":
		if args["chat_id"] == "666" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
			return
		}
		f.sent = append(f.sent, args)
		answer(map[string]any{"message_id": len(f.sent)})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeTelegram) write(chat int64, user, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates = append(f.updates, map[string]any{"update_id": 100 + len(f.updates), "message": map[string]any{
		"text": text, "chat": map[string]any{"id": chat, "type": "private"}, "from": map[string]any{"username": user, "first_name": user}}})
}

// took are the texts sent since the last call.
func (f *fakeTelegram) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		out = append(out, m["chat_id"].(string)+": "+m["text"].(string))
	}
	f.sent = nil
	return out
}

// TestTelegram: the administrators set the bot (a wrong token is refused, the right one is kept sealed), an employee
// links the chat by the code, gets the notifications after the link but not those turned off for Telegram or read,
// /stop and a blocked bot unlink the chat.
func TestTelegram(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	anna, bob := user("Анна"), user("Боб")
	note := func(userID, kind string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, $2, '') RETURNING id::text`, userID, kind).Scan(&id))
		return id
	}

	tg := &fakeTelegram{}
	srv := httptest.NewServer(tg)
	defer srv.Close()
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Telegram{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	// no bot yet: nothing to link
	if links, err := s.Links(ctx, anna); err != nil || len(links) != 0 {
		t.Fatalf("links without a bot: %v %v", links, err)
	}
	if _, err := s.StartLink(ctx, anna, "telegram"); err == nil {
		t.Fatal("linking without a bot")
	}
	if err := s.SaveBot(ctx, "telegram", true, "", "bad", false, anna); err == nil {
		t.Fatal("a wrong token was kept")
	}
	must(s.SaveBot(ctx, "telegram", true, "", "good", false, anna))
	b, err := s.Bot(ctx, "telegram")
	must(err)
	if !b.Ready() || b.Name != "go24_test_bot" || !b.HasToken {
		t.Fatalf("the bot: %+v", b)
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT secret FROM notify_bots`).Scan(&sealed))
	if strings.Contains(string(sealed), "good") {
		t.Fatal("the token is kept as it is")
	}
	must(s.SaveBot(ctx, "telegram", true, "", "", false, anna)) // an empty token keeps the one set
	if b, _ = s.Bot(ctx, "telegram"); !b.Ready() {
		t.Fatal("saving without a token forgot it")
	}

	// linking: the code in the bot's link; a wrong code is answered so
	l, err := s.StartLink(ctx, anna, "telegram")
	must(err)
	if l.URL != "https://t.me/go24_test_bot?start="+l.Code || len(l.Code) != 16 {
		t.Fatalf("the link: %+v", l)
	}
	note(anna, "comment") // before the link: not sent
	tg.write(100, "anna", "/start nonsense")
	tg.write(100, "anna", "/start "+l.Code)
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"100: notifyout.bot_bad_code", "100: notifyout.bot_linked Анна"}) {
		t.Fatalf("the bot's answers: %q", got)
	}
	links, err := s.Links(ctx, anna)
	must(err)
	if len(links) != 1 || !links[0].Linked || links[0].Account != "@anna" || links[0].Code != "" {
		t.Fatalf("linked: %+v", links)
	}
	s.Pass(ctx) // the messages are read once
	if got := tg.took(); len(got) != 0 {
		t.Fatalf("sent again: %q", got)
	}

	// the notifications after the link, not those turned off for Telegram and not those already read
	must(s.SaveOff(ctx, anna, map[string]map[string]bool{"telegram": {"comment": true, "status": true}}))
	c := note(anna, "comment")
	note(anna, "like")
	_, err = pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE id = $1`, note(anna, "status"))
	must(err)
	note(bob, "comment") // Bob has no chat
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{`100: <a href="https://go24.test/notifications/` + c + `">уведомление comment</a>`}) {
		t.Fatalf("delivered: %q", got)
	}
	if links, _ = s.Links(ctx, anna); !links[0].Off["like"] || links[0].Off["comment"] {
		t.Fatalf("the kinds off: %v", links[0].Off)
	}

	// Bob links the same chat: it is Bob's now, Anna's link is gone
	l, err = s.StartLink(ctx, bob, "telegram")
	must(err)
	tg.write(100, "anna", l.Code)
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"100: notifyout.bot_linked Боб"}) {
		t.Fatalf("relinked: %q", got)
	}
	if links, _ = s.Links(ctx, anna); links[0].Linked {
		t.Fatal("one chat, two people")
	}

	// /stop unlinks
	tg.write(100, "anna", "/stop")
	s.Pass(ctx)
	if got := tg.took(); !slices.Equal(got, []string{"100: notifyout.bot_unlinked"}) {
		t.Fatalf("/stop: %q", got)
	}
	if links, _ = s.Links(ctx, bob); links[0].Linked {
		t.Fatal("/stop kept the link")
	}

	// a blocked bot: the link is dropped
	l, err = s.StartLink(ctx, anna, "telegram")
	must(err)
	tg.write(666, "anna", "/start "+l.Code)
	s.Pass(ctx)
	note(anna, "comment")
	s.Pass(ctx)
	if links, _ = s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a blocked bot kept the link")
	}

	// the bot off: nothing to link
	must(s.SaveBot(ctx, "telegram", false, "", "", false, anna))
	if links, _ = s.Links(ctx, anna); len(links) != 0 {
		t.Fatalf("links of a bot off: %+v", links)
	}
}
