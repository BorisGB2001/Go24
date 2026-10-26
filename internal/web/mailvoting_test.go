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

// TestMailVoting: the voting buttons over a letter that asks for a vote (send at once or edit the answer first), the
// answer's editor with the choice in its subject, «Отслеживание» of the user's own letter, the choice in the editor.
func TestMailVoting(t *testing.T) {
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
	_, err = pool.Exec(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, message_id, thread, vote)
		VALUES ($1, $2, 3, 'oleg@go24.test', 'Олег', 'anna@go24.test', 'Да: Бюджет', '', now(), 'b2@go24.test', 'b1@go24.test', 'Да'),
		($1, $2, 4, 'pavel@people.test', '', 'anna@go24.test', 'Нет: Бюджет', 'Дорого', now(), 'b3@people.test', 'b1@go24.test', '')`, box, inbox)
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
	has(page, "data-vote", `action="/mail/letters/`+letter+`/vote"`, `name="choice" value="Утвердить"`, `name="choice" value="Отклонить"`,
		"&mode=reply&vote=%d0%a3%d1%82", "Отправитель просит проголосовать")
	hasNot(page, "mvote-track")
	reply := do("GET", "/mail/compose?letter="+letter+"&mode=reply&inline=1&vote="+url.QueryEscape("Отклонить"), nil, 200)
	has(reply, `name="vote" value="Отклонить"`, `value="Отклонить: Отчёт"`)
	reply = do("GET", "/mail/compose?letter="+letter+"&mode=reply&inline=1&vote="+url.QueryEscape("Чужой"), nil, 200)
	has(reply, `name="vote" value=""`)
	hasNot(reply, "Чужой: Отчёт")
	do("POST", "/mail/letters/"+letter+"/vote", url.Values{"choice": {"Неизвестно"}}, 303) // not one of the choices: back with the error

	track := do("GET", "/mail?folder="+sent+"&letter="+own, nil, 200)
	has(track, "mvote-track", "Отслеживание: ответили 2 из 2", "Да: <b>1</b>", "Нет: <b>1</b>", "<td>Олег</td><td><b>Да</b></td>", "pavel@people.test",
		"Ответ: Да")
	hasNot(track, "data-vote")

	ed := do("GET", "/mail/compose?box="+box, nil, 200)
	has(ed, `name="voting"`, `<option value="Утвердить;Отклонить">`, `<option value="Да;Нет;Возможно">`)
}
