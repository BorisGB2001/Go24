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
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailStorageHTTP: «Хранилище» is a tab of the mail settings; a server out of reach is said on the tab; «Очистить»
// takes only its ages and the user's own folders (the counting and cleaning themselves: mail.TestStorage).
func TestMailStorageHTTP(t *testing.T) {
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
	var anna, eve, inbox string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, selectable) VALUES ($1, 'INBOX', 'inbox', true) RETURNING id`, box).Scan(&inbox))
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path, body string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		if body != "" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}

	code, page, _ := do(anna, "GET", "/mail/settings?tab=storage", "")
	if code != 200 || !strings.Contains(page, `class="on" href="/mail/settings?tab=storage"`) || !strings.Contains(page, "mail.invalid") {
		t.Fatalf("storage tab: %d %s", code, page)
	}
	if code, page, _ = do(eve, "GET", "/mail/settings?tab=storage", ""); code != 200 || !strings.Contains(page, bundle.T("ru-RU", "mail.no_boxes")) {
		t.Fatalf("storage tab without mailboxes: %d", code)
	}
	if _, _, loc := do(anna, "POST", "/mail/folders/"+inbox+"/clean", "months=5"); !strings.Contains(loc, "tab=storage") || !strings.Contains(loc, "msg=") {
		t.Fatalf("a bad age: %s", loc)
	}
	if code, _, _ := do(eve, "POST", "/mail/folders/"+inbox+"/clean", "months=0"); code != 404 {
		t.Fatalf("someone else emptied the folder: %d", code)
	}
}
