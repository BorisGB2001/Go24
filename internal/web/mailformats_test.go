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
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailFormatsHTTP drives «Условное форматирование» over HTTP against the test database: the tab, a rule's form,
// order and on/off; the letters a rule takes are set off in the list and carry look in the REST API; nobody else
// sees or changes the rules.
func TestMailFormatsHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"), Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'INBOX', 'inbox', true) RETURNING id::text`, box).Scan(&inbox))
	letter := func(uid int, from, subject string, seen bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, seen)
			VALUES ($1, $2, $3, $4, '', 'anna@go24.test', $5, 'текст', $6, $7) RETURNING id::text`, box, inbox, uid, from, subject,
			time.Now().Add(-time.Duration(uid)*time.Hour), seen).Scan(&id))
		return id
	}
	boss := letter(1, "boss@go24.test", "План", true)
	fresh := letter(2, "ivan@client.test", "Вопрос", false)
	plain := letter(3, "news@shop.test", "Скидки", true)

	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, *http.Response) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res
	}
	page := func(path string) string {
		t.Helper()
		p, _ := do(anna, "GET", path, nil, 200)
		return p
	}
	row := func(p, id string) string { // the list row of a letter: its opening tag
		t.Helper()
		i := strings.Index(p, `data-letter="`+id+`"`)
		if i < 0 {
			t.Fatalf("no row of %s", id)
		}
		return p[strings.LastIndex(p[:i], "<div"):i]
	}

	// the tab, a new rule's form; rules without a condition or with a strange colour are refused
	if p := page("/mail/settings?tab=formats"); !strings.Contains(p, "/mail/settings?tab=formats&edit=new") {
		t.Fatalf("formats tab: %.2000s", p)
	}
	if p := page("/mail/settings?tab=formats&edit=new"); !strings.Contains(p, `name="color" value="red" checked`) || !strings.Contains(p, `name="unread"`) {
		t.Fatalf("new form: %.3000s", p)
	}
	for _, bad := range []url.Values{{"name": {"Пусто"}, "color": {"red"}}, {"name": {"Розовый"}, "color": {"pink"}, "unread": {"1"}}} {
		if _, res := do(anna, "POST", "/mail/formats", bad, 303); !strings.Contains(res.Header.Get("Location"), "msg=") {
			t.Fatalf("%v saved", bad)
		}
	}
	do(anna, "POST", "/mail/formats", url.Values{"name": {"Новые"}, "enabled": {"1"}, "unread": {"1"}, "color": {"blue"}}, 303)
	do(anna, "POST", "/mail/formats", url.Values{"name": {"Начальник"}, "enabled": {"1"}, "if_from": {"boss@go24.test"}, "color": {"red"},
		"bold": {"1"}, "underline": {"1"}}, 303)
	list, err := srv.Mail.Formats(ctx, anna)
	must(err)
	if len(list) != 2 || list[1].If.From != "boss@go24.test" || !list[1].Look.Bold || list[1].Look.Italic || !list[0].Unread {
		t.Fatalf("formats = %+v", list)
	}

	// the list: the boss's letter red, bold, underlined; the unread one blue; the rest as usual
	p := page("/mail?folder=" + inbox)
	if r := row(p, boss); !strings.Contains(r, "mlook c-red mlook-b mlook-u") {
		t.Fatalf("boss row: %s", r)
	}
	if r := row(p, fresh); !strings.Contains(r, "mlook c-blue") || strings.Contains(r, "mlook-b") {
		t.Fatalf("unread row: %s", r)
	}
	if r := row(p, plain); strings.Contains(r, "mlook") {
		t.Fatalf("plain row: %s", r)
	}

	// order: «Начальник» up — a letter of the boss unread is red now; off — it is blue
	boss2 := letter(4, "boss@go24.test", "Срочно", false)
	if r := row(page("/mail?folder="+inbox), boss2); !strings.Contains(r, "c-blue") {
		t.Fatalf("unread boss before the order: %s", r)
	}
	do(eve, "POST", "/mail/formats/"+list[1].ID+"/do", url.Values{"do": {"up"}}, 404)
	do(anna, "POST", "/mail/formats/"+list[1].ID+"/do", url.Values{"do": {"up"}}, 303)
	if r := row(page("/mail?folder="+inbox), boss2); !strings.Contains(r, "c-red") {
		t.Fatalf("unread boss after the order: %s", r)
	}
	do(anna, "POST", "/mail/formats/"+list[1].ID+"/do", url.Values{"do": {"off"}}, 303)
	if p = page("/mail?folder=" + inbox); !strings.Contains(row(p, boss2), "c-blue") || strings.Contains(row(p, boss), "mlook") {
		t.Fatal("rule off still applies")
	}
	if p = page("/mail/settings?tab=formats"); !strings.Contains(p, "Начальник") || !strings.Contains(p, "mrule off") || !strings.Contains(p, "boss@go24.test") {
		t.Fatalf("tab: %.3000s", p)
	}

	// the form of a rule; nobody else opens, changes or deletes it
	if p = page("/mail/settings?tab=formats&edit=" + list[1].ID); !strings.Contains(p, `name="if_from" value="boss@go24.test"`) || !strings.Contains(p, `name="bold" value="1" checked`) {
		t.Fatal("edit form")
	}
	do(eve, "GET", "/mail/settings?tab=formats&edit="+list[1].ID, nil, 404)
	do(eve, "POST", "/mail/formats/"+list[1].ID+"/save", url.Values{"name": {"x"}, "unread": {"1"}, "color": {"red"}}, 404)
	do(eve, "POST", "/mail/formats/"+list[1].ID+"/do", url.Values{"do": {"delete"}}, 404)
	do(anna, "POST", "/mail/formats/"+list[1].ID+"/save", url.Values{"name": {"Шеф"}, "enabled": {"1"}, "if_from": {"boss@"}, "color": {"green"}, "italic": {"1"}}, 303)
	if r := row(page("/mail?folder="+inbox), boss); !strings.Contains(r, "mlook c-green mlook-i") {
		t.Fatalf("changed rule: %s", r)
	}

	// the REST API
	tok, err := keys.Create(ctx, anna, "k", []string{"mail:read", "mail:write"}, time.Hour)
	must(err)
	api := func(method, path, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return string(raw)
	}
	var got []mail.Format
	must(json.Unmarshal([]byte(api("GET", "/api/v1/mail/formats", "", 200)), &got))
	if len(got) != 2 || got[0].Name != "Шеф" || got[0].Look.Color != "green" {
		t.Fatalf("api formats = %+v", got)
	}
	var made struct{ ID string }
	must(json.Unmarshal([]byte(api("POST", "/api/v1/mail/formats", `{"name":"Магазин","enabled":true,"if":{"from":"shop.test"},"look":{"color":"purple"}}`, 200)), &made))
	api("POST", "/api/v1/mail/formats", `{"name":"Без условий","enabled":true,"look":{"color":"purple"}}`, 422)
	api("POST", "/api/v1/mail/formats/"+made.ID, `{"move":-1}`, 200)
	var letters []map[string]any
	must(json.Unmarshal([]byte(api("GET", "/api/v1/mail/messages?folder="+inbox, "", 200)), &letters))
	looks := map[string]string{}
	for _, l := range letters {
		if lk, ok := l["look"].(map[string]any); ok {
			looks[l["id"].(string)] = lk["color"].(string)
		}
	}
	if looks[boss] != "green" || looks[plain] != "purple" || looks[fresh] != "blue" {
		t.Fatalf("api looks = %v", looks)
	}
	api("DELETE", "/api/v1/mail/formats/"+made.ID, "", 200)
	if got, _ := srv.Mail.Formats(ctx, eve); len(got) != 0 {
		t.Fatal("eve has rules")
	}
	if got, _ := srv.Mail.Formats(ctx, anna); len(got) != 2 {
		t.Fatalf("after delete: %+v", got)
	}
}
