package openlines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestMax: a line's MAX bot is connected by its token (checked with MAX, one line per bot); a client's messages to it —
// a text, a photo, a shared contact — become the line's dialogue (the client's name, the phone, the CRM lead); the
// bot's opening gets the greeting; the operator's answer, a file and the chat bot's buttons go back to the chat; the
// client's other dialogues are found by their MAX user; disconnected — nothing more goes, nothing is read.
func TestMax(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля <Ш>', 'op@t') RETURNING id`).Scan(&op))

	// the bot API: the bot «shop_bot» (token good) with a client's text, photo and contact, a group's message and
	// another client opening the bot
	var mu sync.Mutex
	var sent []map[string]any // what the bot wrote: chat_id and the body
	var api *httptest.Server
	updates := func() string {
		return `{"updates":[
		{"update_type":"message_created","message":{"sender":{"user_id":42,"first_name":"Иван","last_name":"Петров","username":"ivanp"},"recipient":{"chat_id":555,"chat_type":"dialog"},"body":{"text":"Есть доставка?"}}},
		{"update_type":"message_created","message":{"sender":{"user_id":42,"first_name":"Иван"},"recipient":{"chat_id":555,"chat_type":"dialog"},"body":{"text":"Вот товар","attachments":[{"type":"image","payload":{"photo_id":1,"url":"` + api.URL + `/img/1.jpg"}}]}}},
		{"update_type":"message_created","message":{"sender":{"user_id":42,"first_name":"Иван"},"recipient":{"chat_id":555,"chat_type":"dialog"},"body":{"attachments":[{"type":"contact","payload":{"vcf_info":"BEGIN:VCARD\r\nVERSION:3.0\r\nTEL;TYPE=cell:+79001234567\r\nEND:VCARD"}}]}}},
		{"update_type":"message_created","message":{"sender":{"user_id":9,"first_name":"Группа"},"recipient":{"chat_id":-100,"chat_type":"chat"},"body":{"text":"не сюда"}}},
		{"update_type":"bot_started","chat_id":777,"user":{"user_id":43,"first_name":"Мария"},"payload":""}],"marker":31}`
	}
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/img/1.jpg":
			_, _ = w.Write([]byte("JPEGDATA"))
			return
		case "/upload-here":
			must(r.ParseMultipartForm(1 << 20))
			f, h, err := r.FormFile("data")
			must(err)
			raw, _ := io.ReadAll(f)
			if h.Filename != "bill.pdf" || string(raw) != "%PDF" {
				t.Errorf("uploaded %s %q", h.Filename, raw)
			}
			_, _ = w.Write([]byte(`{"fileId":5,"token":"filetok"}`))
			return
		}
		token := r.Header.Get("Authorization")
		if token != "good" && token != "other" || r.URL.Query().Get("access_token") != "" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"code":"verify.token","message":"Invalid access_token"}`))
			return
		}
		switch r.URL.Path {
		case "/me":
			_, _ = w.Write([]byte(map[string]string{"good": `{"user_id":1,"username":"shop_bot","is_bot":true}`, "other": `{"username":"other_bot"}`}[token]))
		case "/updates":
			if r.URL.Query().Get("marker") == "31" {
				_, _ = w.Write([]byte(`{"updates":[],"marker":31}`))
				return
			}
			_, _ = w.Write([]byte(updates()))
		case "/uploads":
			if r.URL.Query().Get("type") != "file" {
				t.Errorf("upload type %s", r.URL.Query().Get("type"))
			}
			_, _ = w.Write([]byte(`{"url":"` + api.URL + `/upload-here"}`))
		case "/messages":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["chat_id"] = r.URL.Query().Get("chat_id")
			sent = append(sent, body)
			_, _ = w.Write([]byte(`{"message":{}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.MaxAPI, s.Key = api.URL, []byte("test secret")
	s.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		files[key], _ = io.ReadAll(r)
		return nil
	}
	s.OpenFile = func(_ context.Context, key string) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(files[key])), nil
	}

	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Greeting: "Здравствуйте! Чем помочь?", Operators: []string{op}, Active: true,
		WelcomeText: "Оператор скоро ответит", CRMAuto: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другая", Active: true})
	must(err)
	var ve *i18n.Error
	if _, err := s.ConnectMax(ctx, wid, "bad"); !errors.As(err, &ve) || ve.Key != "err.ol_max_token" {
		t.Fatalf("a bad token: %v", err)
	}
	name, err := s.ConnectMax(ctx, wid, " good ")
	must(err)
	if name != "shop_bot" {
		t.Fatalf("bot = %q", name)
	}
	if _, err := s.ConnectMax(ctx, other, "good"); !errors.As(err, &ve) || ve.Key != "err.ol_tg_taken" {
		t.Fatalf("one bot, two lines: %v", err)
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT max_secret FROM ol_widgets WHERE id = $1`, wid).Scan(&sealed))
	if bytes.Contains(sealed, []byte("good")) {
		t.Fatal("the token is kept as it is")
	}

	n, err := s.PollMax(ctx, 0)
	must(err)
	if n != 1 {
		t.Fatalf("bots = %d", n)
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "max" || list[0].Phone != "+79001234567" || list[0].LeadID == "" {
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
		!visitor[1].Image() || visitor[2].Body != "📞 +79001234567" {
		t.Fatalf("messages = %+v", msgs)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the photo = %q", files[f.Key])
	}
	var next int64
	must(pool.QueryRow(ctx, `SELECT max_next FROM ol_widgets WHERE id = $1`, wid).Scan(&next))
	if next != 31 {
		t.Fatalf("max_next = %d", next)
	}
	if n, err := s.PollMax(ctx, 0); err != nil || n != 1 {
		t.Fatalf("read again: %d %v", n, err)
	}
	if list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}}); len(list) != 1 {
		t.Fatalf("read twice: %d", len(list))
	}

	// back to MAX: the greeting (already), the welcome, the operator's answer and file, the chat bot's buttons
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	files["openlines/x/bill.pdf"] = []byte("%PDF")
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', ARRAY['Доставка', 'Оплата']) RETURNING 0`, x.ID).Scan(new(int)))
	got, err := s.SendMax(ctx)
	must(err)
	if got != 4 {
		t.Fatalf("sent %d: %+v", got, sent)
	}
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	want := []string{
		`{"chat_id":"777","text":"Здравствуйте! Чем помочь?"}`,
		`{"chat_id":"555","format":"html","text":"Оператор скоро ответит"}`,
		`{"chat_id":"555","format":"html","text":"<b>Оля &lt;Ш&gt;</b>\nДа, доставим завтра"}`,
		`{"attachments":[{"payload":{"token":"filetok"},"type":"file"}],"chat_id":"555","format":"html","text":"<b>Оля &lt;Ш&gt;</b>"}`,
		`{"attachments":[{"payload":{"buttons":[[{"text":"Доставка","type":"message"}],[{"text":"Оплата","type":"message"}]]},"type":"inline_keyboard"}],"chat_id":"555","format":"html","text":"Выберите"}`,
	}
	if len(sent) != len(want) {
		t.Fatalf("sent = %+v", sent)
	}
	for i := range want {
		var w any
		must(json.Unmarshal([]byte(want[i]), &w))
		if g := js(sent[i]); g != js(w) {
			t.Fatalf("sent[%d] = %s\nwant %s", i, g, want[i])
		}
	}
	if got, _ := s.SendMax(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// «Другие обращения клиента»: a dialogue of the same MAX user in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, max_chat, max_user) VALUES ($1, 'h2', 'max', 1, '42')`, other); err != nil {
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

	must(s.DisconnectMax(ctx, wid))
	if b, _ := s.Max(ctx, wid); b.Name != "" {
		t.Fatal("disconnected")
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendMax(ctx); got != 0 {
		t.Fatal("sent without the bot")
	}
	if n, _ := s.PollMax(ctx, 0); n != 0 {
		t.Fatal("polled without the bot")
	}
}
