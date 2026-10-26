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
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestTelegram: a line's bot is connected by its token (checked with Telegram, one line per bot); a client's
// messages to it — a text, a photo — become the line's dialogue (with the client's name and @name, the queue
// notified, the CRM lead); the operator's answer with a file and the line's messages go back to the chat; the chat's
// dialogue is not reachable through the widget.
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
	var boss, op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля <Ш>', 'op@t') RETURNING id`).Scan(&op))

	// the Bot API: the bot «shop_bot» (token 1:good) with two messages of a client and a /start of another one
	var mu sync.Mutex
	var sent []map[string]string // what the bot wrote: the method and its fields
	updates := `[{"update_id":10,"message":{"chat":{"id":555,"type":"private"},"from":{"first_name":"Иван","last_name":"Петров","username":"ivanp"},"text":"Есть доставка?"}},
		{"update_id":11,"message":{"chat":{"id":555,"type":"private"},"from":{"first_name":"Иван","username":"ivanp"},"caption":"Вот товар","photo":[{"file_id":"s","file_size":10},{"file_id":"big","file_size":20}]}},
		{"update_id":12,"message":{"chat":{"id":-100,"type":"group"},"from":{"first_name":"Группа"},"text":"не сюда"}},
		{"update_id":13,"message":{"chat":{"id":777,"type":"private"},"from":{"first_name":"Мария"},"text":"/start"}}]`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/file/bot1:good/photos/big.jpg" {
			_, _ = w.Write([]byte("JPEGDATA"))
			return
		}
		token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
		if token != "1:good" && token != "2:other" {
			http.Error(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, 401)
			return
		}
		args := map[string]any{}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			must(r.ParseMultipartForm(1 << 20))
			for k, v := range r.MultipartForm.Value {
				args[k] = v[0]
			}
			for k, v := range r.MultipartForm.File {
				f, _ := v[0].Open()
				raw, _ := io.ReadAll(f)
				args[k] = v[0].Filename + ":" + string(raw)
			}
		} else {
			_ = json.NewDecoder(r.Body).Decode(&args)
		}
		result := `true`
		switch method {
		case "getMe":
			result = map[string]string{"1:good": `{"username":"shop_bot"}`, "2:other": `{"username":"other_bot"}`}[token]
		case "getUpdates":
			result = `[]`
			if args["offset"].(float64) <= 10 {
				result = updates
			}
		case "getFile":
			result = `{"file_path":"photos/big.jpg","file_size":8}`
		case "sendMessage", "sendPhoto", "sendDocument":
			rec := map[string]string{"method": method}
			for k, v := range args {
				rec[k] = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(jsonText(v)), `"`), `"`)
			}
			sent = append(sent, rec)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":` + result + `}`))
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.TelegramAPI, s.Key = api.URL, []byte("test secret")
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
	if _, err := s.ConnectTelegram(ctx, wid, "1:bad"); !errors.As(err, &ve) {
		t.Fatalf("a bad token: %v", err)
	}
	name, err := s.ConnectTelegram(ctx, wid, " 1:good ")
	must(err)
	if name != "shop_bot" {
		t.Fatalf("bot = %q", name)
	}
	if _, err := s.ConnectTelegram(ctx, other, "1:good"); !errors.As(err, &ve) || ve.Key != "err.ol_tg_taken" {
		t.Fatalf("one bot, two lines: %v", err)
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT tg_secret FROM ol_widgets WHERE id = $1`, wid).Scan(&sealed))
	if bytes.Contains(sealed, []byte("1:good")) {
		t.Fatal("the token is kept as it is")
	}

	n, err := s.PollTelegram(ctx, 0)
	must(err)
	if n != 1 {
		t.Fatalf("bots = %d", n)
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "telegram" || list[0].Page != "https://t.me/ivanp" || list[0].LeadID == "" {
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
	if len(visitor) != 2 || visitor[0].Body != "Есть доставка?" || visitor[1].Body != "Вот товар" || visitor[1].FileName != "photo.jpg" || !visitor[1].Image() {
		t.Fatalf("messages = %+v", msgs)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the photo = %q", files[f.Key])
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'openline' AND user_id = $1`, op).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notified %d", notes)
	}
	var next int64
	must(pool.QueryRow(ctx, `SELECT tg_next FROM ol_widgets WHERE id = $1`, wid).Scan(&next))
	if next != 14 {
		t.Fatalf("tg_next = %d", next)
	}
	// the dialogue of a chat is not the widget's: its token is go24's
	if _, err := s.VisitorMessages(ctx, wid, "tg:"+wid+":555", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the chat through the widget: %v", err)
	}

	// back to Telegram: the greeting on /start (already), the welcome, the operator's answer and file
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	files["openlines/x/bill.pdf"] = []byte("%PDF")
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	got, err := s.SendTelegram(ctx)
	must(err)
	if got != 3 {
		t.Fatalf("sent %d: %+v", got, sent)
	}
	want := []map[string]string{
		{"method": "sendMessage", "chat_id": "777", "text": "Здравствуйте! Чем помочь?"},
		{"method": "sendMessage", "chat_id": "555", "text": "Оператор скоро ответит", "parse_mode": "HTML"},
		{"method": "sendMessage", "chat_id": "555", "text": "<b>Оля &lt;Ш&gt;</b>\nДа, доставим завтра", "parse_mode": "HTML"},
		{"method": "sendDocument", "chat_id": "555", "caption": "<b>Оля &lt;Ш&gt;</b>", "parse_mode": "HTML", "document": "bill.pdf:%PDF"},
	}
	if len(sent) != len(want) {
		t.Fatalf("sent = %+v", sent)
	}
	for i := range want {
		for k, v := range want[i] {
			if sent[i][k] != v {
				t.Fatalf("sent[%d][%s] = %q, want %q", i, k, sent[i][k], v)
			}
		}
	}
	if got, _ := s.SendTelegram(ctx); got != 0 {
		t.Fatal("sent twice")
	}
	// closed: the line's closing message goes too; disconnected — nothing more goes, nothing is read
	must(s.DisconnectTelegram(ctx, wid))
	if b, _ := s.Telegram(ctx, wid); b.Name != "" {
		t.Fatal("disconnected")
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendTelegram(ctx); got != 0 {
		t.Fatal("sent without the bot")
	}
	if n, _ := s.PollTelegram(ctx, 0); n != 0 {
		t.Fatal("polled without the bot")
	}
}

func jsonText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		b, _ := json.Marshal(int64(x))
		return string(b)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
