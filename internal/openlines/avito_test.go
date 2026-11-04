package openlines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestAvito: a line's Avito account is connected by its application's keys (checked with Avito, one line per account);
// the buyer's new messages of a chat — a text, a photo, a voice message — become the line's dialogue with the ad the
// buyer asks about (the buyer's name from Avito, the CRM lead), the chat is marked read; chats older than the
// connection, the company's own messages and messages taken before are not taken again; an expired access token is got
// anew; the operator's answer, a file (its link), a picture (uploaded to Avito) and the chat bot's menu go back to the
// chat; a closed chat is not written again and again; the client's other dialogues are found by their Avito user;
// disconnected — nothing more is taken or goes.
func TestAvito(t *testing.T) {
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

	// the API: the application «app»/«good» of the account «Магазин» 77; an access token is good till the next one is
	// got; chats and their messages as the test sets them; the chat «u2i-gone» is closed
	var mu sync.Mutex
	issued, valid := 0, ""
	chats := `{"chats":[]}`
	messages := map[string]string{}
	var asked, reads []string // the chats whose messages were asked for, marked read
	var sent []string         // «chat text» or «chat image:id»
	var uploaded string
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		switch p {
		case "/token":
			if r.FormValue("grant_type") != "client_credentials" || r.FormValue("client_id") != "app" || r.FormValue("client_secret") != "good" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
				return
			}
			issued++
			valid = "T" + strconv.Itoa(issued)
			_, _ = w.Write([]byte(`{"access_token":"` + valid + `","expires_in":86400,"token_type":"Bearer"}`))
			return
		case "/f/photo":
			_, _ = w.Write([]byte("PHOTO"))
			return
		case "/f/voice":
			_, _ = w.Write([]byte("VOICE"))
			return
		}
		if valid == "" || r.Header.Get("Authorization") != "Bearer "+valid {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const v1 = "/messenger/v1/accounts/77"
		switch {
		case p == "/core/v1/accounts/self":
			_, _ = w.Write([]byte(`{"id":77,"name":"Магазин","profile_url":"https://www.avito.ru/user/abc/profile"}`))
		case p == "/messenger/v2/accounts/77/chats":
			_, _ = w.Write([]byte(chats))
		case strings.HasPrefix(p, "/messenger/v3/accounts/77/chats/") && strings.HasSuffix(p, "/messages/"):
			c := strings.TrimSuffix(strings.TrimPrefix(p, "/messenger/v3/accounts/77/chats/"), "/messages/")
			asked = append(asked, c)
			_, _ = w.Write([]byte(messages[c]))
		case p == v1+"/getVoiceFiles" && r.URL.Query().Get("voice_ids") == "v1":
			_, _ = w.Write([]byte(`{"voices_urls":{"v1":"` + api.URL + `/f/voice"}}`))
		case p == v1+"/uploadImages":
			f, h, err := r.FormFile("uploadfile[]")
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(f)
			uploaded = h.Filename + " " + string(data)
			_, _ = w.Write([]byte(`{"img.1":{"1280x960":"https://avito/img.1"}}`))
		case strings.HasPrefix(p, v1+"/chats/"):
			rest := strings.TrimPrefix(p, v1+"/chats/")
			c, what, _ := strings.Cut(rest, "/")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			switch what {
			case "read":
				reads = append(reads, c)
			case "messages":
				if c == "u2i-gone" {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"error":{"code":403,"message":"chat is blocked"}}`))
					return
				}
				sent = append(sent, c+" "+body["message"].(map[string]any)["text"].(string))
			case "messages/image":
				sent = append(sent, c+" image:"+body["image_id"].(string))
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()

	files := map[string][]byte{"openlines/x/p.png": []byte("PNG")}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.AvitoAPI, s.Key = api.URL, []byte("test secret")
	s.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		files[key], _ = io.ReadAll(r)
		return nil
	}
	s.OpenFile = func(_ context.Context, key string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(files[key]))), nil
	}

	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Operators: []string{op}, Active: true, WelcomeText: "Оператор скоро ответит", CRMAuto: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другая", Active: true})
	must(err)
	var ie *i18n.Error
	if _, err := s.ConnectAvito(ctx, wid, "app", "bad", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_avito_key" {
		t.Fatalf("bad keys: %v", err)
	}
	if _, err := s.ConnectAvito(ctx, wid, "", "good", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_avito_key" {
		t.Fatalf("no client id: %v", err)
	}
	name, err := s.ConnectAvito(ctx, wid, " app ", " good ", "https://go24.example/")
	must(err)
	if a, _ := s.Avito(ctx, wid); name != "Магазин" || a.ID != 77 || a.Link != "https://www.avito.ru/user/abc/profile" {
		t.Fatalf("account = %q %+v", name, a)
	}
	if _, err := s.ConnectAvito(ctx, other, "app", "good", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_tg_taken" {
		t.Fatalf("one account, two lines: %v", err)
	}
	var since int64
	must(pool.QueryRow(ctx, `SELECT avito_since FROM ol_widgets WHERE id::text = $1`, wid).Scan(&since))
	at := func(d int64) string { return strconv.FormatInt(since+d, 10) }
	poll := func() {
		t.Helper()
		mu.Lock()
		asked = nil
		mu.Unlock()
		n, err := s.PollAvito(ctx)
		if err != nil || n != 1 {
			t.Fatalf("poll: %d %v", n, err)
		}
	}

	// the chat about the bicycle: three new messages of the buyer, the company's own one and one older than the
	// connection; a chat older than the connection and one whose last message is the company's are not looked into
	mu.Lock()
	chats = `{"chats":[
		{"id":"u2i-1","context":{"type":"item","value":{"id":1,"title":"Велосипед","url":"https://avito.ru/1"}},
		 "users":[{"id":77,"name":"Магазин"},{"id":5,"name":"Иван"}],"last_message":{"id":"m3","created":` + at(3) + `,"direction":"in"}},
		{"id":"u2i-old","users":[{"id":6,"name":"Старый"}],"last_message":{"id":"o1","created":` + at(-5) + `,"direction":"in"}},
		{"id":"u2i-out","users":[{"id":7,"name":"Пётр"}],"last_message":{"id":"x1","created":` + at(4) + `,"direction":"out"}}]}`
	messages["u2i-1"] = `{"messages":[
		{"id":"m3","author_id":5,"created":` + at(3) + `,"direction":"in","type":"voice","content":{"voice":{"voice_id":"v1"}}},
		{"id":"m2","author_id":5,"created":` + at(2) + `,"direction":"in","type":"image","content":{"image":{"sizes":{"140x105":"x","1280x960":"` + api.URL + `/f/photo"}}}},
		{"id":"k1","author_id":77,"created":` + at(2) + `,"direction":"out","type":"text","content":{"text":"наше"}},
		{"id":"m1","author_id":5,"created":` + at(1) + `,"direction":"in","type":"text","content":{"text":"Есть доставка?"}},
		{"id":"m0","author_id":5,"created":` + at(-100) + `,"direction":"in","type":"text","content":{"text":"давно"}}]}`
	mu.Unlock()
	poll()
	if strings.Join(asked, ",") != "u2i-1" || strings.Join(reads, ",") != "u2i-1" {
		t.Fatalf("asked %v, read %v", asked, reads)
	}
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван" || list[0].Channel != "avito" || list[0].LeadID == "" {
		t.Fatalf("sessions = %+v", list)
	}
	x, msgs, err := s.Open(ctx, op, list[0].ID)
	must(err)
	visitor := func() []Message {
		var out []Message
		for _, m := range msgs {
			if m.AuthorID == "" && m.Kind == "" {
				out = append(out, m)
			}
		}
		return out
	}
	v := visitor()
	if len(v) != 3 || v[0].Body != "🏷 Велосипед https://avito.ru/1\nЕсть доставка?" || v[1].FileName != "photo.jpg" || !v[1].Image() ||
		v[2].FileName != "voice.m4a" || v[2].FileType != "audio/mp4" {
		t.Fatalf("messages = %+v", v)
	}
	if f, _ := s.FileOf(ctx, v[1].ID); string(files[f.Key]) != "PHOTO" {
		t.Fatalf("the photo = %q", files[f.Key])
	}

	// nothing new: the chat is not asked again; an expired access token is got anew
	poll()
	mu.Lock()
	valid = ""
	mu.Unlock()
	poll()
	if len(asked) != 0 {
		t.Fatalf("asked again: %v", asked)
	}

	// the buyer writes again: the new message only, without the ad
	mu.Lock()
	chats = strings.Replace(chats, `"id":"m3","created":`+at(3), `"id":"m4","created":`+at(4), 1)
	messages["u2i-1"] = strings.Replace(messages["u2i-1"], `{"messages":[`, `{"messages":[{"id":"m4","author_id":5,"created":`+at(4)+
		`,"direction":"in","type":"text","content":{"text":"Ещё вопрос"}},`, 1)
	mu.Unlock()
	poll()
	_, msgs, _ = s.Open(ctx, op, x.ID)
	if v := visitor(); len(v) != 4 || v[3].Body != "Ещё вопрос" {
		t.Fatalf("messages = %+v", v)
	}

	// back to Avito: the welcome, the operator's answer, a file by its link, a picture, the chat bot's menu
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(s.ReplyWith(ctx, op, x.ID, "Вот так", &File{Key: "openlines/x/p.png", Name: "p.png", Type: "image/png", Size: 3}))
	must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', ARRAY['1. Доставка', '2. Оплата']) RETURNING 0`, x.ID).Scan(new(int)))
	got, err := s.SendAvito(ctx)
	must(err)
	_, msgs, _ = s.Open(ctx, op, x.ID)
	var bill Message
	for _, m := range msgs {
		if m.FileName == "bill.pdf" {
			bill = m
		}
	}
	want := []string{
		"u2i-1 Оператор скоро ответит",
		"u2i-1 Оля:\nДа, доставим завтра",
		"u2i-1 Оля:\n📎 bill.pdf " + FileLink("https://go24.example", wid, bill),
		"u2i-1 Оля:\nВот так",
		"u2i-1 image:img.1",
		"u2i-1 Выберите\n1. Доставка\n2. Оплата",
	}
	if got != 5 || strings.Join(sent, "|") != strings.Join(want, "|") || uploaded != "p.png PNG" {
		t.Fatalf("sent %d: %q, uploaded %q", got, sent, uploaded)
	}
	if got, _ := s.SendAvito(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// a closed chat: the message is not sent, never will be — the dialogue does not stop
	mu.Lock()
	chats = `{"chats":[{"id":"u2i-gone","users":[{"id":8,"name":"Анна"}],"last_message":{"id":"g1","created":` + at(9) + `,"direction":"in"}}]}`
	messages["u2i-gone"] = `[{"id":"g1","author_id":8,"created":` + at(9) + `,"direction":"in","type":"text","content":{"text":"Привет"}}]`
	mu.Unlock()
	poll()
	got, err = s.SendAvito(ctx)
	must(err)
	if got != 1 {
		t.Fatalf("to the closed chat: %d", got)
	}
	if got, _ := s.SendAvito(ctx); got != 0 {
		t.Fatal("written again")
	}

	// «Другие обращения клиента»: a dialogue of the same Avito user in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, avito_user) VALUES ($1, 'h2', 'avito', '5')`, other); err != nil {
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

	must(s.DisconnectAvito(ctx, wid))
	if a, _ := s.Avito(ctx, wid); a.ID != 0 || a.Name != "" {
		t.Fatalf("disconnected: %+v", a)
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendAvito(ctx); got != 0 {
		t.Fatal("sent without the account")
	}
	if n, err := s.PollAvito(ctx); n != 0 || err != nil {
		t.Fatalf("polled without the account: %d %v", n, err)
	}
}
