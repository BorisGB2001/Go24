package notifyout

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// fakeViber is the Viber REST API of one bot: token «good», user «gone» has unsubscribed, the webhook is refused for
// an address not on https.
type fakeViber struct {
	mu   sync.Mutex
	hook string
	sent []string
}

func (f *fakeViber) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("X-Viber-Auth-Token") != "good" {
		_, _ = io.WriteString(w, `{"status":2,"status_message":"invalidAuthToken"}`)
		return
	}
	var body struct {
		URL      string `json:"url"`
		Receiver string `json:"receiver"`
		Text     string `json:"text"`
		Sender   struct{ Name string }
		Keyboard *struct {
			Buttons []struct{ ActionBody, Text string }
		} `json:"keyboard"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch r.URL.Path {
	case "/get_account_info":
		_, _ = io.WriteString(w, `{"status":0,"status_message":"ok","id":"pa:1","name":"go24 уведомления","uri":"go24bot"}`)
	case "/set_webhook":
		if body.URL != "" && !strings.HasPrefix(body.URL, "https://") {
			_, _ = io.WriteString(w, `{"status":1,"status_message":"invalidUrl"}`)
			return
		}
		f.hook = body.URL
		_, _ = io.WriteString(w, `{"status":0,"status_message":"ok"}`)
	case "/send_message":
		if body.Receiver == "gone" {
			_, _ = io.WriteString(w, `{"status":6,"status_message":"receiverNotSubscribed"}`)
			return
		}
		f.sent = append(f.sent, body.Receiver+": "+body.Text+" ["+body.Sender.Name+"]")
		_, _ = io.WriteString(w, `{"status":0,"status_message":"ok","message_token":1}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeViber) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// signed is a call of Viber to go24, signed with the token.
func signed(token string, event map[string]any) (http.Header, []byte) {
	body, _ := json.Marshal(event)
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write(body)
	return http.Header{"X-Viber-Content-Signature": {hex.EncodeToString(mac.Sum(nil))}}, body
}

// TestViber: the Viber bot is set by its token and told go24's address (on https only); the link opens the chat with
// the code, the bot answers the call with a button that sends it, the code links the chat; the notifications go
// there; an unsigned call is refused, unsubscribing unlinks.
func TestViber(t *testing.T) {
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
	fv := &fakeViber{}
	srv := httptest.NewServer(fv)
	defer srv.Close()
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Viber{API: srv.URL, HTTP: srv.Client()}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "http://go24.lan", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	if err := s.SaveBot(ctx, "viber", true, "", "bad", false, anna); !errors.Is(err, errToken) {
		t.Fatalf("a wrong token: %v", err)
	}
	if err := s.SaveBot(ctx, "viber", true, "", "good", false, anna); !errors.Is(err, errHook) {
		t.Fatalf("go24 not on https: %v", err)
	}
	s.AppURL = "https://go24.example"
	must(s.SaveBot(ctx, "viber", true, "", "good", false, anna))
	if fv.hook != "https://go24.example/hooks/notify/viber" {
		t.Fatalf("the webhook: %q", fv.hook)
	}
	if b, err := s.Bot(ctx, "viber"); err != nil || !b.Ready() || b.Name != "go24bot" {
		t.Fatalf("the bot: %+v %v", b, err)
	}
	l, err := s.StartLink(ctx, anna, "viber")
	must(err)
	if l.URL != "viber://pa?chatURI=go24bot&context="+l.Code {
		t.Fatalf("the link: %+v", l)
	}

	// a call not signed with the token is refused
	h, body := signed("bad", map[string]any{"event": "message", "sender": map[string]any{"id": "u1", "name": "Анна"},
		"message": map[string]any{"type": "text", "text": l.Code}})
	if _, err := s.Hook(ctx, "viber", h, body); !errors.Is(err, ErrHookSign) {
		t.Fatalf("an unsigned call: %v", err)
	}
	// «webhook»: Viber checks the address
	h, body = signed("good", map[string]any{"event": "webhook"})
	if reply, err := s.Hook(ctx, "viber", h, body); err != nil || reply != nil {
		t.Fatalf("the address checked: %s %v", reply, err)
	}
	// the chat opened by the link: the answer is a button that sends the code; the chat is not linked yet
	h, body = signed("good", map[string]any{"event": "conversation_started", "type": "open", "context": l.Code,
		"user": map[string]any{"id": "u1", "name": "Анна"}, "subscribed": false})
	reply, err := s.Hook(ctx, "viber", h, body)
	must(err)
	var welcome struct {
		Text     string
		Sender   struct{ Name string }
		Keyboard struct {
			Buttons []struct{ ActionType, ActionBody, Text string }
		}
	}
	must(json.Unmarshal(reply, &welcome))
	if welcome.Text != "notifyout.bot_press" || welcome.Sender.Name != "go24 уведомления" || len(welcome.Keyboard.Buttons) != 1 ||
		welcome.Keyboard.Buttons[0].ActionBody != l.Code || welcome.Keyboard.Buttons[0].Text != "notifyout.link" {
		t.Fatalf("the welcome: %s", reply)
	}
	// opened without a code, or with an old one
	h, body = signed("good", map[string]any{"event": "conversation_started", "user": map[string]any{"id": "u2"}})
	if reply, _ := s.Hook(ctx, "viber", h, body); !strings.Contains(string(reply), "notifyout.bot_hello") {
		t.Fatalf("no code: %s", reply)
	}
	h, body = signed("good", map[string]any{"event": "conversation_started", "context": "nope", "user": map[string]any{"id": "u2"}})
	if reply, _ := s.Hook(ctx, "viber", h, body); !strings.Contains(string(reply), "notifyout.bot_bad_code") {
		t.Fatalf("a wrong code: %s", reply)
	}
	if links, _ := s.Links(ctx, anna); links[0].Linked {
		t.Fatal("linked by opening the chat")
	}
	// the button pressed: the code is a message, the chat is linked
	h, body = signed("good", map[string]any{"event": "message", "sender": map[string]any{"id": "u1", "name": "Анна"},
		"message": map[string]any{"type": "text", "text": l.Code}})
	reply, err = s.Hook(ctx, "viber", h, body)
	must(err)
	if reply != nil {
		t.Fatalf("a message answered in the call: %s", reply)
	}
	if got := fv.took(); !slices.Equal(got, []string{"u1: notifyout.bot_linked Анна [go24 уведомления]"}) {
		t.Fatalf("the bot's answer: %q", got)
	}
	links, err := s.Links(ctx, anna)
	must(err)
	if len(links) != 1 || !links[0].Linked || links[0].Account != "Анна" {
		t.Fatalf("linked: %+v", links)
	}
	var c string
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, 'comment', '') RETURNING id::text`, anna).Scan(&c))
	s.Pass(ctx)
	if got := fv.took(); !slices.Equal(got, []string{"u1: уведомление comment\nhttps://go24.example/notifications/" + c + " [go24 уведомления]"}) {
		t.Fatalf("delivered: %q", got)
	}

	// unsubscribed: unlinked (the bot's answer is refused by Viber, as the person is gone)
	h, body = signed("good", map[string]any{"event": "unsubscribed", "user_id": "u1"})
	_, err = s.Hook(ctx, "viber", h, body)
	must(err)
	if links, _ = s.Links(ctx, anna); links[0].Linked {
		t.Fatal("unsubscribing kept the link")
	}
	// the token forgotten: the webhook too
	must(s.SaveBot(ctx, "viber", false, "", "", true, anna))
	if fv.hook != "" {
		t.Fatalf("the webhook kept: %q", fv.hook)
	}
}
