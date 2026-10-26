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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailQuickHTTP drives «Быстрые действия» and «Игнорировать беседу» over HTTP against the test database (go24's
// own folders, no mail server): the defaults in the menu and the settings, a quick step made in the form and done
// from the toolbar, one that answers with a template and deletes, a conversation ignored and back; nobody else
// gets any of it.
func TestMailQuickHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folder := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		return id
	}
	inbox, bills, trash := folder("INBOX", "inbox"), folder("Bills", ""), folder("Trash", "trash")
	folder("Drafts", "drafts")
	letter := func(uid int, subject, mid, thread string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at,
			message_id, thread) VALUES ($1, $2, $3, 'ivan@client.test', 'Иван', 'anna@go24.test', $4, 'текст', now(), $5, $6) RETURNING id::text`,
			box, inbox, uid, subject, mid, thread).Scan(&id))
		return id
	}
	invoice := letter(1, "Счёт", "inv@client.test", "inv@client.test")
	hello := letter(2, "Привет", "hi@client.test", "hi@client.test")
	again := letter(3, "Re: Привет", "hi2@client.test", "hi@client.test")
	news := letter(4, "Новости", "news@client.test", "news@client.test")

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
	roleOf := func(subject string) string {
		var role string
		must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.subject = $1`, subject).Scan(&role))
		return role
	}

	// Outlook's «Готово» and «Ответить и удалить» are in the menu and in the settings
	p := page("/mail?folder=" + inbox + "&letter=" + invoice)
	list, err := srv.Mail.QuickSteps(ctx, anna)
	must(err)
	if len(list) != 2 || !strings.Contains(p, `value="quick:`+list[0].ID+`"`) || !strings.Contains(p, `value="ignore"`) {
		t.Fatalf("quick steps %+v; the toolbar: %.3000s", list, p)
	}
	if p = page("/mail/settings?tab=quick"); !strings.Contains(p, list[0].Name) || !strings.Contains(p, list[1].Name) {
		t.Fatalf("quick tab: %.3000s", p)
	}

	// one of her own: a category and a folder; checked
	cats, err := srv.Mail.Categories(ctx, anna, [2]string{"red", "Бухгалтерия"})
	must(err)
	if p = page("/mail/settings?tab=quick&edit=new"); !strings.Contains(p, `name="do_0"`) || !strings.Contains(p, `name="folder_0"`) {
		t.Fatal("the form of a new quick step")
	}
	if _, res := do(anna, "POST", "/mail/quicksteps", url.Values{"name": {"Два"}, "do_0": {"archive"}, "do_1": {"delete"}}, 303); !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("two filing steps saved")
	}
	do(anna, "POST", "/mail/quicksteps", url.Values{"name": {"В счета"}, "do_0": {"category"}, "cat_0": {cats[0].ID}, "folder_0": {inbox},
		"do_1": {"move"}, "folder_1": {bills}, "cat_1": {cats[0].ID}, "do_2": {""}}, 303)
	list, err = srv.Mail.QuickSteps(ctx, anna)
	must(err)
	mine := list[2]
	if len(list) != 3 || mine.Name != "В счета" || len(mine.Steps) != 2 || mine.Steps[0].Arg != cats[0].ID || mine.Steps[1].Arg != bills {
		t.Fatalf("saved: %+v", list)
	}
	if p = page("/mail/settings?tab=quick&edit=" + mine.ID); !strings.Contains(p, `value="В счета"`) {
		t.Fatal("the edit form")
	}
	do(eve, "GET", "/mail/settings?tab=quick&edit="+mine.ID, nil, 404)
	do(eve, "POST", "/mail/quicksteps/"+mine.ID+"/save", url.Values{"name": {"x"}, "do_0": {"read"}}, 404)
	do(eve, "POST", "/mail/quicksteps/"+mine.ID+"/do", url.Values{"do": {"delete"}}, 404)
	do(anna, "POST", "/mail/quicksteps/"+mine.ID+"/do", url.Values{"do": {"up"}}, 303)
	do(eve, "POST", "/mail/act", url.Values{"letter": {invoice}, "do": {"quick:" + mine.ID}, "back": {"/mail"}}, 404)
	_, res := do(anna, "POST", "/mail/act", url.Values{"letter": {invoice}, "do": {"quick:" + mine.ID},
		"back": {"/mail?folder=" + inbox + "&letter=" + invoice}}, 303)
	if loc := res.Header.Get("Location"); strings.Contains(loc, "letter=") || roleOf("Счёт") != "" {
		t.Fatalf("after the quick step: %s, in %q", loc, roleOf("Счёт"))
	}
	var folderOf string
	var catsOf []string
	must(pool.QueryRow(ctx, `SELECT folder_id::text, categories::text[] FROM mail_messages WHERE subject = 'Счёт'`).Scan(&folderOf, &catsOf))
	if folderOf != bills || len(catsOf) != 1 {
		t.Fatalf("the letter is in %s with %v", folderOf, catsOf)
	}

	// «Ответить и удалить» with a template: the reply opens as a draft, the letter goes to «Удалённые»
	tpl, err := srv.Mail.SaveSnippet(ctx, anna, "template", "", "Спасибо", "<p>Спасибо, получили!</p>")
	must(err)
	var rd mail.QuickStep
	for _, q := range list {
		if _, ok := q.Reply(); ok {
			rd = q
		}
	}
	rd.Steps[0].Arg = tpl
	_, err = srv.Mail.SaveQuickStep(ctx, anna, rd)
	must(err)
	_, res = do(anna, "POST", "/mail/act", url.Values{"letter": {news}, "do": {"quick:" + rd.ID}, "back": {"/mail?folder=" + inbox}}, 303)
	loc := res.Header.Get("Location")
	if !strings.Contains(loc, "draft=") || roleOf("Новости") != "trash" {
		t.Fatalf("«Ответить и удалить»: %s, the letter in %q", loc, roleOf("Новости"))
	}
	if p = page(loc); !strings.Contains(p, "Спасибо, получили!") || !strings.Contains(p, "Re: Новости") {
		t.Fatalf("the reply: %.3000s", p)
	}

	// «Игнорировать беседу» and back
	do(eve, "POST", "/mail/act", url.Values{"letter": {hello}, "do": {"ignore"}, "back": {"/mail"}}, 404)
	do(anna, "POST", "/mail/act", url.Values{"letter": {hello}, "do": {"ignore"}, "back": {"/mail?folder=" + inbox + "&letter=" + hello}}, 303)
	if roleOf("Привет") != "trash" || roleOf("Re: Привет") != "trash" {
		t.Fatalf("ignored: %q, %q", roleOf("Привет"), roleOf("Re: Привет"))
	}
	if p = page("/mail?folder=" + trash + "&letter=" + again); !strings.Contains(p, `value="unignore"`) {
		t.Fatal("no «Не игнорировать» on an ignored letter")
	}
	do(anna, "POST", "/mail/act", url.Values{"letter": {again}, "do": {"unignore"}, "back": {"/mail?folder=" + trash}}, 303)
	if roleOf("Привет") != "inbox" || roleOf("Re: Привет") != "inbox" {
		t.Fatalf("back: %q, %q", roleOf("Привет"), roleOf("Re: Привет"))
	}
}
