package web

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignChatHTTP: the client of a deal's document gets the link in the open line's chat Sign by
// messengers: the form offers «В чат» (chosen when the client has no e-mail and no phone), not with no dialogue; the
// link, the code and the signed document come into the deal's contact's latest dialogue; the client's page offers
// «Код в чат»; the history says «в чат»; the REST API takes "via": "chat"; the CRM robot sends into the chat.
func TestSignChatHTTP(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
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
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), I18n: bundle,
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool), Sign: sign.NewStore(pool), BaseURL: "https://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return key }

	var contact string
	must(pool.QueryRow(ctx, `INSERT INTO crm_contacts (name, responsible_id, created_by) VALUES ('Олег Клиентов', $1, $1) RETURNING id`, anna).Scan(&contact))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna},
		Contact: crm.Ref{ID: contact}})
	must(err)
	lonely, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Без чата", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	var src bytes.Buffer
	zw := zip.NewWriter(&src)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>Договор</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
	zw.Close()
	addDoc := func(deal, name string) string {
		key := "go24/crm-documents/t/" + name
		must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(src.Bytes()), int64(src.Len()), docxType))
		file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: name, ContentType: docxType, Size: int64(src.Len()), UploadedBy: anna})
		must(err)
		id, err := srv.CRM.AddDocument(ctx, crm.Document{TemplateName: "Договор", Entity: "deal", EntityID: deal, Number: "5", FileID: file,
			Values: map[string]string{"Client.Director": "Олег Клиентов"}, AuthorID: anna})
		must(err)
		return id
	}
	docID, lonelyDoc := addDoc(deal, "chat.docx"), addDoc(lonely, "lonely.docx")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob} {
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
		if who != "" {
			req.AddCookie(as[who])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	sendForm := func(deal, doc string) url.Values {
		return url.Values{"kind": {"deals"}, "id": {deal}, "doc": {doc}, "title": {"Договор № 5"},
			"signer": {bob}, "client_name": {"Олег Клиентов"}, "client_email": {""}, "client_phone": {""}, "client_via": {"chat"}}
	}
	signID := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`)

	// no dialogue: no «В чат» in the form, not sent into the chat
	if code, body, _ := do(anna, "GET", "/sign/new?kind=deals&id="+lonely+"&doc="+lonelyDoc, nil); code != http.StatusOK || strings.Contains(body, `name="client_via"`) {
		t.Fatalf("the form with no chat: %d", code)
	}
	if _, _, loc := do(anna, "POST", "/sign", sendForm(lonely, lonelyDoc)); signID.MatchString(loc) || !strings.Contains(loc, bundle.T("ru-RU", "err.doc_send_chat")) {
		t.Fatalf("sent with no chat: %s", loc)
	}

	wid, err := srv.OpenLines.SaveWidget(ctx, anna, openlines.Widget{Name: "Линия", Active: true})
	must(err)
	sid, vtok, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	must(srv.OpenLines.VisitorSay(ctx, wid, vtok, "Пришлите договор"))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET contact_id = $2 WHERE id = $1`, sid, contact)
	must(err)
	last := func() string {
		t.Helper()
		msgs, err := srv.OpenLines.VisitorMessages(ctx, wid, vtok, 0)
		must(err)
		return msgs[len(msgs)-1].Body
	}

	// the form: «В чат» chosen, the client having neither an e-mail nor a phone
	if code, body, _ := do(anna, "GET", "/sign/new?kind=deals&id="+deal+"&doc="+docID, nil); code != http.StatusOK ||
		!strings.Contains(body, `<option value="chat" selected>`) {
		t.Fatalf("the form: %d", code)
	}
	_, _, loc := do(anna, "POST", "/sign", sendForm(deal, docID))
	m := signID.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("send: %s", loc)
	}
	id := m[1]
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if len(d.Parties) != 2 || d.Parties[1].Chat != sid {
		t.Fatalf("parties: %+v", d.Parties)
	}
	if strings.Contains(last(), "/sg/") {
		t.Fatal("the link before the client's turn")
	}
	do(bob, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}})
	tok := d.Parties[1].Token
	if text := last(); text != bundle.T("ru-RU", "sign.chat.link", "Анна", "Договор № 5", "https://go24/sg/"+tok) {
		t.Fatalf("the link in the chat: %q", text)
	}

	// the client's page: «Код в чат»; the code in the chat, signing with it
	if code, body, _ := do("", "GET", "/sg/"+tok, nil); code != http.StatusOK || !strings.Contains(body, `name="via" value="chat"`) ||
		strings.Contains(body, `name="via" value="mail"`) || !strings.Contains(body, bundle.T("ru-RU", "sign.get_code_chat")) {
		t.Fatalf("the client's page: %d", code)
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/code", url.Values{"via": {"sms"}}); !strings.Contains(loc, "code=1") ||
		!strings.Contains(loc, bundle.T("ru-RU", "sign.code_sent", bundle.T("ru-RU", "sign.chat.to"))) {
		t.Fatalf("the code in the chat: %s", loc)
	}
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(last())
	if code == "" {
		t.Fatalf("no code in %q", last())
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/sign", url.Values{"code": {code}, "agree": {"1"}}); strings.Contains(loc, "code=1") {
		t.Fatalf("signed with the code: %s", loc)
	}
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	if d.Status != "done" || d.Parties[1].CodeVia != "chat" {
		t.Fatalf("done: %s %+v", d.Status, d.Parties[1])
	}
	if text := last(); text != bundle.T("ru-RU", "sign.chat.done", "Договор № 5", "https://go24/sg/"+tok) {
		t.Fatalf("the signed document in the chat: %q", text)
	}
	evs, err := srv.Sign.Events(ctx, id)
	must(err)
	vias := ""
	for _, e := range evs {
		if e.Kind == sign.EvCode || e.Kind == sign.EvSent {
			vias += e.Note + ","
		}
	}
	if vias != ",chat,chat," { // bob told by a notification, the client's link and code in the chat
		t.Fatalf("the history: %q", vias)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, bundle.T("ru-RU", "sign.via.chat")) {
		t.Fatalf("anna's page: %d", code)
	}

	// REST: "clients": [{"via": "chat"}]
	apiKey, err := srv.Keys.Create(ctx, anna, "sign", []string{"documents:write"}, 0)
	must(err)
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/sign", strings.NewReader(`{"document": "`+docID+`", "signer": "`+bob+`",
		"clients": [{"name": "Олег", "via": "chat"}]}`))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated || !strings.Contains(string(raw), `"chat": "`+sid+`"`) {
		t.Fatalf("REST: %d %s", res.StatusCode, raw)
	}

	// the CRM robot: the client with no e-mail and no phone gets the link in the chat
	if !srv.SignChatReady(ctx, "deal", deal) || srv.SignChatReady(ctx, "deal", lonely) {
		t.Fatal("SignChatReady")
	}
	_, err = srv.SignRobot(ctx, anna, "deal", deal, docID, []string{anna}, "Олег Клиентов", "", "")
	must(err)
	sent, err := srv.Sign.List(ctx, anna, "", true)
	must(err)
	robot := 0
	for _, x := range sent {
		if len(x.Parties) == 2 && x.Parties[0].UserID == anna && x.Parties[1].Chat == sid {
			robot++
		}
	}
	if robot != 1 {
		t.Fatalf("the robot's client: %d of %d", robot, len(sent))
	}
	if _, err := srv.SignRobot(ctx, anna, "deal", lonely, lonelyDoc, []string{anna}, "Олег", "", ""); err == nil {
		t.Fatal("the robot sent with no way to the client")
	}
}

func TestSignCodeVia(t *testing.T) {
	both := sign.Party{Email: "a@b.c", Phone: "+7900", Chat: "s"}
	for _, c := range []struct {
		p           sign.Party
		asked, want string
	}{
		{both, "", "chat"}, {both, "mail", "mail"}, {both, "sms", "sms"}, {sign.Party{Email: "a@b.c", Phone: "+7900"}, "chat", "mail"},
		{sign.Party{Phone: "+7900"}, "mail", "sms"}, {sign.Party{Chat: "s"}, "sms", "chat"}, {sign.Party{}, "", "mail"},
	} {
		if got := signCodeVia(c.p, c.asked); got != c.want {
			t.Errorf("signCodeVia(%+v, %q) = %q, want %q", c.p, c.asked, got, c.want)
		}
	}
	got := signClients([]string{"Олег", "Ира"}, []string{"", "i@b.c"}, nil, "chat")
	if len(got) != 2 || got[0].Chat != "chat" || got[1].Chat != "" {
		t.Fatalf("signClients: %+v", got)
	}
}
