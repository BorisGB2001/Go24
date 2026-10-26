package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMailImportHTTP drives «Импорт из файла» over HTTP against the test database: the page, the form's checks, a
// file from the computer kept on the drive and queued, the drive's file offered next time, nobody else's mailbox
// reached; go24 worker failing on an unreachable server marks the import failed and tells the user. (The letters
// themselves — TestFileImport of the mail.)
func TestMailImportHTTP(t *testing.T) {
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
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Mail: mail.NewStore(pool, "x"), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, body io.Reader, ctype string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	// form posts the fields and, last, the file
	form := func(who string, fields map[string]string, name, data string) (int, string) {
		t.Helper()
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		for _, k := range []string{"box", "where", "into", "dupes", "drive_file"} {
			if v, ok := fields[k]; ok {
				w.WriteField(k, v)
			}
		}
		if name != "" {
			fw, _ := w.CreateFormFile("file", name)
			fw.Write([]byte(data))
		}
		w.Close()
		code, _, loc := do(who, "POST", "/mail/import", &b, w.FormDataContentType())
		return code, loc
	}
	count := func() int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_imports`).Scan(&n))
		return n
	}

	code, page, _ := do(anna, "GET", "/mail/import?folder="+inbox, nil, "")
	if code != 200 || !strings.Contains(page, `value="`+inbox+`" selected`) || !strings.Contains(page, "js/mailimport.js") || strings.Contains(page, "data-import-drive") {
		t.Fatalf("import page: %d %.3000s", code, page)
	}
	if code, loc := form(anna, map[string]string{"box": box, "where": "same"}, "", ""); code != 303 || !strings.Contains(loc, "msg=") || count() != 0 {
		t.Fatalf("no file: %d %s", code, loc)
	}
	letter := "From: c@x.ru\r\nTo: anna@go24.test\r\nSubject: hi\r\nMessage-ID: <1@x.ru>\r\n\r\ntext\r\n"
	if code, loc := form(eve, map[string]string{"box": box}, "a.eml", letter); code < 300 || count() != 0 {
		t.Fatalf("someone else's mailbox: %d %s", code, loc)
	}
	if code, loc := form(anna, map[string]string{"box": box}, "notes.txt", "hello"); code != 303 || !strings.Contains(loc, "msg=") || count() != 0 {
		t.Fatalf("not mail: %d %s", code, loc)
	}
	if s3ep == "" {
		t.Log("no GO24_TEST_S3: the file from the computer is not checked")
		return
	}
	code, loc := form(anna, map[string]string{"box": box, "where": "folder", "into": inbox, "dupes": "allow"}, "письмо.eml", letter)
	if code != 303 || !strings.HasPrefix(loc, "/mail/import?box="+box) {
		t.Fatalf("import: %d %s", code, loc)
	}
	list, err := srv.Mail.Imports(ctx, anna)
	must(err)
	if len(list) != 1 || list[0].Status != "queued" || list[0].Format != "eml" || list[0].FolderID != inbox || !list[0].Dupes || list[0].FileID == "" || list[0].Name != "письмо.eml" {
		t.Fatalf("imports: %+v", list)
	}
	var folder string
	must(pool.QueryRow(ctx, `SELECT d.name FROM files f JOIN drive_folders d ON d.id = f.folder_id WHERE f.id = $1`, list[0].FileID).Scan(&folder))
	if folder != "Импорт почты" {
		t.Fatalf("the file is kept in %q", folder)
	}
	// the file on the drive is offered; imported from it once more
	if _, page, _ := do(anna, "GET", "/mail/import", nil, ""); !strings.Contains(page, "data-export-busy") || !strings.Contains(page, `value="`+list[0].FileID+`"`) {
		t.Fatal("the page does not offer the drive's file or wait for the import")
	}
	if code, _ := form(eve, map[string]string{"box": box, "drive_file": list[0].FileID}, "", ""); code < 300 || count() != 1 {
		t.Fatal("someone else's file imported")
	}
	if code, _ := form(anna, map[string]string{"box": box, "where": "same", "drive_file": list[0].FileID}, "", ""); code != 303 || count() != 2 {
		t.Fatal("the drive's file not imported")
	}

	if errs := srv.RunMailImports(ctx); len(errs) != 0 {
		t.Fatal(errs)
	}
	e, err := srv.Mail.GetImport(ctx, anna, list[0].ID)
	if err != nil || e.Status != "failed" || e.Error == "" || e.FinishedAt == nil {
		t.Fatalf("import on an unreachable server: %+v %v", e, err)
	}
	var note string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'mail' LIMIT 1`, anna).Scan(&note))
	if !strings.Contains(note, "письмо.eml") {
		t.Fatalf("notification: %q", note)
	}
	if code, _, _ := do(eve, "POST", "/mail/import/"+e.ID+"/delete", nil, ""); code == 303 {
		if _, err := srv.Mail.GetImport(ctx, anna, e.ID); err != nil {
			t.Fatal("someone else's import deleted")
		}
	}
	if code, _, _ := do(anna, "POST", "/mail/import/"+e.ID+"/delete", nil, ""); code != 303 {
		t.Fatalf("delete: %d", code)
	}
	if list, _ := srv.Mail.Imports(ctx, anna); len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}
}
