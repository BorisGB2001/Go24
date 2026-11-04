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
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/kb"
	"go24/internal/mail"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMailShare: «Поделиться» of a letter — a post of the feed to all or to the people of the correspondence, the
// letter's mark of where it went; a chat needs the messenger.
func TestMailShare(t *testing.T) {
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
	var anna, bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Mail: mail.NewStore(pool, "x"), Notify: notify.NewStore(pool), Feed: feed.NewStore(pool, ts),
		Storages: storages.NewStore(pool, nil, nil), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body, sent_at)
		VALUES ($1, $2, 1, 'boss@client.test', 'Босс', 'anna@go24.test', 'bob@go24.test', 'Re: Счёт за октябрь', 'Оплатите до пятницы', now()) RETURNING id`,
		box, inbox).Scan(&letter))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, http.Header) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	share := "/mail/letters/" + letter + "/share"

	page, _ := do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	for _, want := range []string{`action="` + share + `"`, `name="users" value="` + bob + `"`, "data-share-to"} {
		if !strings.Contains(page, want) {
			t.Errorf("the letter has no %s", want)
		}
	}
	if strings.Contains(page, `data-share-rooms`) {
		t.Error("a chat is offered without the messenger")
	}

	// to all: the post carries the subject without «Re:», the note and the letter's text
	_, hdr := do(anna, "POST", share, url.Values{"do": {"post"}, "note": {"Коллеги, к сведению"}, "folder": {inbox}}, 303)
	if !strings.Contains(hdr.Get("Location"), "msg=") {
		t.Errorf("after the post: %s", hdr.Get("Location"))
	}
	var post, title, body string
	must(pool.QueryRow(ctx, `SELECT id::text, title, body FROM feed_posts WHERE author_id = $1`, anna).Scan(&post, &title, &body))
	if title != "Счёт за октябрь" || !strings.HasPrefix(body, "Коллеги, к сведению") || !strings.Contains(body, "Оплатите до пятницы") ||
		!strings.Contains(body, "boss@client.test") {
		t.Errorf("the post: %q %q", title, body)
	}
	page, _ = do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	if !strings.Contains(page, "/feed#post-"+post) {
		t.Error("the letter does not show its post")
	}

	// to the people of the correspondence: none chosen — an error; chosen — the post is theirs only
	do(anna, "POST", share, url.Values{"do": {"post"}, "to": {"people"}}, 303)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM feed_posts`).Scan(&n))
	if n != 1 {
		t.Errorf("a post to nobody: %d posts", n)
	}
	do(anna, "POST", share, url.Values{"do": {"post"}, "to": {"people"}, "users": {bob}}, 303)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_letter_shares WHERE message_id = $1 AND kind = 'post'`, letter).Scan(&n))
	if n != 2 {
		t.Errorf("mail_letter_shares: %d", n)
	}

	// a chat without the messenger, someone else's letter, an unknown action
	do(anna, "POST", share, url.Values{"do": {"chat"}}, 303)
	do(eve, "POST", share, url.Values{"do": {"post"}}, 404)
	do(anna, "POST", share, url.Values{"do": {"x"}}, 303)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_letter_shares`).Scan(&n))
	if n != 2 {
		t.Errorf("mail_letter_shares after the refusals: %d", n)
	}
	if rooms, _ := do(anna, "GET", "/mail/share/rooms", nil, 200); strings.TrimSpace(rooms) != "[]" {
		t.Errorf("rooms = %s", rooms)
	}
}

// TestMailShareKB: «Поделиться → В базу знаний», as Outlook's «Отправить в OneNote» — a page of a base the user
// writes in, with the letter's head and text; the letter shows its page; a base one cannot write in is refused.
func TestMailShareKB(t *testing.T) {
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
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Mail: mail.NewStore(pool, "x"), Notify: notify.NewStore(pool), Feed: feed.NewStore(pool, ts),
		KB: kb.NewStore(pool), Storages: storages.NewStore(pool, nil, nil), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body, sent_at)
		VALUES ($1, $2, 1, 'boss@client.test', 'Босс', 'anna@go24.test', 'bob@go24.test', 'Re: Регламент отгрузок',
		E'Порядок такой:\n# не заголовок\n- сначала счёт\n- потом отгрузка', now()) RETURNING id`, box, inbox).Scan(&letter))
	wiki, err := srv.KB.CreateSpace(ctx, anna, "Регламенты", "", "")
	must(err)
	closed, err := srv.KB.CreateSpace(ctx, eve, "Закрытая", "", "")
	must(err)
	must(srv.KB.SetRight(ctx, eve, closed, "user", eve, "full")) // only Eve's now

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values, want int) (string, http.Header) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	share := "/mail/letters/" + letter + "/share"
	page, _ := do("GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	if !strings.Contains(page, `name="do" value="kb"`) || !strings.Contains(page, `<option value="`+wiki+`">Регламенты</option>`) ||
		strings.Contains(page, closed) {
		t.Error("the menu does not offer exactly the bases Anna writes in")
	}

	_, hdr := do("POST", share, url.Values{"do": {"kb"}, "space": {wiki}, "title": {""}, "note": {"Для новичков"}, "folder": {inbox}}, 303)
	var id, title, body string
	must(pool.QueryRow(ctx, `SELECT id::text, title, body FROM kb_pages WHERE space_id = $1`, wiki).Scan(&id, &title, &body))
	if hdr.Get("Location") != "/kb/"+wiki+"/"+id {
		t.Errorf("after the page: %s", hdr.Get("Location"))
	}
	for _, want := range []string{"Для новичков", "Босс <boss@client.test>", "anna@go24.test", "bob@go24.test", "Порядок такой:",
		"​# не заголовок", "- сначала счёт"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page has no %q: %s", want, body)
		}
	}
	if title != "Регламент отгрузок" {
		t.Errorf("title = %q", title)
	}
	if html := string(kb.Render(body)); strings.Contains(html, "<h2") || !strings.Contains(html, "<li>сначала счёт</li>") {
		t.Errorf("the page renders as %s", html)
	}
	page, _ = do("GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	if !strings.Contains(page, `href="/kb/`+wiki+"/"+id+`"`) {
		t.Error("the letter does not show its page")
	}

	// a base closed to Anna, an unknown one: no page
	do("POST", share, url.Values{"do": {"kb"}, "space": {closed}}, 404)
	do("POST", share, url.Values{"do": {"kb"}, "space": {"00000000-0000-0000-0000-000000000000"}}, 404)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM kb_pages`).Scan(&n))
	if n != 1 {
		t.Errorf("%d pages after the refusals", n)
	}
}
