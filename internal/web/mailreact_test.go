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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailReactHTTP drives the reactions against the test database: a colleague's letter takes them, an outside
// one does not; Анна's 👍 shows under the letter to Ева who has it too and tells Борис, its sender, with the link to
// his own copy; the same reaction again takes it away; another reaction replaces the person's own; REST reads and
// sets them, an unknown reaction is refused.
func TestMailReactHTTP(t *testing.T) {
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
	user := func(sub, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, boris, eve := user("anna", "Анна Петрова"), user("boris", "Борис"), user("eve", "Ева")
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), I18n: bundle, Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Mail.Tr = func(key string, args ...any) string { return bundle.T("ru-RU", key, args...) }
	folder := map[string]string{}
	for _, u := range []string{anna, boris, eve} {
		var email string
		must(pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, u).Scan(&email))
		_, err = srv.Mail.Add(ctx, u, mail.Settings{Email: email, IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
		must(err)
		role := "inbox"
		if u == boris {
			role = "sent"
		}
		var f string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) SELECT m.id, $2, $2, true
			FROM mailboxes m WHERE m.email = $1 RETURNING id::text`, email, role).Scan(&f))
		folder[u] = f
	}
	letter := func(who string, uid int, from, mid string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, cc_addr, subject, body, sent_at, thread, message_id)
			SELECT mailbox_id, id, $2::int, $3, '', 'Анна <anna@go24.test>', 'eve@go24.test', 'Отчёт', 'Готово', now(), $4, $4
			FROM mail_folders WHERE id::text = $1 RETURNING id::text`, folder[who], uid, from, mid).Scan(&id))
		return id
	}
	annaCopy, borisCopy, eveCopy := letter(anna, 1, "boris@go24.test", "r1@go24.test"), letter(boris, 1, "boris@go24.test", "r1@go24.test"), letter(eve, 1, "boris@go24.test", "r1@go24.test")
	outside := letter(anna, 2, "john@client.test", "x1@client.test")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, boris, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "text/html")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, b)
		}
		return string(b)
	}
	react := func(who, id, kind string) mail.Reactions {
		t.Helper()
		var rs mail.Reactions
		must(json.Unmarshal([]byte(do(who, "POST", "/mail/letters/"+id+"/react", url.Values{"kind": {kind}}, 200)), &rs))
		return rs
	}
	read := func(who, id string) string { return do(who, "GET", "/mail/letters/"+id+"?folder="+folder[who], nil, 200) }

	if p := read(anna, annaCopy); !strings.Contains(p, `data-mreact`) || !strings.Contains(p, `value="party"`) {
		t.Fatal("a colleague's letter offers no reactions")
	}
	if p := read(anna, outside); strings.Contains(p, `data-mreact`) {
		t.Fatal("an outside letter offers reactions")
	}
	do(anna, "POST", "/mail/letters/"+outside+"/react", url.Values{"kind": {"like"}}, 422)

	rs := react(anna, annaCopy, "like")
	if !rs.Open || rs.Mine != "like" || len(rs.List) != 1 || rs.List[0].Emoji != "👍" || rs.List[0].Count != 1 || rs.List[0].Who[0] != "Анна Петрова" {
		t.Fatalf("after 👍: %+v", rs)
	}
	var body, link string
	must(pool.QueryRow(ctx, `SELECT body, url FROM notifications WHERE user_id = $1`, boris).Scan(&body, &link))
	if !strings.Contains(body, "Анна Петрова") || !strings.Contains(body, "👍") || !strings.Contains(body, "Отчёт") || !strings.Contains(link, "letter="+borisCopy) {
		t.Fatalf("Борис is told: %q %q", body, link)
	}
	if p := read(eve, eveCopy); !strings.Contains(p, `title="Анна Петрова"`) || !strings.Contains(p, "👍 <span>1</span>") {
		t.Fatal("Ева does not see Анна's reaction")
	}
	rs = react(eve, eveCopy, "heart")
	if rs.Mine != "heart" || len(rs.List) != 2 || rs.List[0].Kind != "like" || rs.List[1].Kind != "heart" {
		t.Fatalf("after ❤️ of Ева: %+v", rs)
	}
	if rs = react(eve, eveCopy, "like"); rs.Mine != "like" || len(rs.List) != 1 || rs.List[0].Count != 2 {
		t.Fatalf("Ева's 👍 in place of ❤️: %+v", rs)
	}
	if rs = react(anna, annaCopy, "like"); rs.Mine != "" || len(rs.List) != 1 || rs.List[0].Who[0] != "Ева" {
		t.Fatalf("Анна's 👍 again takes it away: %+v", rs)
	}
	if p := read(boris, borisCopy); !strings.Contains(p, `title="Ева"`) {
		t.Fatal("the sender does not see the reactions in «Отправленные»")
	}
	react(boris, borisCopy, "party") // the sender's own reaction tells nobody
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&told))
	if told != 3 { // Анна's 👍, Ева's ❤️ and her 👍 in its place
		t.Fatalf("notifications: %d, want 3", told)
	}

	// REST
	api := func(who, method, path, body string, want int, out any) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		if out != nil {
			must(json.Unmarshal(b, out))
		}
	}
	api(anna, "POST", "/api/v1/mail/messages/"+annaCopy+"/reactions", `{"reaction":"wow"}`, 200, &rs)
	if rs.Mine != "wow" || len(rs.List) != 3 {
		t.Fatalf("REST 😮: %+v", rs)
	}
	api(anna, "POST", "/api/v1/mail/messages/"+annaCopy+"/reactions", `{"reaction":"xx"}`, 422, nil)
	api(eve, "GET", "/api/v1/mail/messages/"+annaCopy+"/reactions", "", 404, nil)
	api(eve, "GET", "/api/v1/mail/messages/"+eveCopy+"/reactions", "", 200, &rs)
	if rs.Mine != "like" || len(rs.List) != 3 {
		t.Fatalf("REST GET: %+v", rs)
	}
	api(anna, "POST", "/api/v1/mail/messages/"+annaCopy+"/reactions", `{"reaction":""}`, 200, &rs)
	if rs.Mine != "" || len(rs.List) != 2 {
		t.Fatalf("REST none: %+v", rs)
	}
}
