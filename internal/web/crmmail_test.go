package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
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
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// fakeSMTP is a mail server that takes every letter (plain SMTP, no AUTH) and keeps what came.
func fakeSMTP(t *testing.T) (port int, got func() []string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var letters []string
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r, say := bufio.NewReader(c), func(s string) { io.WriteString(c, s+"\r\n") }
				say("220 fake")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						say("250 fake")
					case cmd == "DATA":
						say("354 go on")
						var b strings.Builder
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							b.WriteString(l)
						}
						mu.Lock()
						letters = append(letters, b.String())
						mu.Unlock()
						say("250 kept")
					case cmd == "QUIT":
						say("221 bye")
						return
					default:
						say("250 ok")
					}
				}
			}(c)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), letters...)
	}
}

// TestCRMMailHTTP: «Шаблоны писем CRM» — the user's own and shared ones, another's private one unseen, another's
// shared one not changed but by an administrator; a template filled with the deal's fields (escaped in the HTML);
// the card's «Письмо» offers the client's addresses and the templates, sends the letter with its file and puts it
// into the deal's timeline at once; whoever may only read the deal does not send.
func TestCRMMailHTTP(t *testing.T) {
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
	var anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), Mail: mail.NewStore(pool, "test-secret"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	port, letters := fakeSMTP(t)
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@shop.test", IMAPHost: "127.0.0.1", IMAPPort: 1, SMTPHost: "127.0.0.1", SMTPPort: port, Password: "pw"})
	must(err)

	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	ivan, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван", Email: "ivan@client.test", Responsible: crm.Ref{ID: anna}})
	must(err)
	petr, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Пётр", Email: "petr@client.test", Responsible: crm.Ref{ID: anna}})
	must(err)
	co, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "ООО Клиент", Email: "office@client.test", Responsible: crm.Ref{ID: anna}})
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка <станков>", Amount: 1500, Currency: "RUB", PipelineID: pipes[0].ID,
		Stage: pipes[0].Stages[0], Contact: crm.Ref{ID: ivan}, Contacts: []crm.Ref{{ID: ivan}, {ID: petr}}, Company: crm.Ref{ID: co}, Responsible: crm.Ref{ID: anna}})
	must(err)

	to, err := srv.CRM.ClientEmails(ctx, "deal", deal)
	must(err)
	if len(to) != 3 || to[0].Email != "ivan@client.test" || to[2].Email != "office@client.test" {
		t.Fatalf("the deal's addresses: %+v", to)
	}
	if to, err := srv.CRM.ClientEmails(ctx, "company", co); err != nil || len(to) != 1 || to[0].Email != "office@client.test" {
		t.Fatalf("the company's addresses: %+v %v", to, err)
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	annaC, borisC := cookieOf(anna, false), cookieOf(boris, false)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, method, path, ct string, body io.Reader) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(c)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}
	post := func(c *http.Cookie, path string, form url.Values) (int, string) {
		t.Helper()
		code, loc, _ := do(c, "POST", path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		return code, loc
	}

	// templates: Anna's shared one and private one, Boris's private one
	if code, loc := post(annaC, "/crm/mail-templates/new", url.Values{"name": {"КП"}, "entity": {"deal"}, "shared": {"1"},
		"subject": {"Предложение: {Title}"}, "html": {"<p>Сумма {Amount} {Currency}, {Contact}. {Нет такого} {CloseDate}{Title}</p><script>x</script>"}}); code != http.StatusSeeOther || strings.Contains(loc, "err") {
		t.Fatalf("save: %d %s", code, loc)
	}
	post(annaC, "/crm/mail-templates/new", url.Values{"name": {"Черновик"}, "entity": {"deal"}, "subject": {"Мой"}})
	post(borisC, "/crm/mail-templates/new", url.Values{"name": {"Борисов"}, "entity": {"lead"}, "subject": {"Его"}})
	if code, loc := post(annaC, "/crm/mail-templates/new", url.Values{"name": {"Пустой"}, "entity": {"deal"}}); !strings.Contains(loc, "msg=") && code == http.StatusSeeOther {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_mail_templates WHERE name = 'Пустой'`).Scan(&n))
		if n != 0 {
			t.Fatal("a template without a subject and text is kept")
		}
	}
	mine, err := srv.CRM.MailTemplates(ctx, anna, "")
	must(err)
	his, err := srv.CRM.MailTemplates(ctx, boris, "")
	must(err)
	if len(mine) != 2 || len(his) != 2 {
		t.Fatalf("Anna sees %d, Boris %d templates", len(mine), len(his))
	}
	var kp, draft crm.MailTemplate
	for _, x := range mine {
		map[string]*crm.MailTemplate{"КП": &kp, "Черновик": &draft}[x.Name].ID = x.ID
	}
	if kp, err = srv.CRM.MailTemplateOf(ctx, anna, kp.ID); err != nil || strings.Contains(kp.Body, "script") {
		t.Fatalf("the template's text is cleaned: %q %v", kp.Body, err)
	}
	if _, err := srv.CRM.MailTemplateOf(ctx, boris, draft.ID); err != crm.ErrNotFound {
		t.Fatalf("another's private template: %v", err)
	}
	if _, err := srv.CRM.SaveMailTemplate(ctx, boris, false, crm.MailTemplate{ID: kp.ID, Name: "Чужой", Entity: "deal", Subject: "x"}); err != crm.ErrForbidden {
		t.Fatalf("another's shared template changed: %v", err)
	}
	if err := srv.CRM.DeleteMailTemplate(ctx, boris, false, kp.ID); err != crm.ErrForbidden {
		t.Fatalf("another's shared template deleted: %v", err)
	}
	if _, err := srv.CRM.SaveMailTemplate(ctx, boris, true, crm.MailTemplate{ID: kp.ID, Name: "КП", Entity: "deal", Subject: kp.Subject, Body: kp.Body, Shared: true}); err != nil {
		t.Fatalf("an administrator changes it: %v", err)
	}
	if code, _, body := do(annaC, "GET", "/crm/mail-templates?edit="+kp.ID, "", nil); code != http.StatusOK || !strings.Contains(body, "Предложение: {Title}") {
		t.Fatalf("templates page: %d", code)
	}

	// filled with the deal's values
	code, _, body := do(annaC, "GET", "/crm/deals/"+deal+"/mail-template/"+kp.ID, "", nil)
	var filled struct{ Subject, HTML string }
	must(json.Unmarshal([]byte(body), &filled))
	if code != http.StatusOK || filled.Subject != "Предложение: Поставка <станков>" ||
		!strings.Contains(filled.HTML, "Сумма 1 500 RUB, Иван. {Нет такого} Поставка &lt;станков&gt;</p>") {
		t.Fatalf("filled: %d %+v", code, filled)
	}
	if code, _, _ := do(annaC, "GET", "/crm/leads/"+deal+"/mail-template/"+kp.ID, "", nil); code == http.StatusOK {
		t.Fatal("a deal's template filled for a lead")
	}

	// the card offers the form; Boris may only read Anna's deals
	_, err = pool.Exec(ctx, `UPDATE crm_roles SET perms = jsonb_set(perms, '{deal,write}', '"own"') WHERE is_default`)
	must(err)
	if code, _, body := do(borisC, "GET", "/crm/deals/"+deal, "", nil); code != http.StatusOK || strings.Contains(body, "/crm/deals/"+deal+"/mail\"") {
		t.Fatalf("Boris's card offers «Письмо»: %d", code)
	}
	if code, _, body := do(annaC, "GET", "/crm/deals/"+deal, "", nil); code != http.StatusOK || !strings.Contains(body, `action="/crm/deals/`+deal+`/mail"`) ||
		!strings.Contains(body, "petr@client.test") || !strings.Contains(body, ">КП</option>") || strings.Contains(body, "Борисов") {
		t.Fatalf("the card's «Письмо»: %d", code)
	}
	letter := func(c *http.Cookie) (int, string) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for k, v := range map[string]string{"box": box, "to": "ivan@client.test", "cc": "petr@client.test", "subject": filled.Subject, "html": filled.HTML} {
			must(w.WriteField(k, v))
		}
		fw, _ := w.CreateFormFile("files", "price.txt")
		io.WriteString(fw, "прайс")
		must(w.Close())
		code, loc, _ := do(c, "POST", "/crm/deals/"+deal+"/mail", w.FormDataContentType(), &b)
		return code, loc
	}
	if code, loc := letter(borisC); code != http.StatusForbidden || len(letters()) != 0 {
		t.Fatalf("Boris sends: %d %s %d", code, loc, len(letters()))
	}
	if code, loc := letter(annaC); code != http.StatusSeeOther || !strings.Contains(loc, "#timeline") {
		t.Fatalf("send: %d %s", code, loc)
	}
	if got := letters(); len(got) != 1 || !strings.Contains(got[0], "price.txt") || !strings.Contains(got[0], "ivan@client.test") {
		t.Fatalf("the letter that went: %d", len(got))
	}
	var subject string
	var outgoing bool
	must(pool.QueryRow(ctx, `SELECT c.subject, c.outgoing FROM crm_mail c WHERE c.entity = 'deal' AND c.entity_id::text = $1`, deal).Scan(&subject, &outgoing))
	if subject != filled.Subject || !outgoing {
		t.Fatalf("in the timeline: %q %v", subject, outgoing)
	}
	tl, err := srv.CRM.Timeline(ctx, "deal", deal)
	must(err)
	found := false
	for _, e := range tl {
		found = found || e.Kind == "mail"
	}
	if !found {
		t.Fatal("no letter in the deal's timeline")
	}
}

// TestInvoiceSendHTTP: «Отправить клиенту» of an invoice — the deal's letter with the invoice's file (the printed page
// in HTML when there is no document server) and the client's link, switched on by the sending; the invoice becomes
// «Отправлен», the letter goes into the deal's timeline; an invoice of another deal is not sent by this deal's letter.
func TestInvoiceSendHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool), I18n: bundle,
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), Mail: mail.NewStore(pool, "test-secret"), BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	port, letters := fakeSMTP(t)
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@shop.test", IMAPHost: "127.0.0.1", IMAPPort: 1, SMTPHost: "127.0.0.1", SMTPPort: port, Password: "pw"})
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	ivan, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван", Email: "ivan@client.test", Responsible: crm.Ref{ID: anna}})
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Contact: crm.Ref{ID: ivan}, Responsible: crm.Ref{ID: anna}})
	must(err)
	other, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Другая", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.SetProductRows(ctx, anna, "deal", deal, []crm.ProductRow{{Name: "Станок", Price: 1000, Quantity: 2}})
	must(err)
	inv, err := srv.CRM.MakeInvoice(ctx, anna, "invoice", deal, 0)
	must(err)
	alien, err := srv.CRM.MakeInvoice(ctx, anna, "invoice", other, 0)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		req.Header.Set("Accept-Language", "ru")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}

	_, _, page := do("GET", "/crm/invoices/"+inv, nil)
	if !strings.Contains(page, `name="inv" value="`+inv+`"`) || !strings.Contains(page, `value="ivan@client.test"`) ||
		!strings.Contains(page, "Направляем счёт № ") || !strings.Contains(page, `action="/crm/deals/`+deal+`/mail"`) {
		t.Fatal("the invoice has no «Отправить клиенту»")
	}
	send := url.Values{"box": {box}, "to": {"ivan@client.test"}, "subject": {"Счёт"}, "html": {"<p>Добрый день</p>"},
		"inv": {alien}, "inv_file": {"1"}, "inv_link": {"1"}}
	if _, _, _ = do("POST", "/crm/deals/"+deal+"/mail", send); len(letters()) != 0 {
		t.Fatal("another deal's invoice sent")
	}
	send.Set("inv", inv)
	code, loc, _ := do("POST", "/crm/deals/"+deal+"/mail", send)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/invoices/"+inv+"?msg=") {
		t.Fatalf("send: %d %s", code, loc)
	}
	v, err := srv.CRM.Invoice(ctx, inv)
	must(err)
	got := letters()
	if len(got) != 1 || v.PublicToken == "" || v.Status != "sent" {
		t.Fatalf("letters %d, token %q, status %s", len(got), v.PublicToken, v.Status)
	}
	if !strings.Contains(got[0], "https://go24.test/q/"+v.PublicToken) || !strings.Contains(got[0], "text/html; charset=utf-8; name=") {
		t.Fatalf("the letter has no link or no file:\n%s", got[0])
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_mail WHERE entity = 'deal' AND entity_id::text = $1`, deal).Scan(&n))
	if n != 1 {
		t.Fatalf("the letter isn't in the deal's timeline: %d", n)
	}
}
