package openlines

import (
	"context"
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

// TestOK: a line's OK group is connected by its bot token (checked with OK, the webhook subscribed at go24's address,
// one line per group; OK not reaching go24 — not connected); OK's calls are taken only at the address with the secret;
// the chat's start gets the greeting; the client's messages — a text, a picture, a contact, a button — become the line's
// dialogue (the client's name from OK, the CRM lead); the operator's answer, a file (its link), a picture (by its link
// in go24) and the chat bot's buttons go back to the chat; a chat that is no more is not written again and again; the
// client's other dialogues are found by their OK user; disconnected — the webhook removed, nothing more goes.
func TestOK(t *testing.T) {
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

	// the API: the group «Магазин» 57 (token good); its webhook is good only at https://go24.example; the chat «chat:gone»
	// is no more
	var mu sync.Mutex
	var sent []map[string]any // me/messages
	var hooks []string        // «+url» subscribed, «-url» unsubscribed
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
		if r.URL.Query().Get("access_token") != "good" {
			_, _ = w.Write([]byte(`{"error_code":102,"error_msg":"PARAM_SESSION_EXPIRED : Session expired"}`))
			return
		}
		u, _ := body["url"].(string)
		switch p := r.URL.Path; {
		case p == "/me/info" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"user_id":"user:5824","group_id":"group:57","name":"Магазин"}`))
		case p == "/me/subscribe":
			hooks = append(hooks, "+"+u)
			if !strings.HasPrefix(u, "https://go24.example") {
				_, _ = w.Write([]byte(`{"error_code":5000,"error_msg":"webhook url is not reachable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		case p == "/me/unsubscribe":
			hooks = append(hooks, "-"+u)
			_, _ = w.Write([]byte(`{"success":true}`))
		case strings.HasPrefix(p, "/me/messages/"):
			body["path"] = strings.TrimPrefix(p, "/me/messages/")
			sent = append(sent, body)
			if body["path"] == "chat:gone" {
				_, _ = w.Write([]byte(`{"error_code":300,"error_msg":"NOT_FOUND : chat not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true,"message_id":"mid.1"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.OKAPI, s.Key = api.URL, []byte("test secret")
	s.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		files[key], _ = io.ReadAll(r)
		return nil
	}

	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Greeting: "Здравствуйте! Чем помочь?", Operators: []string{op}, Active: true,
		WelcomeText: "Оператор скоро ответит", CRMAuto: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другая", Active: true})
	must(err)
	var ie *i18n.Error
	if _, err := s.ConnectOK(ctx, wid, "bad", "", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_ok_token" {
		t.Fatalf("a bad token: %v", err)
	}
	if _, err := s.ConnectOK(ctx, wid, "good", "javascript:alert(1)", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_ok_link" {
		t.Fatalf("a bad link: %v", err)
	}
	if _, err := s.ConnectOK(ctx, wid, "good", "", "http://192.168.0.1:8080"); !errors.As(err, &ie) || ie.Key != "err.ol_ok_hook" {
		t.Fatalf("not reachable: %v", err)
	}
	if g, _ := s.OK(ctx, wid); g.ID != "" {
		t.Fatalf("connected without the webhook: %+v", g)
	}
	name, err := s.ConnectOK(ctx, wid, " good ", "", "https://go24.example/")
	must(err)
	if g, _ := s.OK(ctx, wid); name != "Магазин" || g.ID != "57" || g.Link != "https://ok.ru/group/57" {
		t.Fatalf("group = %q %+v", name, g)
	}
	path := s.OKHookPath(wid)
	if last := hooks[len(hooks)-1]; last != "+https://go24.example"+path || len(path) != len("/hooks/ol/ok/"+wid+"/")+32 {
		t.Fatalf("webhook = %q", last)
	}
	if _, err := s.ConnectOK(ctx, other, "good", "", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_tg_taken" {
		t.Fatalf("one group, two lines: %v", err)
	}

	secret := path[strings.LastIndex(path, "/")+1:]
	call := func(body string) {
		t.Helper()
		must(s.OKHook(ctx, wid, secret, []byte(body)))
	}
	if err := s.OKHook(ctx, wid, strings.Repeat("0", 32), []byte(`{"webhookType":"MESSAGE_CREATED","sender":{"user_id":"user:1"},"recipient":{"chat_id":"chat:1"},"message":{"text":"x"}}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a wrong secret: %v", err)
	}
	if err := s.OKHook(ctx, other, secret, []byte(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another line's address: %v", err)
	}
	call(`{"webhookType":"CHAT_STARTED","sender":{"user_id":"user:2","name":"Пётр"},"recipient":{"chat_id":"chat:2"}}`)
	if len(sent) != 1 || sent[0]["path"] != "chat:2" || sent[0]["message"].(map[string]any)["text"] != "Здравствуйте! Чем помочь?" {
		t.Fatalf("greeting = %+v", sent)
	}
	sent = nil
	for _, body := range []string{
		`{"webhookType":"MESSAGE_CREATED","sender":{"user_id":"user:1","name":"Иван Петров"},"recipient":{"chat_id":"chat:1"},"message":{"text":"Есть доставка?"}}`,
		`{"webhookType":"MESSAGE_CREATED","sender":{"user_id":"user:1","name":"Иван Петров"},"recipient":{"chat_id":"chat:1"},"message":{"text":"Вот товар","attachments":[{"type":"IMAGE","payload":{"url":"` + api.URL + `/img/1.jpg"}}]}}`,
		`{"webhookType":"MESSAGE_CREATED","sender":{"user_id":"user:1","name":"Иван Петров"},"recipient":{"chat_id":"chat:1"},"message":{"attachments":[{"type":"CONTACT","payload":{"name":"Иван","phone":"+79001234567"}}]}}`,
		`{"webhookType":"MESSAGE_CALLBACK","sender":{"user_id":"user:1","name":"Иван Петров"},"recipient":{"chat_id":"chat:1"},"callback":{"payload":"Оплата","callback_id":"cb1"}}`,
		`{"webhookType":"MESSAGE_EDITED","sender":{"user_id":"user:1"},"recipient":{"chat_id":"chat:1"},"message":{"text":"не то"}}`,
	} {
		call(body)
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "ok" || list[0].LeadID == "" {
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
	if len(visitor) != 4 || visitor[0].Body != "Есть доставка?" || visitor[1].Body != "Вот товар" || visitor[1].FileName != "photo.jpg" ||
		!visitor[1].Image() || visitor[2].Body != "👤 Иван +79001234567" || visitor[3].Body != "Оплата" {
		t.Fatalf("messages = %+v", visitor)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the picture = %q", files[f.Key])
	}

	// back to OK: the welcome, the operator's answer, a file by its link, a picture, the chat bot's buttons
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(s.ReplyWith(ctx, op, x.ID, "Вот так", &File{Key: "openlines/x/p.png", Name: "p.png", Type: "image/png", Size: 3}))
	must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', ARRAY['Доставка', 'Оплата']) RETURNING 0`, x.ID).Scan(new(int)))
	got, err := s.SendOK(ctx)
	must(err)
	if got != 5 || len(sent) != 5 {
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
	want := []string{
		`{"text":"Оператор скоро ответит"}`,
		`{"text":"Оля:\nДа, доставим завтра"}`,
		`{"text":"Оля:\n📎 bill.pdf ` + FileLink("https://go24.example", wid, bill) + `"}`,
		`{"attachments":[{"payload":{"url":"` + FileLink("https://go24.example", wid, pic) + `"},"type":"IMAGE"}],"text":"Оля:\nВот так"}`,
		`{"attachments":[{"payload":{"keyboard":{"buttons":[[{"payload":"Доставка","text":"Доставка","type":"CALLBACK"}],[{"payload":"Оплата","text":"Оплата","type":"CALLBACK"}]]}},"type":"INLINE_KEYBOARD"}],"text":"Выберите"}`,
	}
	for i := range want {
		m, _ := json.Marshal(sent[i]["message"])
		if sent[i]["path"] != "chat:1" || sent[i]["recipient"].(map[string]any)["chat_id"] != "chat:1" || strings.ReplaceAll(string(m), `&`, "&") != want[i] {
			t.Fatalf("sent[%d] = %+v %s", i, sent[i], m)
		}
	}
	if got, _ := s.SendOK(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// a chat that is no more: the message is not sent, never will be — the dialogue does not stop
	call(`{"webhookType":"MESSAGE_CREATED","sender":{"user_id":"user:3","name":"Анна"},"recipient":{"chat_id":"chat:gone"},"message":{"text":"Привет"}}`)
	got, err = s.SendOK(ctx)
	must(err)
	if got != 1 {
		t.Fatalf("to the chat no more: %d", got)
	}
	if got, _ := s.SendOK(ctx); got != 0 {
		t.Fatal("written again")
	}

	// «Другие обращения клиента»: a dialogue of the same OK user in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, ok_user) VALUES ($1, 'h2', 'ok', 'user:1')`, other); err != nil {
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

	must(s.DisconnectOK(ctx, wid))
	if g, _ := s.OK(ctx, wid); g.ID != "" || hooks[len(hooks)-1] != "-https://go24.example"+path {
		t.Fatalf("disconnected: %+v %v", g, hooks)
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendOK(ctx); got != 0 {
		t.Fatal("sent without the group")
	}
	if err := s.OKHook(ctx, wid, secret, []byte(`{"webhookType":"MESSAGE_CREATED"}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taken without the group: %v", err)
	}
}
