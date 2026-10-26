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
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailRecall: «Отправить повторно» and «Отозвать сообщение» in the menu of a sent letter only, the recall with
// its results over the letter, «…и заменить новым сообщением» — the letter again in the editor, as it went.
func TestMailRecall(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, sent, letter, own string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'Sent', 'sent') RETURNING id`, box).Scan(&sent))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, message_id, thread, voting)
		VALUES ($1, $2, 1, 'boss@client.test', 'Пётр', 'anna@go24.test', 'Отчёт', 'Утверждаем?', now(), 'q1@client.test', 'q1@client.test', 'Утвердить;Отклонить')
		RETURNING id`, box, inbox).Scan(&letter))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, cc_addr, subject, body, sent_at, message_id, thread, voting)
		VALUES ($1, $2, 2, 'anna@go24.test', 'oleg@go24.test', 'pavel@people.test', 'Бюджет', 'Да?', now() - interval '1 hour', 'b1@go24.test', 'b1@go24.test', 'Да;Нет')
		RETURNING id`, box, sent).Scan(&own))
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET bcc_addr = 'boss@people.test', importance = 1, body_html = '<p>Да?</p>' WHERE id = $1`, own)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Черновики', 'drafts', true)`, box)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values, want int) string {
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
		return string(raw)
	}
	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("no %s", w)
			}
		}
	}
	hasNot := func(page string, nope ...string) {
		t.Helper()
		for _, w := range nope {
			if strings.Contains(page, w) {
				t.Errorf("unexpected %s", w)
			}
		}
	}

	page := do("GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	hasNot(page, "mode=resend", "/recall")
	page = do("GET", "/mail?folder="+sent+"&letter="+own, nil, 200)
	has(page, "&mode=resend", `action="/mail/letters/`+own+`/recall"`, `name="do" value="delete"`, `name="do" value="replace"`, "Отозвать сообщение", "Отправить повторно")
	hasNot(page, "mrecall-external")

	do("POST", "/mail/letters/"+letter+"/recall", url.Values{"do": {"delete"}}, 303) // a letter come in: back with the error
	if list, _ := srv.Mail.Recalls(ctx, anna, "q1@client.test"); len(list) != 0 {
		t.Fatalf("a letter come in was recalled: %+v", list)
	}
	req, _ := http.NewRequest("POST", hs.URL+"/mail/letters/"+own+"/recall", strings.NewReader("do=replace"))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	must(err)
	res.Body.Close()
	if loc := res.Header.Get("Location"); res.StatusCode != 303 || !strings.Contains(loc, "mode=resend") || !strings.Contains(loc, "letter="+own) {
		t.Fatalf("recall: %d %s", res.StatusCode, loc)
	}
	page = do("GET", "/mail?folder="+sent+"&letter="+own, nil, 200)
	has(page, "Отзыв сообщения", "<td>oleg@go24.test</td>", "<td>pavel@people.test</td>", "<td>boss@people.test</td>", "Не удалось: адрес вне go24")

	ed := do("GET", "/mail/compose?letter="+own+"&mode=resend&inline=1", nil, 200)
	has(ed, `name="mode" value="resend"`, `value="Бюджет"`, "oleg@go24.test", "pavel@people.test", "boss@people.test", "&lt;p&gt;Да?&lt;/p&gt;")
	hasNot(ed, "Re: Бюджет", "Fwd: Бюджет")
}
