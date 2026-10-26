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

// TestMailExportHTTP drives «Экспорт в файл» over HTTP against the test database: the page with the mailbox's
// folders, the form's checks, the export queued and listed, nobody else's export reached; go24 worker failing on an
// unreachable server marks the export failed and tells the user. (The files themselves — TestExport of the mail.)
func TestMailExportHTTP(t *testing.T) {
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
	var anna, eve, inbox, local string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, local) VALUES ($1, 'Своя', true) RETURNING id`, box).Scan(&local))
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}

	code, page, _ := do(anna, "GET", "/mail/export?folder="+inbox, nil)
	if code != 200 || !strings.Contains(page, `value="`+inbox+`" checked`) || strings.Contains(page, local) || !strings.Contains(page, "js/mailexport.js") {
		t.Fatalf("export page: %d %.3000s", code, page)
	}
	if code, _, loc := do(anna, "POST", "/mail/export", url.Values{"box": {box}, "scope": {"folders"}, "format": {"mbox"}}); code != 303 || !strings.Contains(loc, "msg=") {
		t.Fatalf("no folders: %d %s", code, loc)
	}
	if code, _, loc := do(anna, "POST", "/mail/export", url.Values{"box": {box}, "scope": {"folders"}, "folder": {local}, "format": {"eml"}}); code != 303 || !strings.Contains(loc, "msg=") {
		t.Fatalf("only go24's own folder: %d %s", code, loc)
	}
	if code, _, _ := do(eve, "POST", "/mail/export", url.Values{"box": {box}, "scope": {"all"}, "format": {"mbox"}}); code == 303 {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_exports`).Scan(&n))
		if n != 0 {
			t.Fatal("someone else's mailbox exported")
		}
	}
	form := url.Values{"box": {box}, "scope": {"folders"}, "folder": {inbox}, "subfolders": {"1"}, "since": {"2026-10-01"}, "until": {"2026-10-31"}, "format": {"eml"}}
	if code, _, loc := do(anna, "POST", "/mail/export", form); code != 303 || !strings.HasPrefix(loc, "/mail/export?box="+box) {
		t.Fatalf("export: %d %s", code, loc)
	}
	list, err := srv.Mail.Exports(ctx, anna)
	must(err)
	if len(list) != 1 || list[0].Status != "queued" || list[0].Format != "eml" || len(list[0].Folders) != 1 || !list[0].Subfolders ||
		list[0].Since == nil || list[0].Until.Format("2006-01-02") != "2026-10-31" {
		t.Fatalf("exports: %+v", list)
	}
	if _, page, _ := do(anna, "GET", "/mail/export", nil); !strings.Contains(page, "data-export-busy") {
		t.Fatal("the page does not wait for the export")
	}
	if code, _, _ := do(eve, "POST", "/mail/export/"+list[0].ID+"/delete", url.Values{}); code == 303 {
		if _, err := srv.Mail.GetExport(ctx, anna, list[0].ID); err != nil {
			t.Fatal("someone else's export deleted")
		}
	}

	if errs := srv.RunMailExports(ctx); len(errs) != 0 {
		t.Fatal(errs)
	}
	e, err := srv.Mail.GetExport(ctx, anna, list[0].ID)
	if err != nil || e.Status != "failed" || e.Error == "" || e.FinishedAt == nil {
		t.Fatalf("export on an unreachable server: %+v %v", e, err)
	}
	var note string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'mail'`, anna).Scan(&note))
	if !strings.Contains(note, "anna@go24.test") {
		t.Fatalf("notification: %q", note)
	}
	if code, _, _ := do(anna, "POST", "/mail/export/"+e.ID+"/delete", url.Values{}); code != 303 {
		t.Fatalf("delete: %d", code)
	}
	if list, _ := srv.Mail.Exports(ctx, anna); len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}
