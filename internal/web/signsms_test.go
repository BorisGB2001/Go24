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
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/sms"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignSMSHTTP: the client with a phone and no e-mail Sign: not sent with no provider of SMS, a bad
// phone refused; with one, the client gets the link by SMS, asks for the code by SMS (the page hides the phone), signs
// with it, gets the link to the signed document by SMS; the signing sheet and the history say «by SMS»; the SMS are
// not on the CRM card; the CRM robot sends to the client's phone.
func TestSignSMSHTTP(t *testing.T) {
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
	last := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		if len(texts) == 0 {
			return "", ""
		}
		return phones[len(phones)-1], texts[len(texts)-1]
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), I18n: bundle, SMS: smsStore,
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool), Sign: sign.NewStore(pool), BaseURL: "https://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	var src bytes.Buffer
	zw := zip.NewWriter(&src)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>Договор</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
	zw.Close()
	key := "go24/crm-documents/t/sms.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(src.Bytes()), int64(src.Len()), docxType))
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "sms.docx", ContentType: docxType, Size: int64(src.Len()), UploadedBy: anna})
	must(err)
	docID, err := srv.CRM.AddDocument(ctx, crm.Document{TemplateName: "Договор", Entity: "deal", EntityID: deal, Number: "9", FileID: file,
		Values: map[string]string{"Client.Director": "Олег Клиентов", "Client.Phone": "8 900 555-44-33"}, AuthorID: anna})
	must(err)

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
	sendForm := func(phone string) url.Values {
		return url.Values{"kind": {"deals"}, "id": {deal}, "doc": {docID}, "title": {"Договор № 9"},
			"signer": {bob}, "client_name": {"Олег Клиентов"}, "client_email": {""}, "client_phone": {phone}}
	}
	signID := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`)

	// the form takes the client's phone from the card's document
	if code, body, _ := do(anna, "GET", "/sign/new?kind=deals&id="+deal+"&doc="+docID, nil); code != http.StatusOK ||
		!strings.Contains(body, `name="client_phone" value="8 900 555-44-33"`) {
		t.Fatalf("the form: %d", code)
	}
	// no provider of SMS: a client with only a phone is not sent to
	if _, _, loc := do(anna, "POST", "/sign", sendForm("8 900 555-44-33")); signID.MatchString(loc) || !strings.Contains(loc, bundle.T("ru-RU", "sign.err.no_sms")) {
		t.Fatalf("sent with no SMS: %s", loc)
	}
	_, err = smsStore.SaveSender(ctx, sms.Sender{Kind: "gateway", Name: "Шлюз", Login: "https://gw.test/send", From: "Shop", Active: true}, "TOKEN")
	must(err)
	if _, _, loc := do(anna, "POST", "/sign", sendForm("12")); signID.MatchString(loc) {
		t.Fatalf("sent to a bad phone: %s", loc)
	}
	_, _, loc := do(anna, "POST", "/sign", sendForm("8 900 555-44-33"))
	m := signID.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("send: %s", loc)
	}
	id := m[1]
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if len(d.Parties) != 2 || d.Parties[1].Phone != "+79005554433" || d.Parties[1].Email != "" {
		t.Fatalf("parties: %+v", d.Parties)
	}
	if p, _ := last(); p != "" {
		t.Fatalf("an SMS before the client's turn: %s", p)
	}
	do(bob, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}})
	tok := d.Parties[1].Token
	if p, text := last(); p != "+79005554433" || !strings.Contains(text, "https://go24/sg/"+tok) || !strings.Contains(text, "Договор № 9") {
		t.Fatalf("the link by SMS: %s %q", p, text)
	}

	// the client's page: the phone hidden, «Код по SMS»; the code by SMS, signing with it
	if code, body, _ := do("", "GET", "/sg/"+tok, nil); code != http.StatusOK || !strings.Contains(body, "&#43;7 *** ***-44-33") ||
		!strings.Contains(body, `name="via" value="sms"`) || strings.Contains(body, `name="via" value="mail"`) {
		t.Fatalf("the client's page: %d", code)
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/code", url.Values{"via": {"sms"}}); !strings.Contains(loc, "code=1") || !strings.Contains(loc, "+7 *** ***-44-33") {
		t.Fatalf("the code by SMS: %s", loc)
	}
	_, text := last()
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(text)
	if code == "" {
		t.Fatalf("no code in %q", text)
	}
	if code, body, _ := do("", "GET", "/sg/"+tok+"?code=1", nil); code != http.StatusOK || !strings.Contains(body, `name="via" value="sms"`) {
		t.Fatalf("«send again» by SMS: %d", code)
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/sign", url.Values{"code": {code}, "agree": {"1"}}); strings.Contains(loc, "code=1") {
		t.Fatalf("signed with the code: %s", loc)
	}
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	if d.Status != "done" || d.Parties[1].CodeVia != "sms" {
		t.Fatalf("done: %s %+v", d.Status, d.Parties[1])
	}
	if _, text := last(); !strings.Contains(text, "https://go24/sg/"+tok) || !strings.Contains(text, bundle.T("ru-RU", "sign.sms.done", "Договор № 9", "")) {
		t.Fatalf("the signed document by SMS: %q", text)
	}
	evs, err := srv.Sign.Events(ctx, id)
	must(err)
	vias := ""
	for _, e := range evs {
		if e.Kind == sign.EvCode {
			vias += e.Note
		}
	}
	if vias != "sms" {
		t.Fatalf("the history's code: %q", vias)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, "&#43;79005554433") ||
		!strings.Contains(body, bundle.T("ru-RU", "sign.via.sms")) {
		t.Fatalf("anna's page: %d", code)
	}
	var onCard int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM sms_messages WHERE entity <> ''`).Scan(&onCard))
	if onCard != 0 {
		t.Fatalf("SMS of «Подпись» on the card: %d", onCard)
	}

	// the CRM robot: the client with only a phone
	title, err := srv.SignRobot(ctx, anna, "deal", deal, docID, []string{anna}, "Олег Клиентов", "", "+79005554433")
	must(err)
	sent, err := srv.Sign.List(ctx, anna, "", true)
	must(err)
	if title != "Договор № 9" || len(sent) != 2 {
		t.Fatalf("the robot: %q %d", title, len(sent))
	}
	for _, x := range sent {
		if x.ID != id && (len(x.Parties) != 2 || x.Parties[1].Phone != "+79005554433") {
			t.Fatalf("the robot's client: %+v", x.Parties)
		}
	}
}

func TestMaskPhone(t *testing.T) {
	for in, want := range map[string]string{"+79005554433": "+7 *** ***-44-33", "+4915112345678": "+491 *** ***-56-78", "": "", "+12345": "+12345"} {
		if got := maskPhone(in); got != want {
			t.Errorf("maskPhone(%q) = %q, want %q", in, got, want)
		}
	}
	got := signClients([]string{"Олег", "", ""}, []string{"", "a@b.c"}, []string{" +7 900 ", "", ""})
	if len(got) != 2 || got[0].Phone != "+7 900" || got[1].Email != "a@b.c" || got[1].Phone != "" {
		t.Fatalf("signClients: %+v", got)
	}
}
