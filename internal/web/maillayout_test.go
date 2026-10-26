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

// TestMailLayout: «Макет» — the defaults as in Outlook (cozy, medium text, a line of text, sender pictures, the
// names of the files), a bad choice not kept; the list follows the saved choices.
func TestMailLayout(t *testing.T) {
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
	var inbox, letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at)
		VALUES ($1, $2, 1, 'boss@client.test', 'Пётр Начальников', 'anna@go24.test', 'Отчёт', 'Текст письма для просмотра', now()) RETURNING id`, box, inbox).Scan(&letter))
	for _, name := range []string{"a.pdf", "b.docx", "c.xlsx", "d.txt"} {
		_, err := pool.Exec(ctx, `INSERT INTO files (object_key, name, content_type, size, mail_id) VALUES ('mail/test/' || $1, $1, 'application/octet-stream', 10, $2)`, name, letter)
		must(err)
	}

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

	page := do("GET", "/mail/settings?tab=layout", nil, 200)
	has(page, `action="/mail/settings/layout"`, `value="cozy" checked`, `value="medium" checked`, `name="show_text" value="1" checked`,
		`name="show_pics" value="1" checked`, `name="show_files" value="1" checked`)
	list := do("GET", "/mail?folder="+inbox, nil, 200)
	has(list, "mtext-medium", "mdense-cozy", `title="Пётр Начальников"`, "Текст письма для просмотра", `class="mrow-files"`, "a.pdf", "c.xlsx")
	hasNot(list, "d.txt") // three names and «ещё 1»

	do("POST", "/mail/settings/layout", url.Values{"density": {"tight"}, "text_size": {"medium"}}, 303)
	if l, _ := srv.Mail.Layout(ctx, anna); l.Density != "cozy" || !l.ShowFiles {
		t.Errorf("a bad choice is kept: %+v", l)
	}
	do("POST", "/mail/settings/layout", url.Values{"density": {"compact"}, "text_size": {"large"}}, 303)
	if l, _ := srv.Mail.Layout(ctx, anna); l != (mail.Layout{Density: "compact", TextSize: "large"}) {
		t.Errorf("layout = %+v", l)
	}
	list = do("GET", "/mail?folder="+inbox, nil, 200)
	has(list, "mtext-large", "mdense-compact", `data-text-size="large"`, "Отчёт")
	hasNot(list, `title="Пётр Начальников"`, "Текст письма для просмотра", `class="mrow-files"`)
}
