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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/net/websocket"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeSlack is Slack's Web API for the bot «go24bot» (tokens xoxb-good and xapp-good) and its Socket Mode socket;
// the direct chat with Anna is D1, the one with a deactivated person D6. The test writes the events.
type fakeSlack struct {
	mu     sync.Mutex
	ws     string // the socket's address
	conns  []*websocket.Conn
	acked  []string
	sent   []string
	opened int
	seq    int
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/socket" {
		websocket.Handler(func(c *websocket.Conn) {
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			_ = websocket.JSON.Send(c, map[string]any{"type": "hello"})
			for {
				var ack struct {
					EnvelopeID string `json:"envelope_id"`
				}
				if err := websocket.JSON.Receive(c, &ack); err != nil {
					return
				}
				f.mu.Lock()
				f.acked = append(f.acked, ack.EnvelopeID)
				f.mu.Unlock()
			}
		}).ServeHTTP(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	auth := r.Header.Get("Authorization")
	answer := func(v map[string]any) {
		v["ok"] = true
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code string) { _ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": code}) }
	switch method := strings.TrimPrefix(r.URL.Path, "/api/"); {
	case method == "apps.connections.open":
		if auth != "Bearer xapp-good" {
			fail("invalid_auth")
			return
		}
		f.opened++
		answer(map[string]any{"url": f.ws})
	case auth != "Bearer xoxb-good":
		fail("invalid_auth")
	case method == "auth.test":
		answer(map[string]any{"user": "go24bot", "team": "Тест"})
	case method == "users.info":
		answer(map[string]any{"user": map[string]any{"id": r.Form.Get("user"), "name": "anna"}})
	case method == "chat.postMessage":
		if r.Form.Get("channel") == "D6" {
			fail("user_disabled")
			return
		}
		f.sent = append(f.sent, r.Form.Get("channel")+": "+r.Form.Get("text"))
		answer(map[string]any{})
	default:
		fail("unknown_method")
	}
}

// event writes an event to the open socket and waits for go24 to acknowledge it.
func (f *fakeSlack) event(t *testing.T, kind string, ev map[string]any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.conns)
		f.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("go24 did not open the socket")
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	f.seq++
	id := "env" + strconv.Itoa(f.seq)
	c := f.conns[len(f.conns)-1]
	f.mu.Unlock()
	if err := websocket.JSON.Send(c, map[string]any{"type": kind, "envelope_id": id, "payload": map[string]any{"event": ev}}); err != nil {
		t.Fatal(err)
	}
	for !slices.Contains(f.took(&f.acked, false), id) {
		if time.Now().After(deadline) {
			t.Fatalf("envelope %s not acknowledged", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeSlack) took(list *[]string, clear bool) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(*list)
	if clear {
		*list = nil
	}
	return out
}

func dm(channel, text string) map[string]any {
	return map[string]any{"type": "message", "channel_type": "im", "channel": channel, "user": "U1", "text": text}
}

// TestSlack: the bot is set by both tokens (one of them or a wrong one refused); the socket opens when the
// messages are first asked for, every envelope is acknowledged; the code in the direct chat links it (not in a
// channel, not the bot's own message), the notifications are messages with a link, «stop» unlinks, a deactivated
// person's chat drops the link, «disconnect» opens the socket again.
func TestSlack(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	note := func(kind string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, $2, '') RETURNING id::text`, anna, kind).Scan(&id))
		return id
	}
	fs := &fakeSlack{}
	srv := httptest.NewServer(fs)
	defer srv.Close()
	fs.ws = "ws" + strings.TrimPrefix(srv.URL, "http") + "/socket"
	sl := &Slack{API: srv.URL + "/api", HTTP: srv.Client(), retry: 50 * time.Millisecond, idle: time.Minute}
	s := New(pool, "go24 secret")
	s.Channels = []Channel{sl}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление <" + it.Kind + ">" }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}
	// pass repeats a round until the bot has written something (the socket brings the messages on its own)
	pass := func() []string {
		t.Helper()
		for i := 0; i < 100; i++ {
			s.Pass(ctx)
			if got := fs.took(&fs.sent, true); len(got) > 0 {
				return got
			}
			time.Sleep(20 * time.Millisecond)
		}
		return nil
	}

	if err := s.SaveBot(ctx, "slack", true, "", "xoxb-good", false, anna); err == nil {
		t.Fatal("a bot without the app-level token")
	}
	if err := s.SaveBot(ctx, "slack", true, "", "xoxb-good xapp-bad", false, anna); err == nil {
		t.Fatal("a wrong app-level token was kept")
	}
	must(s.SaveBot(ctx, "slack", true, "", " xapp-good\nxoxb-good ", false, anna))
	if b, err := s.Bot(ctx, "slack"); err != nil || !b.Ready() || b.Name != "go24bot" {
		t.Fatalf("the bot: %+v %v", b, err)
	}
	s.Pass(ctx) // opens the socket
	l, err := s.StartLink(ctx, anna, "slack")
	must(err)
	if l.URL != "" || l.Code == "" {
		t.Fatalf("the link: %+v", l)
	}
	fs.event(t, "events_api", map[string]any{"type": "message", "channel_type": "channel", "channel": "C1", "user": "U1", "text": l.Code})
	fs.event(t, "events_api", map[string]any{"type": "message", "channel_type": "im", "channel": "D1", "bot_id": "B1", "text": l.Code})
	fs.event(t, "events_api", dm("D1", l.Code))
	if got := pass(); !slices.Equal(got, []string{"D1: notifyout.bot_linked Анна"}) {
		t.Fatalf("the bot's answers: %q", got)
	}
	if links, _ := s.Links(ctx, anna); len(links) != 1 || !links[0].Linked || links[0].Account != "@anna" {
		t.Fatalf("linked: %+v", links)
	}
	c := note("comment")
	if got := pass(); !slices.Equal(got, []string{"D1: <https://go24.test/notifications/" + c + "|уведомление &lt;comment&gt;>"}) {
		t.Fatalf("delivered: %q", got)
	}
	fs.mu.Lock()
	conns := len(fs.conns)
	fs.mu.Unlock()
	fs.event(t, "disconnect", nil) // Slack refreshes the connection: go24 opens a new socket
	for i := 0; ; i++ {
		fs.mu.Lock()
		n := len(fs.conns)
		fs.mu.Unlock()
		if n > conns {
			break
		}
		if i > 300 {
			t.Fatal("the socket was not opened again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fs.event(t, "events_api", dm("D1", "Stop"))
	if got := pass(); !slices.Equal(got, []string{"D1: notifyout.bot_unlinked"}) {
		t.Fatalf("stop: %q", got)
	}
	if fs.opened < 3 { // the check, the first socket, the one after «disconnect»
		t.Fatalf("sockets opened: %d", fs.opened)
	}
	l, err = s.StartLink(ctx, anna, "slack")
	must(err)
	fs.event(t, "events_api", dm("D6", l.Code))
	pass()
	note("comment")
	s.Pass(ctx)
	if links, _ := s.Links(ctx, anna); links[0].Linked {
		t.Fatal("a deactivated person's chat kept the link")
	}
}
