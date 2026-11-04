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
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

// fakeWhatsApp is the Graph API of one number «1065403522» with the token «good» and the app secret «sec»: the person
// «late» wrote more than 24 hours ago, the template is approved in English only.
type fakeWhatsApp struct {
	mu        sync.Mutex
	hook      string
	verify    string
	subscribe bool
	sent      []string
}

func (f *fakeWhatsApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	mac := hmac.New(sha256.New, []byte("sec"))
	mac.Write([]byte("good"))
	if r.Header.Get("Authorization") != "Bearer good" || r.URL.Query().Get("appsecret_proof") != hex.EncodeToString(mac.Sum(nil)) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid OAuth access token","code":190}}`)
		return
	}
	var body struct {
		To       string `json:"to"`
		Type     string `json:"type"`
		Text     struct{ Body string }
		Template struct {
			Name       string
			Language   struct{ Code string }
			Components []struct {
				Parameters []struct{ Text string }
			}
		}
		Hook *struct {
			URI    string `json:"override_callback_uri"`
			Verify string `json:"verify_token"`
		} `json:"webhook_configuration"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/1065403522":
		_, _ = io.WriteString(w, `{"display_phone_number":"+7 900 123-45-67","verified_name":"go24","id":"1065403522"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/1022901293/subscribed_apps":
		f.subscribe = true
		_, _ = io.WriteString(w, `{"success":true}`)
	case r.Method == http.MethodPost && r.URL.Path == "/1065403522" && body.Hook != nil:
		if !strings.HasPrefix(body.Hook.URI, "https://") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Callback verification failed","code":2200}}`)
			return
		}
		f.hook, f.verify = body.Hook.URI, body.Hook.Verify
		_, _ = io.WriteString(w, `{"success":true}`)
	case r.Method == http.MethodPost && r.URL.Path == "/1065403522/messages":
		switch {
		case body.Type == "text" && body.To == "late":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Re-engagement message","code":131047}}`)
			return
		case body.Type == "template" && body.Template.Language.Code != "en":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"Template name does not exist in the translation","code":132001}}`)
			return
		case body.Type == "template":
			f.sent = append(f.sent, body.To+" ["+body.Template.Name+"]: "+body.Template.Components[0].Parameters[0].Text)
		default:
			f.sent = append(f.sent, body.To+": "+body.Text.Body)
		}
		_, _ = io.WriteString(w, `{"messaging_product":"whatsapp","messages":[{"id":"wamid.1"}]}`)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeWhatsApp) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// waCall is a call of Meta to go24 with a person's message to the number, signed with the app secret.
func waCall(secret, phoneID, from, name, text string) (http.Header, []byte) {
	body, _ := json.Marshal(map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{"id": "1022901293",
		"changes": []any{map[string]any{"field": "messages", "value": map[string]any{"messaging_product": "whatsapp",
			"metadata": map[string]any{"display_phone_number": "79001234567", "phone_number_id": phoneID},
			"contacts": []any{map[string]any{"profile": map[string]any{"name": name}, "wa_id": from}},
			"messages": []any{map[string]any{"from": from, "id": "wamid.x", "type": "text", "text": map[string]any{"body": text}}},
		}}}}}})
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return http.Header{"X-Hub-Signature-256": {"sha256=" + hex.EncodeToString(mac.Sum(nil))}}, body
}

// TestWhatsApp: the number is set by its id, token, app secret and account (the parts checked with Meta, the
// number's webhook pointed at go24 on https only); Meta's check of the address is answered with the verify token
// only; the link wa.me opens the chat with the code, the code linked the chat; the notifications go there, as the
// template after 24 hours; a call not signed with the secret, or for another number, is not taken.
func TestWhatsApp(t *testing.T) {
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
	fw := &fakeWhatsApp{}
	srv := httptest.NewServer(fw)
	defer srv.Close()
	wa := &WhatsApp{API: srv.URL, HTTP: srv.Client()}
	s := New(pool, "go24 secret")
	s.Channels = []Channel{wa}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "http://go24.lan", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "уведомление " + it.Kind }
	s.Tr = func(key string, args ...any) string {
		if len(args) > 0 {
			return key + " " + args[0].(string)
		}
		return key
	}

	for _, bad := range []string{"good", "1065403522:good", "1065403522:good:wrong", "abc:good:sec", "1065403522:bad:sec"} {
		if err := s.SaveBot(ctx, "whatsapp", true, "", bad, false, anna); !errors.Is(err, errToken) {
			t.Fatalf("a wrong token %q: %v", bad, err)
		}
	}
	if err := s.SaveBot(ctx, "whatsapp", true, "", "1065403522:good:sec:1022901293", false, anna); !errors.Is(err, errHook) {
		t.Fatalf("go24 not on https: %v", err)
	}
	s.AppURL = "https://go24.example"
	must(s.SaveBot(ctx, "whatsapp", true, "", " 1065403522:good:sec:1022901293 ", false, anna))
	b, err := s.Bot(ctx, "whatsapp")
	must(err)
	addr, verify := s.HookSetup(b)
	if !fw.subscribe || fw.hook != "https://go24.example/hooks/notify/whatsapp" || addr != fw.hook || len(verify) != 32 || fw.verify != verify {
		t.Fatalf("the webhook: %v %q %q / %q %q", fw.subscribe, fw.hook, fw.verify, addr, verify)
	}
	if !b.Ready() || b.Name != "+7 900 123-45-67" {
		t.Fatalf("the bot: %+v", b)
	}

	// Meta checks the address with the verify token
	q := url.Values{"hub.mode": {"subscribe"}, "hub.verify_token": {verify}, "hub.challenge": {"1158201444"}}
	if got, err := s.Challenge(ctx, "whatsapp", q); err != nil || got != "1158201444" {
		t.Fatalf("the check: %q %v", got, err)
	}
	q.Set("hub.verify_token", "nope")
	if _, err := s.Challenge(ctx, "whatsapp", q); !errors.Is(err, ErrHookSign) {
		t.Fatalf("a wrong verify token: %v", err)
	}

	l, err := s.StartLink(ctx, anna, "whatsapp")
	must(err)
	if l.URL != "https://wa.me/79001234567?text="+l.Code {
		t.Fatalf("the link: %+v", l)
	}
	// not signed with the app secret; another number of the application
	h, body := waCall("other", "1065403522", "79005556677", "Анна", l.Code)
	if _, err := s.Hook(ctx, "whatsapp", h, body); !errors.Is(err, ErrHookSign) {
		t.Fatalf("an unsigned call: %v", err)
	}
	h, body = waCall("sec", "999999999", "79005556677", "Анна", l.Code)
	must2(t)(s.Hook(ctx, "whatsapp", h, body))
	if links, _ := s.Links(ctx, anna); links[0].Linked || len(fw.took()) != 0 {
		t.Fatal("another number's message taken")
	}
	// the code sent: linked
	h, body = waCall("sec", "1065403522", "79005556677", "Анна", l.Code)
	must2(t)(s.Hook(ctx, "whatsapp", h, body))
	if got := fw.took(); !slices.Equal(got, []string{"79005556677: notifyout.bot_linked Анна"}) {
		t.Fatalf("the answer: %q", got)
	}
	links, err := s.Links(ctx, anna)
	must(err)
	if len(links) != 1 || !links[0].Linked || links[0].Account != "Анна" {
		t.Fatalf("linked: %+v", links)
	}
	var c string
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, 'comment', '') RETURNING id::text`, anna).Scan(&c))
	s.Pass(ctx)
	if got := fw.took(); !slices.Equal(got, []string{"79005556677: уведомление comment\nhttps://go24.example/notifications/" + c}) {
		t.Fatalf("delivered: %q", got)
	}
	// more than 24 hours since the person wrote: the template, its language found, the line breaks gone
	must(wa.Send(ctx, b, "late", Message{Text: "уведомление  comment", Link: "https://go24.example/n/1"}))
	if got := fw.took(); !slices.Equal(got, []string{"late [go24_notification]: уведомление comment https://go24.example/n/1"}) {
		t.Fatalf("the template: %q", got)
	}

	// /stop unlinks
	h, body = waCall("sec", "1065403522", "79005556677", "Анна", "/stop")
	must2(t)(s.Hook(ctx, "whatsapp", h, body))
	if links, _ = s.Links(ctx, anna); links[0].Linked {
		t.Fatal("/stop kept the link")
	}
	// the token forgotten: the address is not answered any more
	must(s.SaveBot(ctx, "whatsapp", false, "", "", true, anna))
	if _, err := s.Challenge(ctx, "whatsapp", url.Values{"hub.mode": {"subscribe"}, "hub.verify_token": {verify}}); !errors.Is(err, ErrHookSign) {
		t.Fatalf("checked without a token: %v", err)
	}
}

// must2 fails the test on the error of a call with an answer.
func must2(t *testing.T) func([]byte, error) {
	return func(_ []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
}
