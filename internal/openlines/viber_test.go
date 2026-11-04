package openlines

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestViber: a line's Viber bot is connected by its token (checked with Viber, the webhook set to go24's address, one
// line per bot; Viber not reaching go24 — not connected); Viber's calls are taken only signed with the token; a client
// opening the chat gets the greeting as the answer; their messages — a text, a picture, a contact — become the line's
// dialogue (the client's name from Viber, the CRM lead); the operator's answer, a file (by its link in go24), a picture
// and the chat bot's buttons go back to the chat; a client who unsubscribed is not written again and again; the
// client's other dialogues are found by their Viber user; disconnected — the webhook removed, nothing more goes.
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
	var boss, op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля', 'op@t') RETURNING id`).Scan(&op))

	// the API: the bot «Магазин» (token good); its webhook is good only at https://go24.example; the client «gone» has
	// unsubscribed
	var mu sync.Mutex
	var sent []map[string]any // send_message
	var hooks []string
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/img/1.jpg" {
			_, _ = w.Write([]byte("JPEGDATA"))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.Header.Get("X-Viber-Auth-Token") != "good" {
			_, _ = w.Write([]byte(`{"status":2,"status_message":"invalidAuthToken"}`))
			return
		}
		switch r.URL.Path {
		case "/get_account_info":
			_, _ = w.Write([]byte(`{"status":0,"id":"pa:1","name":"Магазин","uri":"shopbot"}`))
		case "/set_webhook":
			u, _ := body["url"].(string)
			hooks = append(hooks, u)
			if u != "" && !strings.HasPrefix(u, "https://go24.example") {
				_, _ = w.Write([]byte(`{"status":1,"status_message":"Result[HttpRequest failed]"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":0}`))
		case "/send_message":
			sent = append(sent, body)
			if body["receiver"] == "gone" {
				_, _ = w.Write([]byte(`{"status":6,"status_message":"notSubscribed"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":0,"message_token":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.ViberAPI, s.Key = api.URL, []byte("test secret")
	s.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		files[key], _ = io.ReadAll(r)
		return nil
	}

	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Greeting: "Здравствуйте! Чем помочь?", Operators: []string{op}, Active: true,
		WelcomeText: "Оператор скоро ответит", CRMAuto: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другая", Active: true})
	must(err)
	var ve *i18n.Error
	if _, err := s.ConnectViber(ctx, wid, "bad", "https://go24.example"); !errors.As(err, &ve) || ve.Key != "err.ol_viber_token" {
		t.Fatalf("a bad token: %v", err)
	}
	if _, err := s.ConnectViber(ctx, wid, "good", "http://192.168.0.1:8080"); !errors.As(err, &ve) || ve.Key != "err.ol_viber_hook" {
		t.Fatalf("not reachable: %v", err)
	}
	if b, _ := s.Viber(ctx, wid); b.URI != "" {
		t.Fatalf("connected without the webhook: %+v", b)
	}
	name, err := s.ConnectViber(ctx, wid, " good ", "https://go24.example/")
	must(err)
	if b, _ := s.Viber(ctx, wid); name != "Магазин" || b.URI != "shopbot" {
		t.Fatalf("bot = %q %+v", name, b)
	}
	if last := hooks[len(hooks)-1]; last != "https://go24.example/hooks/ol/viber/"+wid {
		t.Fatalf("webhook = %q", last)
	}
	if _, err := s.ConnectViber(ctx, other, "good", "https://go24.example"); !errors.As(err, &ve) || ve.Key != "err.ol_tg_taken" {
		t.Fatalf("one bot, two lines: %v", err)
	}

	call := func(token, body string) ([]byte, error) {
		m := hmac.New(sha256.New, []byte(token))
		m.Write([]byte(body))
		return s.ViberHook(ctx, wid, hex.EncodeToString(m.Sum(nil)), []byte(body))
	}
	if _, err := call("bad", `{"event":"message","sender":{"id":"u1"},"message":{"type":"text","text":"x"}}`); !errors.Is(err, ErrViberSign) {
		t.Fatalf("not signed: %v", err)
	}
	if _, err := s.ViberHook(ctx, other, "00", []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a line without the bot: %v", err)
	}
	if reply, err := call("good", `{"event":"webhook","timestamp":1}`); err != nil || reply != nil {
		t.Fatalf("webhook check: %s %v", reply, err)
	}
	reply, err := call("good", `{"event":"conversation_started","user":{"id":"u2","name":"Пётр"},"subscribed":false}`)
	must(err)
	if string(reply) != `{"sender":{"name":"Магазин"},"text":"Здравствуйте! Чем помочь?","type":"text"}` {
		t.Fatalf("greeting = %s", reply)
	}
	for _, body := range []string{
		`{"event":"message","sender":{"id":"u1","name":"Иван Петров"},"message":{"type":"text","text":"Есть доставка?"}}`,
		`{"event":"message","sender":{"id":"u1","name":"Иван Петров"},"message":{"type":"picture","text":"Вот товар","media":"` + api.URL + `/img/1.jpg","size":8}}`,
		`{"event":"message","sender":{"id":"u1","name":"Иван Петров"},"message":{"type":"contact","contact":{"name":"Иван","phone_number":"+79001234567"}}}`,
		`{"event":"unsubscribed","user_id":"u3"}`,
	} {
		_, err := call("good", body)
		must(err)
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "viber" || list[0].LeadID == "" {
		t.Fatalf("sessions = %+v", list)
	}
	x, msgs, err := s.Open(ctx, op, list[0].ID)
	must(err)
	var visitor []Message
	for _, m := range msgs {
		if m.AuthorID == "" && m.Kind == "" {
			visitor = append(visitor, m)
		}
	}
	if len(visitor) != 3 || visitor[0].Body != "Есть доставка?" || visitor[1].Body != "Вот товар" || visitor[1].FileName != "photo.jpg" ||
		!visitor[1].Image() || visitor[2].Body != "👤 Иван +79001234567" {
		t.Fatalf("messages = %+v", visitor)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the picture = %q", files[f.Key])
	}

	// back to Viber: the welcome, the operator's answer, a file and a picture by their links, the chat bot's buttons
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(s.ReplyWith(ctx, op, x.ID, "Вот так", &File{Key: "openlines/x/p.png", Name: "p.png", Type: "image/png", Size: 3}))
	must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', ARRAY['Доставка', 'Оплата']) RETURNING 0`, x.ID).Scan(new(int)))
	got, err := s.SendViber(ctx)
	must(err)
	if got != 5 || len(sent) != 6 { // a file goes after the operator's name: Viber gives it no text
		t.Fatalf("sent %d: %+v", got, sent)
	}
	_, msgs, _ = s.Open(ctx, op, x.ID)
	var bill, pic Message
	for _, m := range msgs {
		switch m.FileName {
		case "bill.pdf":
			bill = m
		case "p.png":
			pic = m
		}
	}
	want := []map[string]any{
		{"type": "text", "text": "Оператор скоро ответит"},
		{"type": "text", "text": "Оля:\nДа, доставим завтра"},
		{"type": "text", "text": "Оля:"},
		{"type": "file", "media": FileLink("https://go24.example", wid, bill), "file_name": "bill.pdf", "size": 4.0},
		{"type": "picture", "media": FileLink("https://go24.example", wid, pic), "text": "Оля:\nВот так"},
		{"type": "text", "text": "Выберите", "min_api_version": 3.0},
	}
	for i := range want {
		if sent[i]["receiver"] != "u1" || sent[i]["sender"].(map[string]any)["name"] != "Магазин" {
			t.Fatalf("sent[%d] = %+v", i, sent[i])
		}
		for k, v := range want[i] {
			if sent[i][k] != v {
				t.Fatalf("sent[%d] %s = %v, want %v", i, k, sent[i][k], v)
			}
		}
	}
	if kb, _ := json.Marshal(sent[5]["keyboard"]); string(kb) != `{"Buttons":[{"ActionBody":"Доставка","ActionType":"reply","Columns":6,"Rows":1,"Text":"Доставка"},{"ActionBody":"Оплата","ActionType":"reply","Columns":6,"Rows":1,"Text":"Оплата"}],"DefaultHeight":false,"Type":"keyboard"}` {
		t.Fatalf("keyboard = %s", kb)
	}
	if got, _ := s.SendViber(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// a client who unsubscribed: the message is not sent, never will be — the dialogue does not stop
	_, err = call("good", `{"event":"message","sender":{"id":"gone","name":"Анна"},"message":{"type":"text","text":"Привет"}}`)
	must(err)
	got, err = s.SendViber(ctx)
	must(err)
	if got != 1 {
		t.Fatalf("to the unsubscribed: %d", got)
	}
	if got, _ := s.SendViber(ctx); got != 0 {
		t.Fatal("written again")
	}

	// «Другие обращения клиента»: a dialogue of the same Viber user in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, viber_user) VALUES ($1, 'h2', 'viber', 'u1')`, other); err != nil {
		t.Fatal(err)
	}
	var sid string
	must(pool.QueryRow(ctx, `SELECT id::text FROM ol_sessions WHERE token_hash = 'h2'`).Scan(&sid))
	if _, err := pool.Exec(ctx, `INSERT INTO ol_messages (session_id, body) VALUES ($1, 'раньше')`, sid); err != nil {
		t.Fatal(err)
	}
	past, _, err := s.ClientDialogues(ctx, Who{All: true}, x.ID)
	must(err)
	if len(past) != 1 || past[0].ID != sid {
		t.Fatalf("other dialogues = %+v", past)
	}

	must(s.DisconnectViber(ctx, wid))
	if b, _ := s.Viber(ctx, wid); b.URI != "" || hooks[len(hooks)-1] != "" {
		t.Fatalf("disconnected: %+v %v", b, hooks)
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendViber(ctx); got != 0 {
		t.Fatal("sent without the bot")
	}
	if _, err := call("good", `{"event":"message","sender":{"id":"u1"},"message":{"type":"text","text":"x"}}`); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taken without the bot: %v", err)
	}
}
