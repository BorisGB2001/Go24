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

// TestMailNoForwardHTTP: a letter «Не пересылать» in the inbox has the bar and the lock in the list, no «Переслать»,
// printing or source; the server refuses them and sharing too; an answer is restricted; the author's own copy in
// «Отправленные» is printed. The editor offers the option.
func TestMailNoForwardHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
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
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(cookie)
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
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
	var inbox, sentF, locked, own string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'INBOX', 'inbox', true) RETURNING id::text`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Sent', 'sent', true) RETURNING id::text`, box).Scan(&sentF))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at, no_forward)
		VALUES ($1, $2, 1, 'boss@go24.test', 'anna@go24.test', 'Секрет', 'только тебе', now(), true) RETURNING id::text`, box, inbox).Scan(&locked))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at, no_forward)
		VALUES ($1, $2, 2, 'anna@go24.test', 'boss@go24.test', 'Мой секрет', 'моё', now(), true) RETURNING id::text`, box, sentF).Scan(&own))

	l := do("GET", "/mail/letters/"+locked+"?folder="+inbox, nil, 200)
	if !strings.Contains(l, "data-nofwd") || !strings.Contains(l, "mnocopy") {
		t.Fatalf("no restriction bar: %.3000s", l)
	}
	for _, no := range []string{"mode=fwd", "/mail/letters/" + locked + "/print\"", "/mail/letters/" + locked + "/source"} {
		if strings.Contains(l, no) {
			t.Errorf("a restricted letter offers %q", no)
		}
	}
	if page := do("GET", "/mail?folder="+inbox, nil, 200); !strings.Contains(page, "mnofwd") {
		t.Error("no lock in the list")
	}
	do("GET", "/mail/letters/"+locked+"/print", nil, 403)
	do("GET", "/mail/letters/"+locked+"/source", nil, 403)
	do("GET", "/mail/compose?letter="+locked+"&mode=fwd", nil, 422)
	do("POST", "/mail/drafts/save", url.Values{"box": {box}, "mode": {"fwdatt"}, "reply_to": {locked}}, 422)
	do("POST", "/mail/letters/"+locked+"/share", url.Values{"do": {"chat"}}, 422)
	if ed := do("GET", "/mail/compose?letter="+locked+"&mode=reply", nil, 200); !strings.Contains(ed, `name="no_forward" value="1" data-nofwd checked`) {
		t.Fatalf("the answer is not restricted: %.3000s", ed)
	}

	// the author's own copy: the bar says so, it is printed and forwarded
	l = do("GET", "/mail/letters/"+own+"?folder="+sentF, nil, 200)
	if !strings.Contains(l, "data-nofwd") || strings.Contains(l, "mnocopy") || !strings.Contains(l, "mode=fwd") {
		t.Fatalf("the sent copy: %.3000s", l)
	}
	do("GET", "/mail/letters/"+own+"/print", nil, 200)
	if ed := do("GET", "/mail?new=1&box="+box, nil, 200); !strings.Contains(ed, `name="no_forward"`) {
		t.Fatal("the editor has no «Не пересылать»")
	}
}
