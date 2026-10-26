package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/mail"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

func TestZipName(t *testing.T) {
	seen := map[string]int{}
	var got []string
	for _, n := range []string{"a.pdf", "A.pdf", "../x/b", "a.pdf", ""} {
		got = append(got, zipName(n, seen))
	}
	if strings.Join(got, "|") != "a.pdf|A (2).pdf|b|a (3).pdf|file" {
		t.Errorf("zipName = %q", got)
	}
}

// TestMailMoreHTTP: a letter's files (the viewer's preview, «Скачать все»), «Печать» with its headers and policy,
// «Показать исходный текст» of go24's own letter, blocking and trusting senders, the settings «Уведомления» and
// «Нежелательная почта», the counter of the menu — and none of it for anyone but the mailbox's owner.
func TestMailMoreHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: ts, Mail: mail.NewStore(pool, "x"), Notify: notify.NewStore(pool), Drive: drive.NewStore(pool, ts),
		Storages: storages.NewStore(pool, nil, nil), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, letter, other, junkF string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Junk', 'junk', true) RETURNING id`, box).Scan(&junkF))
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, body_html, sent_at)
		VALUES ($1, $2, 5000000000, 'boss@client.test', 'Босс', 'anna@go24.test', 'Счёт', 'Оплатите', '<p>Оплатите <img src="https://track.test/p.gif"></p>', now()) RETURNING id`,
		box, inbox).Scan(&letter))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, subject, sent_at, other)
		VALUES ($1, $2, 5000000001, 'boss@client.test', 'Ещё', now(), true) RETURNING id`, box, inbox).Scan(&other))
	var f1, f2 string
	for _, f := range []struct {
		name string
		to   *string
	}{{"счёт.pdf", &f1}, {"счёт.pdf", &f2}} {
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: f.name, ContentType: "application/pdf", UploadedBy: anna, MailID: letter})
		must(err)
		*f.to = id
	}

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
		return string(raw), res.Header
	}

	// the counter: «Другие» are not counted
	if body, _ := do(anna, "GET", "/mail/unread", nil, 200); !strings.Contains(body, `"unread": 1`) {
		t.Errorf("unread = %s", body)
	}
	page, _ := do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	for _, want := range []string{`data-counter="mail"`, `data-preview="` + f1 + `"`, `data-kind="pdf"`, "/mail/letters/" + letter + "/files.zip",
		"/mail/letters/" + letter + "/print", "/mail/senders/block", "data-trust", "js/mailx.js"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page has no %s", want)
		}
	}
	if body, _ := do(anna, "GET", "/mail/files/"+f1+"/preview", nil, 200); !strings.Contains(body, `"kind": "pdf"`) {
		t.Errorf("preview = %s", body)
	}
	zipped, hdr := do(anna, "GET", "/mail/letters/"+letter+"/files.zip", nil, 200)
	zr, err := zip.NewReader(bytes.NewReader([]byte(zipped)), int64(len(zipped)))
	must(err)
	if len(zr.File) != 2 || zr.File[0].Name != "счёт.pdf" || zr.File[1].Name != "счёт (2).pdf" || !strings.Contains(hdr.Get("Content-Disposition"), ".zip") {
		t.Errorf("zip = %+v %s", zr.File, hdr.Get("Content-Disposition"))
	}
	printed, hdr := do(anna, "GET", "/mail/letters/"+letter+"/print", nil, 200)
	if !strings.Contains(printed, "boss@client.test") || !strings.Contains(printed, "<p>Оплатите") || !strings.Contains(printed, "счёт.pdf") ||
		!strings.Contains(hdr.Get("Content-Security-Policy"), "img-src 'self' data:") {
		t.Errorf("print: %s %.1500s", hdr.Get("Content-Security-Policy"), printed)
	}
	do(anna, "GET", "/mail/letters/"+letter+"/source", nil, 404) // go24's own letter: no source on the server
	for _, p := range []string{"/mail/files/" + f1 + "/preview", "/mail/files/" + f1 + "/raw", "/mail/letters/" + letter + "/files.zip",
		"/mail/letters/" + letter + "/print", "/mail/letters/" + letter + "/source", "/mail/files/bad/preview"} {
		do(eve, "GET", p, nil, 404)
	}
	do(eve, "POST", "/mail/senders/block", url.Values{"letter": {letter}}, 404)

	// «Всегда показывать от …»: the sender's pictures load, also when printed
	do(anna, "POST", "/mail/senders/safe", url.Values{"letter": {letter}}, 200)
	if _, hdr := do(anna, "GET", "/mail/letters/"+letter+"/print", nil, 200); !strings.Contains(hdr.Get("Content-Security-Policy"), "img-src * data:") {
		t.Error("a safe sender's pictures stay blocked in print")
	}
	if pane, _ := do(anna, "GET", "/mail/letters/"+letter, nil, 200); strings.Contains(pane, "data-show-pics") {
		t.Error("the banner of pictures shows for a safe sender")
	}
	// «Заблокировать отправителя»: both letters go to junk
	body, _ := do(anna, "POST", "/mail/senders/block", url.Values{"letter": {letter}}, 200)
	var res map[string]any
	must(json.Unmarshal([]byte(body), &res))
	if res["moved"] != float64(2) {
		t.Errorf("block = %s", body)
	}
	var role string
	must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id = $1`, other).Scan(&role))
	if role != "junk" {
		t.Errorf("the other letter is in %q", role)
	}

	// the settings
	do(anna, "POST", "/mail/senders", url.Values{"kind": {"blocked"}, "sender": {"spam.test"}}, 200)
	do(anna, "POST", "/mail/senders", url.Values{"kind": {"blocked"}, "sender": {"nonsense"}}, 422)
	junk, _ := do(anna, "GET", "/mail/settings?tab=junk", nil, 200)
	if !strings.Contains(junk, "@spam.test") || !strings.Contains(junk, "boss@client.test") {
		t.Errorf("junk settings: %.2000s", junk)
	}
	do(anna, "POST", "/mail/senders/delete", url.Values{"sender": {"@spam.test"}}, 200)
	do(anna, "POST", "/mail/settings/notify", url.Values{"notify": {"all"}, "sound": {"1"}}, 200)
	if p, _ := srv.Mail.NotifyPrefs(ctx, anna); p.Mode != "all" || !p.Sound || p.Desktop {
		t.Errorf("notify prefs = %+v", p)
	}
	if set, _ := do(anna, "GET", "/mail/settings?tab=notify", nil, 200); !strings.Contains(set, `value="all" checked`) {
		t.Error("the notify settings do not show the mode")
	}
	if dr, _ := do(anna, "GET", "/mail/drive?which=my", nil, 200); !strings.Contains(dr, `"which": "my"`) || !strings.Contains(dr, `"drives"`) {
		t.Errorf("drive = %s", dr)
	}
	do(anna, "POST", "/mail/drafts/x/files/drive", url.Values{"file": {f1}}, 404) // a letter's file is not a drive's

	// «Отказаться от подписки»: a newsletter with only a page — the page opens; «Также заблокировать» sends it to junk
	var news string
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, subject, sent_at, unsubscribe)
		VALUES ($1, $2, 5000000002, 'news@shop.test', 'Магазин', 'Скидки', now(), '<https://shop.test/leave?u=1>') RETURNING id`, box, inbox).Scan(&news))
	if pane, _ := do(anna, "GET", "/mail/letters/"+news, nil, 200); !strings.Contains(pane, "/mail/letters/"+news+"/unsubscribe") ||
		!strings.Contains(pane, `data-page="https://shop.test/leave?u=1"`) || !strings.Contains(pane, `name="block"`) {
		t.Errorf("the newsletter offers no unsubscribing: %.3000s", pane)
	}
	if pane, _ := do(anna, "GET", "/mail/letters/"+other, nil, 200); strings.Contains(pane, "data-unsub") {
		t.Error("a letter of no newsletter offers unsubscribing")
	}
	do(eve, "POST", "/mail/letters/"+news+"/unsubscribe", url.Values{}, 404)
	body, _ = do(anna, "POST", "/mail/letters/"+news+"/unsubscribe", url.Values{"block": {"1"}}, 200)
	res = nil
	must(json.Unmarshal([]byte(body), &res))
	if res["open"] != "https://shop.test/leave?u=1" || res["blocked"] != true {
		t.Errorf("unsubscribe = %s", body)
	}
	var at *time.Time
	must(pool.QueryRow(ctx, `SELECT f.role, x.unsubscribed_at FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id = $1`, news).Scan(&role, &at))
	if role != "junk" || at == nil {
		t.Errorf("the newsletter is in %q, unsubscribed %v", role, at)
	}
	if pane, _ := do(anna, "GET", "/mail/letters/"+news, nil, 200); strings.Contains(pane, "data-unsub") || !strings.Contains(pane, "munsub-done") {
		t.Error("unsubscribing is offered again")
	}

	// «Импорт писем»: the menu of a server's folder offers it, go24's own folder takes none, another's folder is not
	// found (the import itself — TestImport, against a mail server)
	page, _ = do(anna, "GET", "/mail?folder="+inbox, nil, 200)
	if !strings.Contains(page, `data-mail-import="`+inbox+`"`) || strings.Contains(page, `data-mail-import="`+junkF+`"`) {
		t.Error("«Импорт писем» is not offered in a server's folder only")
	}
	upload := func(who, folder string, want int) string {
		t.Helper()
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		fw, _ := w.CreateFormFile("file", "a.eml")
		_, _ = fw.Write([]byte("From: a@buyer.test\r\nSubject: Hi\r\n\r\nhi\r\n"))
		_ = w.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/mail/folders/"+folder+"/import", &b)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", w.FormDataContentType())
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("import into %s: %d, want %d: %.300s", folder, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	if body := upload(anna, junkF, 422); !strings.Contains(body, "нельзя импортировать") {
		t.Errorf("import into go24's folder = %s", body)
	}
	upload(eve, inbox, 404)
}
