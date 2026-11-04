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

// TestWhatsApp: a line's WhatsApp number is connected by its id, token and app secret (checked with Meta, the business
// account's webhook subscribed at go24's address, one line per number; Meta not reaching go24 — not connected); Meta's
// check of the address is answered only with the line's verify token, its calls taken only signed with the app secret;
// the client's messages — a text, a picture, a place, a contact, a button — become the line's dialogue (the client's
// name from WhatsApp, their number the dialogue's phone, the CRM lead); the operator's answer, a file, a picture (by
// their links in go24) and the chat bot's buttons (reply buttons, a list, lines) go back to the client; an answer after
// the 24 hours is not sent again and again, the dialogue says why; the client's other dialogues are found by their
// WhatsApp id; disconnected — the webhook removed, nothing more goes.
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
	var boss, op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля', 'op@t') RETURNING id`).Scan(&op))

	// the API: the number 106540352242922 «Магазин» of the business account 102290129340398 (token good); its webhook is good only at
	// https://go24.example; the client 70000000000 wrote more than 24 hours ago
	var mu sync.Mutex
	var sent []map[string]any // /106540352242922/messages
	var hooks []string        // «+url» subscribed, «-» removed
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid OAuth access token","type":"OAuthException","code":190}}`))
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch p := r.URL.Path; {
		case p == "/106540352242922" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"display_phone_number":"+7 900 123-45-67","verified_name":"Магазин","id":"106540352242922"}`))
		case p == "/102290129340398/subscribed_apps" && r.Method == http.MethodPost:
			u, _ := body["override_callback_uri"].(string)
			hooks = append(hooks, "+"+u)
			if !strings.HasPrefix(u, "https://go24.example") || len(body["verify_token"].(string)) != 32 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Callback verification failed","code":2200}}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":true}`))
		case p == "/102290129340398/subscribed_apps" && r.Method == http.MethodDelete:
			hooks = append(hooks, "-")
			_, _ = w.Write([]byte(`{"success":true}`))
		case p == "/media1":
			_, _ = w.Write([]byte(`{"url":"` + api.URL + `/dl/media1","mime_type":"image/jpeg","file_size":8,"id":"media1"}`))
		case p == "/dl/media1":
			_, _ = w.Write([]byte("JPEGDATA"))
		case p == "/106540352242922/messages":
			sent = append(sent, body)
			if body["to"] == "70000000000" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Re-engagement message","code":131047}}`))
				return
			}
			_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","messages":[{"id":"wamid.1"}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer api.Close()

	files := map[string][]byte{}
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	s.WhatsAppAPI, s.Key = api.URL, []byte("test secret")
	s.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		files[key], _ = io.ReadAll(r)
		return nil
	}

	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Operators: []string{op}, Active: true, WelcomeText: "Оператор скоро ответит", CRMAuto: true})
	must(err)
	other, err := s.SaveWidget(ctx, boss, Widget{Name: "Другая", Active: true})
	must(err)
	var ie *i18n.Error
	if _, err := s.ConnectWhatsApp(ctx, wid, "106540352242922", "", "bad", "appsecret", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_wa_token" {
		t.Fatalf("a bad token: %v", err)
	}
	if _, err := s.ConnectWhatsApp(ctx, wid, "../me", "", "good", "appsecret", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_wa_token" {
		t.Fatalf("a bad number id: %v", err)
	}
	if _, err := s.ConnectWhatsApp(ctx, wid, "106540352242922", "102290129340398", "good", "appsecret", "http://192.168.0.1:8080"); !errors.As(err, &ie) || ie.Key != "err.ol_wa_hook" {
		t.Fatalf("not reachable: %v", err)
	}
	if n, _ := s.WhatsApp(ctx, wid); n.Phone != "" || hooks[len(hooks)-1] != "-" {
		t.Fatalf("connected without the webhook: %+v %v", n, hooks)
	}
	name, err := s.ConnectWhatsApp(ctx, wid, " 106540352242922 ", "102290129340398", " good ", "appsecret", "https://go24.example/")
	must(err)
	n, _ := s.WhatsApp(ctx, wid)
	if name != "Магазин" || n.Phone != "106540352242922" || n.Number != "+7 900 123-45-67" || n.WABA != "102290129340398" || n.Hook != "https://go24.example/hooks/ol/whatsapp/"+wid || len(n.Verify) != 32 {
		t.Fatalf("number = %q %+v", name, n)
	}
	if last := hooks[len(hooks)-1]; last != "+"+n.Hook {
		t.Fatalf("webhook = %q", last)
	}
	if _, err := s.ConnectWhatsApp(ctx, other, "106540352242922", "", "good", "appsecret", "https://go24.example"); !errors.As(err, &ie) || ie.Key != "err.ol_tg_taken" {
		t.Fatalf("one number, two lines: %v", err)
	}

	// Meta's check of the address
	if c, err := s.WAVerify(ctx, wid, "subscribe", n.Verify, "1158201444"); err != nil || c != "1158201444" {
		t.Fatalf("verify = %q %v", c, err)
	}
	if _, err := s.WAVerify(ctx, wid, "subscribe", "wrong", "1"); !errors.Is(err, ErrWASign) {
		t.Fatalf("a wrong verify token: %v", err)
	}
	if _, err := s.WAVerify(ctx, other, "subscribe", n.Verify, "1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a line with no number: %v", err)
	}

	sign := func(body string) string {
		m := hmac.New(sha256.New, []byte("appsecret"))
		m.Write([]byte(body))
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	hook := func(from, name, msg string) string {
		return `{"object":"whatsapp_business_account","entry":[{"id":"102290129340398","changes":[{"field":"messages","value":{"messaging_product":"whatsapp",
			"metadata":{"display_phone_number":"79001234567","phone_number_id":"106540352242922"},"contacts":[{"profile":{"name":"` + name + `"},"wa_id":"` + from + `"}],
			"messages":[{"from":"` + from + `","id":"wamid.x","timestamp":"1700000000",` + msg + `}]}}]}]}`
	}
	call := func(body string) {
		t.Helper()
		must(s.WAHook(ctx, wid, sign(body), []byte(body)))
	}
	if err := s.WAHook(ctx, wid, "sha256=00", []byte(hook("79001234567", "x", `"type":"text","text":{"body":"x"}`))); !errors.Is(err, ErrWASign) {
		t.Fatalf("not signed: %v", err)
	}
	for _, msg := range []string{
		`"type":"text","text":{"body":"Есть доставка?"}`,
		`"type":"image","image":{"id":"media1","mime_type":"image/jpeg","caption":"Вот товар"}`,
		`"type":"location","location":{"latitude":55.75,"longitude":37.61,"name":"Склад","address":"ул. Ленина"}`,
		`"type":"contacts","contacts":[{"name":{"formatted_name":"Иван"},"phones":[{"phone":"+79001234567"}]}]`,
		`"type":"interactive","interactive":{"type":"button_reply","button_reply":{"id":"Оплата","title":"Оплата"}}`,
		`"type":"reaction","reaction":{"message_id":"wamid.y","emoji":"👍"}`,
	} {
		call(hook("79001234567", "Иван Петров", msg))
	}
	// statuses and another number's messages are not the line's
	call(`{"entry":[{"changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"106540352242922"},"statuses":[{"id":"wamid.1","status":"read"}]}}]}]}`)
	call(strings.Replace(hook("79005555555", "Чужой", `"type":"text","text":{"body":"не сюда"}`), `"phone_number_id":"106540352242922"`, `"phone_number_id":"999"`, 1))
	list, _ := s.Sessions(ctx, Filter{Who: Who{All: true}})
	if len(list) != 1 || list[0].Name != "Иван Петров" || list[0].Channel != "whatsapp" || list[0].Phone != "+79001234567" || list[0].LeadID == "" {
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
	if len(visitor) != 5 || visitor[0].Body != "Есть доставка?" || visitor[1].Body != "Вот товар" || visitor[1].FileName != "photo.jpg" ||
		!visitor[1].Image() || visitor[2].Body != "📍 Склад ул. Ленина\nhttps://maps.google.com/?q=55.750000,37.610000" ||
		visitor[3].Body != "👤 Иван +79001234567" || visitor[4].Body != "Оплата" {
		t.Fatalf("messages = %+v", visitor)
	}
	f, _ := s.FileOf(ctx, visitor[1].ID)
	if string(files[f.Key]) != "JPEGDATA" {
		t.Fatalf("the picture = %q", files[f.Key])
	}

	// back to WhatsApp: the welcome, the answer, a file and a picture with their captions, the chat bot's menus
	must(s.Reply(ctx, op, x.ID, "Да, доставим завтра"))
	must(s.ReplyWith(ctx, op, x.ID, "", &File{Key: "openlines/x/bill.pdf", Name: "bill.pdf", Type: "application/pdf", Size: 4}))
	must(s.ReplyWith(ctx, op, x.ID, "Вот так", &File{Key: "openlines/x/p.png", Name: "p.png", Type: "image/png", Size: 3}))
	for _, b := range []string{`ARRAY['Доставка', 'Оплата']`, `ARRAY['Доставка по городу', 'Самовывоз', 'Оплата картой', 'Возврат']`,
		`ARRAY['Очень длинный пункт меню номер один', 'Второй']`} {
		must(pool.QueryRow(ctx, `INSERT INTO ol_messages (session_id, kind, body, buttons) VALUES ($1, 'bot', 'Выберите', `+b+`) RETURNING 0`, x.ID).Scan(new(int)))
	}
	got, err := s.SendWhatsApp(ctx)
	must(err)
	if got != 7 || len(sent) != 7 {
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
		`{"text":{"body":"Оператор скоро ответит","preview_url":true},"type":"text"}`,
		`{"text":{"body":"Оля:\nДа, доставим завтра","preview_url":true},"type":"text"}`,
		`{"document":{"caption":"Оля:","filename":"bill.pdf","link":"` + FileLink("https://go24.example", wid, bill) + `"},"type":"document"}`,
		`{"image":{"caption":"Оля:\nВот так","link":"` + FileLink("https://go24.example", wid, pic) + `"},"type":"image"}`,
		`{"interactive":{"action":{"buttons":[{"reply":{"id":"Доставка","title":"Доставка"},"type":"reply"},{"reply":{"id":"Оплата","title":"Оплата"},"type":"reply"}]},"body":{"text":"Выберите"},"type":"button"},"type":"interactive"}`,
		`{"interactive":{"action":{"button":"ol.wa_menu","sections":[{"rows":[{"id":"Доставка по городу","title":"Доставка по городу"},{"id":"Самовывоз","title":"Самовывоз"},{"id":"Оплата картой","title":"Оплата картой"},{"id":"Возврат","title":"Возврат"}]}]},"body":{"text":"Выберите"},"type":"list"},"type":"interactive"}`,
		`{"text":{"body":"Выберите\nОчень длинный пункт меню номер один\nВторой","preview_url":true},"type":"text"}`,
	}
	for i := range want {
		if sent[i]["to"] != "79001234567" || sent[i]["messaging_product"] != "whatsapp" {
			t.Fatalf("sent[%d] = %+v", i, sent[i])
		}
		delete(sent[i], "to")
		delete(sent[i], "messaging_product")
		delete(sent[i], "recipient_type")
		m, _ := json.Marshal(sent[i])
		if strings.ReplaceAll(string(m), `&`, "&") != want[i] {
			t.Fatalf("sent[%d] = %s", i, m)
		}
	}
	if got, _ := s.SendWhatsApp(ctx); got != 0 {
		t.Fatal("sent twice")
	}

	// more than 24 hours: the message is not sent, never will be — the dialogue says why and does not stop
	call(hook("70000000000", "Анна", `"type":"text","text":{"body":"Привет"}`))
	got, err = s.SendWhatsApp(ctx)
	must(err)
	if got != 1 {
		t.Fatalf("after the 24 hours: %d", got)
	}
	if got, _ := s.SendWhatsApp(ctx); got != 0 {
		t.Fatal("written again")
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ol_messages m JOIN ol_sessions s ON s.id = m.session_id WHERE s.wa_user = '70000000000'
		AND m.kind = 'note' AND m.body = 'ol.note_wa_window'`).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notes = %d", notes)
	}

	// «Другие обращения клиента»: a dialogue of the same WhatsApp id in another line
	if _, err := pool.Exec(ctx, `INSERT INTO ol_sessions (widget_id, token_hash, channel, wa_user) VALUES ($1, 'h2', 'whatsapp', '79001234567')`, other); err != nil {
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

	must(s.DisconnectWhatsApp(ctx, wid))
	if n, _ := s.WhatsApp(ctx, wid); n.Phone != "" || hooks[len(hooks)-1] != "-" {
		t.Fatalf("disconnected: %+v %v", n, hooks)
	}
	must(s.Reply(ctx, op, x.ID, "ещё"))
	if got, _ := s.SendWhatsApp(ctx); got != 0 {
		t.Fatal("sent without the number")
	}
	body := hook("79001234567", "Иван", `"type":"text","text":{"body":"ещё"}`)
	if err := s.WAHook(ctx, wid, sign(body), []byte(body)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("taken without the number: %v", err)
	}
}
