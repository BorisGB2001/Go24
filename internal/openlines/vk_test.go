package openlines

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestVK: a line's VK community is connected by its access key (checked with VK, the long poll switched on, one line
// per community); a client's messages to it — a text, a photo, a voice message — become the line's dialogue (the
// client's name from VK, the CRM lead); the «Start» button gets the greeting; the long poll's key expired — a new one,
// the events not lost; the operator's answer, a file and the chat bot's buttons go back to the chat; the client's other
// dialogues are found by their VK user; disconnected — nothing more goes, nothing is read.
func TestVK(t *testing.T) {
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

	// the API: the community «Магазин» (key good) whose long poll has a client's text, photo and voice message, the
	// «Start» of another client, a group chat's message and the community's own message; its key expires once
	var mu sync.Mutex
	var sent []url.Values // messages.send
	var calls []string
	lpKey := "k1"
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/img/1.jpg":
			_, _ = w.Write([]byte("JPEGDATA"))
			return
		case "/voice.mp3":
			_, _ = w.Write([]byte("MP3DATA"))
			return
		case "/lp":
			q := r.URL.Query()
			switch {
			case q.Get("key") != lpKey:
				_, _ = w.Write([]byte(`{"failed":2}`))
			case q.Get("ts") == "10":
				_, _ = w.Write([]byte(`{"ts":"14","updates":[
				{"type":"message_new","object":{"message":{"from_id":42,"peer_id":42,"text":"Есть доставка?"}}},
				{"type":"message_new","object":{"message":{"from_id":42,"peer_id":42,"text":"Вот товар","attachments":[{"type":"photo","photo":{"sizes":[{"type":"s","url":"` + api.URL + `/img/small.jpg","width":75,"height":50},{"type":"x","url":"` + api.URL + `/img/1.jpg","width":604,"height":403}]}}]}}},
				{"type":"message_new","object":{"message":{"from_id":42,"peer_id":42,"text":"","attachments":[{"type":"audio_message","audio_message":{"duration":3,"link_mp3":"` + api.URL + `/voice.mp3"}},{"type":"video","video":{"title":"Обзор"}}]}}},
				{"type":"message_new","object":{"message":{"from_id":43,"peer_id":43,"text":"Начать","payload":"{\"command\":\"start\"}"}}},
				{"type":"message_new","object":{"message":{"from_id":9,"peer_id":2000000001,"text":"не сюда"}}},
				{"type":"message_reply","object":{"from_id":-7,"peer_id":42,"text":"ответ"}}]}`))
			default:
				_, _ = w.Write([]byte(`{"ts":` + q.Get("ts") + `,"updates":[]}`))
			}
			return
		case "/up/doc":
			must(r.ParseMultipartForm(1 << 20))
			f, h, err := r.FormFile("file")
			must(err)
			raw, _ := io.ReadAll(f)
			if h.Filename != "bill.pdf" || string(raw) != "%PDF" {
				t.Errorf("uploaded %s %q", h.Filename, raw)
			}
			_, _ = w.Write([]byte(`{"file":"FILEDATA"}`))
			return
		}
		must(r.ParseForm())
		token := r.PostForm.Get("access_token")
		method := r.URL.Path[len("/method/"):]
		calls = append(calls, method)
		if token != "good" || r.PostForm.Get("v") != vkVersion {
			_, _ = w.Write([]byte(`{"error":{"error_code":5,"error_msg":"User authorization failed: invalid access_token (4)."}}`))
			return
		}
		switch method {
		case "groups.getById":
			_, _ = w.Write([]byte(`{"response":{"groups":[{"id":7,"name":"Магазин","screen_name":"shop"}],"profiles":[]}}`))
		case "groups.setLongPollSettings":
			if r.PostForm.Get("group_id") != "7" || r.PostForm.Get("message_new") != "1" {
				t.Errorf("long poll settings %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"response":1}`))
		case "groups.getLongPollServer":
			_, _ = w.Write([]byte(`{"response":{"key":"` + lpKey + `","server":"` + api.URL + `/lp","ts":"10"}}`))
		case "users.get":
			_, _ = w.Write([]byte(`{"response":[{"id":42,"first_name":"Иван","last_name":"Петров"}]}`))
		case "docs.getMessagesUploadServer":
			_, _ = w.Write([]byte(`{"response":{"upload_url":"` + api.URL + `/up/doc"}}`))
		case "docs.save":
			if r.PostForm.Get("file") != "FILEDATA" {
				t.Errorf("docs.save %v", r.PostForm)
			}
			_, _ = w.Write([]byte(`{"response":{"type":"doc","doc":{"id":5,"owner_id":42}}}`))
		case "messages.send":
			r.PostForm.Del("access_token")
			r.PostForm.Del("v")
			sent = append(sent, r.PostForm)
			_, _ = w.Write([]byte(`{"response":1}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.VKAPI, s.Key = api.URL, []byte("test secret")
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
	if _, err := s.ConnectVK(ctx, wid, "bad"); !errors.As(err, &ve) || ve.Key != "err.ol_vk_token" {
		t.Fatalf("a bad key: %v", err)
	}
	name, err := s.ConnectVK(ctx, wid, " good ")
	must(err)
	if g, _ := s.VK(ctx, wid); name != "Магазин" || g.Screen != "shop" {
		t.Fatalf("community = %q %+v", name, g)
	}
	if _, err := s.ConnectVK(ctx, other, "good"); !errors.As(err, &ve) || ve.Key != "err.ol_tg_taken" {
		t.Fatalf("one community, two lines: %v", err)
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT vk_secret FROM ol_widgets WHERE id = $1`, wid).Scan(&sealed))
	if bytes.Contains(sealed, []byte("good")) {
		t.Fatal("the key is kept as it is")
	}

	// the long poll's key expired: a new one, read from the same ts
	mu.Lock()
	lpKey = "k2"
	mu.Unlock()
	n, err := s.PollVK(ctx, 0)
	must(err)
	if n != 1 {
		t.Fatalf("communities = %d", n)
	}
	if list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}}); len(list) != 0 {
		t.Fatalf("read with an old key: %+v", list)
	}
	must2 := func() { _, err := s.PollVK(ctx, 0); must(err) }
	must2()
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "vk" || list[0].LeadID == "" {
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
		!visitor[1].Image() || visitor[2].Body != "📎 Обзор" || visitor[2].FileName != "voice.mp3" {
		t.Fatalf("messages = %+v", visitor)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the photo = %q", files[f.Key])
	}
	var key, ts string
	must(pool.QueryRow(ctx, `SELECT vk_key, vk_ts FROM ol_widgets WHERE id = $1`, wid).Scan(&key, &ts))
	if key != "k2" || ts != "14" {
		t.Fatalf("long poll = %s %s", key, ts)
	}
	must2()
	if list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}}); len(list) != 1 {
		t.Fatalf("read twice: %d", len(list))
	}
	if len(sent) != 1 || sent[0].Get("peer_id") != "43" || sent[0].Get("message") != "Здравствуйте! Чем помочь?" {
		t.Fatalf("greeting = %+v", sent)
	}

	// back to VK: the welcome, the operator's answer and file, the chat bot's buttons
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	files["openlines/x/bill.pdf"] = []byte("%PDF")
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', ARRAY['Доставка', 'Оплата']) RETURNING 0`, x.ID).Scan(new(int)))
	got, err := s.SendVK(ctx)
	must(err)
	if got != 4 {
		t.Fatalf("sent %d: %+v", got, sent)
	}
	sent = sent[1:]
	want := []map[string]string{
		{"peer_id": "42", "message": "Оператор скоро ответит"},
		{"peer_id": "42", "message": "Оля:\nДа, доставим завтра"},
		{"peer_id": "42", "message": "Оля:", "attachment": "doc42_5"},
		{"peer_id": "42", "message": "Выберите", "keyboard": `{"buttons":[[{"action":{"label":"Доставка","type":"text"}}],[{"action":{"label":"Оплата","type":"text"}}]],"inline":true,"one_time":false}`},
	}
	if len(sent) != len(want) {
		t.Fatalf("sent = %+v", sent)
	}
	ids := map[string]bool{}
	for i := range want {
		for k, v := range want[i] {
			if sent[i].Get(k) != v {
				t.Fatalf("sent[%d] %s = %q, want %q", i, k, sent[i].Get(k), v)
			}
		}
		ids[sent[i].Get("random_id")] = true
	}
	if len(ids) != 4 || ids["0"] {
		t.Fatalf("random ids = %v", ids)
	}
	if got, _ := s.SendVK(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// «Другие обращения клиента»: a dialogue of the same VK user in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, vk_peer, vk_user) VALUES ($1, 'h2', 'vk', 42, '42')`, other); err != nil {
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

	must(s.DisconnectVK(ctx, wid))
	if g, _ := s.VK(ctx, wid); g.Name != "" {
		t.Fatal("disconnected")
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendVK(ctx); got != 0 {
		t.Fatal("sent without the community")
	}
	if n, _ := s.PollVK(ctx, 0); n != 0 {
		t.Fatal("polled without the community")
	}
}
