package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/sms"
	"go24/internal/tasks"
)

// TestDocSendHTTP: «Отправить клиенту» of a card's document — not offered with no SMS provider and no
// dialogue; by SMS to the client's phone with the public link switched on, the line «SMS» in the card; into the
// latest dialogue of the deal's contact with the line «Документ отправлен в чат»; REST /api/v1/documents/{id}/send.
func TestDocSendHTTP(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	var mu sync.Mutex
	var texts, phones []string
	smsStore := sms.NewStore(pool, "secret")
	smsStore.Post = func(_ context.Context, req sms.Request) (sms.Response, error) {
		m, _ := req.JSON.(map[string]string)
		mu.Lock()
		defer mu.Unlock()
		texts, phones = append(texts, m["text"]), append(phones, m["phone"])
		return sms.Response{Status: http.StatusOK, Body: []byte(`{"id":1}`)}, nil
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), I18n: bundle, SMS: smsStore,
		Keys: apikeys.NewStore(pool), BaseURL: "https://go24/", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.CRM.SendSMS = func(ctx context.Context, userID, entity, entityID, phone, text string) error {
		_, err := srv.SMS.Send(ctx, sms.Out{Phone: phone, Text: text, AuthorID: userID, Entity: entity, EntityID: entityID})
		return err
	}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return key }

	var contact string
	must(pool.QueryRow(ctx, `INSERT INTO crm_contacts (name, responsible_id, created_by) VALUES ('Ольга Петрова', $1, $1) RETURNING id`, anna).Scan(&contact))
	_, err = pool.Exec(ctx, `INSERT INTO crm_comms (entity, entity_id, kind, value) VALUES ('contact', $1, 'phone', '8 900 555-44-33')`, contact)
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna},
		Contact: crm.Ref{ID: contact}})
	must(err)
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, content_type, size, uploaded_by) VALUES ('k/d.docx', 'd.docx', $1, 1, $2) RETURNING id`,
		docxType, anna).Scan(&file))
	docID, err := srv.CRM.AddDocument(ctx, crm.Document{TemplateName: "Договор", Entity: "deal", EntityID: deal, Number: "7", FileID: file,
		Values: map[string]string{}, AuthorID: anna})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	send := "/crm/deals/" + deal + "/documents/" + docID + "/send"
	card := func() string {
		t.Helper()
		code, body, _ := do(anna, "GET", "/crm/deals/"+deal, nil)
		if code != http.StatusOK {
			t.Fatalf("the card: %d", code)
		}
		return body
	}

	// neither an SMS provider nor a dialogue: nothing to offer, and nothing sent
	if strings.Contains(card(), send) {
		t.Fatal("«Отправить клиенту» with no channel")
	}
	if _, _, loc := do(anna, "POST", send, url.Values{"via": {"sms"}}); !strings.Contains(loc, bundle.T("ru-RU", "err.sms_no_sender")) {
		t.Fatalf("SMS with no provider: %s", loc)
	}
	if _, _, loc := do(anna, "POST", send, url.Values{"via": {"chat"}}); !strings.Contains(loc, bundle.T("ru-RU", "err.doc_send_chat")) {
		t.Fatalf("chat with no dialogue: %s", loc)
	}

	// by SMS: the client's phone in the form; the text and the public link switched on; the line «SMS» in the card
	_, err = smsStore.SaveSender(ctx, sms.Sender{Kind: "gateway", Name: "Шлюз", Login: "https://gw.test/send", From: "Shop", Active: true}, "TOKEN")
	must(err)
	if body := card(); !strings.Contains(body, `action="`+send+`"`) || !strings.Contains(body, `<option value="8 900 555-44-33">`) ||
		!strings.Contains(body, bundle.T("ru-RU", "docs.send_text", "Договор № 7")) || strings.Contains(body, `<option value="chat">`) {
		t.Fatal("the form of «Отправить клиенту» by SMS")
	}
	if _, _, loc := do(anna, "POST", send, url.Values{"via": {"sms"}, "phone": {"8 900 555-44-33"}, "text": {"Ваш договор"}}); !strings.Contains(loc, "msg="+bundle.T("ru-RU", "docs.sent_sms")) {
		t.Fatalf("by SMS: %s", loc)
	}
	doc, err := srv.CRM.DocumentByID(ctx, docID)
	must(err)
	if doc.PublicToken == "" {
		t.Fatal("the public link is not on")
	}
	mu.Lock()
	if len(texts) != 1 || phones[0] != "+79005554433" || texts[0] != "Ваш договор\nhttps://go24/d/"+doc.PublicToken {
		t.Fatalf("the SMS: %q %q", phones, texts)
	}
	mu.Unlock()
	if _, _, loc := do(anna, "POST", send, url.Values{"via": {"sms"}, "phone": {"12"}}); !strings.Contains(loc, bundle.T("ru-RU", "err.sms_phone")) {
		t.Fatalf("a bad phone: %s", loc)
	}
	if _, _, loc := do(anna, "POST", send, url.Values{"via": {"fax"}}); !strings.Contains(loc, bundle.T("ru-RU", "err.doc_send_via")) {
		t.Fatalf("an unknown channel: %s", loc)
	}
	evs, err := srv.CRM.Timeline(ctx, "deal", deal)
	must(err)
	if e, ok := crm.ParseSMSEvent(evs[0].Body); evs[0].Kind != "sms" || !ok || e.To != "8 900 555-44-33" || !strings.Contains(e.Text, "/d/"+doc.PublicToken) {
		t.Fatalf("the line «SMS»: %+v", evs[0])
	}

	// into the chat: the latest dialogue of the deal's contact; the link already on stays the same
	wid, err := srv.OpenLines.SaveWidget(ctx, anna, openlines.Widget{Name: "Линия", Active: true})
	must(err)
	sid, vtok, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	must(srv.OpenLines.VisitorSay(ctx, wid, vtok, "Пришлите договор"))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET contact_id = $2 WHERE id = $1`, sid, contact)
	must(err)
	if body := card(); !strings.Contains(body, `<option value="chat">`) {
		t.Fatal("no «В чат» in the form")
	}
	if code, body, loc := do(anna, "POST", send, url.Values{"via": {"chat"}, "text": {""}}); !strings.Contains(loc, "msg="+bundle.T("ru-RU", "docs.sent_chat")) {
		t.Fatalf("into the chat: %d %s %.300s", code, loc, body)
	}
	msgs, err := srv.OpenLines.VisitorMessages(ctx, wid, vtok, 0)
	must(err)
	want := bundle.T("ru-RU", "docs.send_text", "Договор № 7") + "\nhttps://go24/d/" + doc.PublicToken
	if last := msgs[len(msgs)-1]; last.Body != want {
		t.Fatalf("the chat's message: %q, want %q", last.Body, want)
	}
	if body := card(); !strings.Contains(body, bundle.T("ru-RU", "crm.ev.doc_sent")+" «Договор № 7» <a href=\"/openlines?s="+sid+"\">") {
		t.Fatal("no «Документ отправлен в чат» in the card's history")
	}

	// REST
	key, err := srv.Keys.Create(ctx, anna, "docs", []string{"documents:write"}, 0)
	must(err)
	api := func(body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/api/v1/documents/"+docID+"/send", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if code, out := api(`{"via":"sms","text":"Договор"}`); code != http.StatusOK || out["phone"] != "+79005554433" {
		t.Fatalf("REST by SMS: %d %v", code, out)
	}
	if code, out := api(`{"via":"chat"}`); code != http.StatusOK || out["chat"] != sid {
		t.Fatalf("REST into the chat: %d %v", code, out)
	}
	if code, _ := api(`{"via":"pigeon"}`); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Fatalf("REST, an unknown channel: %d", code)
	}
}
