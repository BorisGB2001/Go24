package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

func TestMailAddrs(t *testing.T) {
	got := mailAddrs(`"Петров, Пётр" <petr@x.test>, ann@x.test`)
	if len(got) != 2 || got[0].Name != "Петров, Пётр" || got[0].Email != "petr@x.test" || got[1].Name != "" || got[1].Email != "ann@x.test" {
		t.Fatalf("mailAddrs = %+v", got)
	}
	if got := mailAddrs("не адрес <"); len(got) != 1 || got[0].Email != "" || got[0].Name != "не адрес <" {
		t.Fatalf("broken header = %+v", got)
	}
	if mailAddrs(" ") != nil {
		t.Fatal("empty header has addresses")
	}
}

// The contact card of a sender or a recipient, as Outlook's profile card: an employee's card of the portal, the CRM's
// contact, the user's own contact made from the card, the latest letters with the address only.
func TestMailPersonCardHTTP(t *testing.T) {
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
	var anna, bob, client string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ('bob', 'Борис Бобров', 'Bob@go24.test', 'Инженер') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO crm_contacts (name, position, email, responsible_id, created_by) VALUES ('Иван Клиентов', 'Закупщик', 'client@x.test', $1, $1) RETURNING id`, anna).Scan(&client))
	_, err = pool.Exec(ctx, `INSERT INTO crm_comms (entity, entity_id, kind, value) VALUES ('contact', $1, 'email', 'client@x.test')`, client)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folder := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		return id
	}
	inbox, sent, trash := folder("INBOX", "inbox"), folder("Sent", "sent"), folder("Trash", "trash")
	uid := 0
	letter := func(folder, from, fromName, to, cc, subject string, ago int) string {
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body, sent_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, '', now() - make_interval(hours => $9)) RETURNING id::text`,
			box, folder, uid, from, fromName, to, cc, subject, ago).Scan(&id))
		return id
	}
	report := letter(inbox, "bob@go24.test", "Борис", "anna@go24.test", "", "Отчёт за неделю", 1)
	letter(sent, "anna@go24.test", "Анна", "Борис <bob@go24.test>, joann@x.test", "", "План работ", 2)
	letter(trash, "bob@go24.test", "Борис", "anna@go24.test", "", "Удалённое письмо", 3)
	letter(inbox, "client@x.test", "Иван", "anna@go24.test", "", "Запрос цены", 4)
	letter(sent, "anna@go24.test", "Анна", "joann@x.test", "", "Только для Джоанны", 5)
	letter(inbox, "ann@x.test", "Ann", "anna@go24.test", "Bob@go24.test", "Копия Бобу", 6)

	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client303 := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(path string, form url.Values, want int) string {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client303.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	card := func(addr string) string {
		t.Helper()
		return do("/mail/person?"+url.Values{"addr": {addr}, "back": {"/mail?folder=" + inbox}}.Encode(), nil, 200)
	}
	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("no %q in %.3000s", w, page)
			}
		}
	}
	hasnt := func(page string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(page, w) {
				t.Fatalf("%q in %.3000s", w, page)
			}
		}
	}

	// an employee: the portal's card (position, profile), the letters from and to the address, not the deleted one
	p := card("Борис <bob@go24.test>")
	has(p, "Борис Бобров", "Инженер", "/u/"+bob, "Отчёт за неделю", "План работ", "Копия Бобу", "Написать письмо", "to=bob%40go24.test",
		"/mail?folder="+inbox+"&amp;letter="+report, "Добавить отправителя в избранное", "Добавить в контакты", `data-copy="bob@go24.test"`)
	hasnt(p, "Удалённое письмо", "Запрос цены")

	// a CRM contact: its name, position and card; ann@ is not joann@
	p = card("client@x.test")
	has(p, "Иван Клиентов", "Закупщик", "/crm/contacts/"+client, "Запрос цены")
	p = card("ann@x.test")
	has(p, "Ann", "Копия Бобу")
	hasnt(p, "Только для Джоанны", "План работ")
	has(card("nobody@x.test"), "nobody@x.test", "Писем с этим адресом пока нет")

	// «Добавить в контакты» → back to the page; the card then shows the contact to be changed
	back := "/mail?folder=" + inbox + "&letter=" + report
	do("/mail/person/contact", url.Values{"email": {"ann@x.test"}, "name": {"Энн Смит"}, "company": {"Acme"}, "phone": {"+7 900 000-00-00"}, "back": {back}}, 303)
	p = card("ann@x.test")
	has(p, "Энн Смит", "Acme", "7 900 000-00-00", "Изменить контакт")
	do("/mail/person/contact", url.Values{"email": {"ann@x.test"}, "name": {"Энн"}, "back": {back}}, 303)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_contacts WHERE user_id = $1`, anna).Scan(&n))
	if n != 1 {
		t.Fatalf("contacts = %d, want the one changed", n)
	}

	do("/mail/person?addr=not-an-address", nil, 404)

	// the reading pane: the sender and the recipients open their cards
	p = do(back, nil, 200)
	has(p, `class="mperson" data-card="/mail/person?addr=bob%40go24.test&amp;back=`, `data-card="/mail/person?addr=anna%40go24.test`)
}
