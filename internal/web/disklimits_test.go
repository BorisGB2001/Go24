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
	"go24/internal/storages"
	"go24/internal/tasks"
)

func TestParseGB(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "0": 0, "5": 5 << 30, "0,5": 1 << 29, " 1.25 ": 5 << 28} {
		if got, err := parseGB(in); err != nil || got != want {
			t.Errorf("parseGB(%q) = %d %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-1", "x", "NaN", "1e9"} {
		if _, err := parseGB(in); err == nil {
			t.Errorf("parseGB(%q) is taken", in)
		}
	}
	if gbText(0) != "" || gbText(5<<30) != "5" || gbText(1<<29) != "0.5" {
		t.Error("gbText")
	}
}

// TestDiskLimitsHTTP: «Место на Диске» — administrators only set the limits and see every drive; the drive page
// shows how much is used of what; an upload over the limit is refused with a clear error.
func TestDiskLimitsHTTP(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Борис', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/1", Name: "big.bin", Size: 900 << 20, UploadedBy: anna, Drive: "user", DriveOwner: anna})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, ct string, body io.Reader, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		if ct != "" {
			req.Header.Set("Content-Type", ct)
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
	form := func(v url.Values) io.Reader { return strings.NewReader(v.Encode()) }
	const formCT = "application/x-www-form-urlencoded"

	send(anna, "GET", "/settings/disk", "", nil, 403)
	send(anna, "POST", "/settings/disk", formCT, form(url.Values{"user": {"1"}}), 403)
	if page := send(anna, "GET", "/disk/my", "", nil, 200); !strings.Contains(page, "data-disk-usage") || !strings.Contains(page, "Занято 900") {
		t.Error("the drive page does not show what is used")
	}
	send(boss, "POST", "/settings/disk", formCT, form(url.Values{"user": {"x"}}), 422)
	send(boss, "POST", "/settings/disk", formCT, form(url.Values{"user": {"1"}, "company": {"0,5"}, "project": {""}}), 200)
	if l, _ := srv.Drive.Limits(ctx); l != (drive.Limits{User: 1 << 30, Company: 1 << 29}) {
		t.Errorf("limits = %+v", l)
	}
	page := send(boss, "GET", "/settings/disk", "", nil, 200)
	for _, want := range []string{`name="user" value="1"`, `name="company" value="0.5"`, "Анна", `width:87%`} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings page has no %s", want)
		}
	}
	if page := send(anna, "GET", "/disk/my", "", nil, 200); !strings.Contains(page, "из 1") || !strings.Contains(page, `width:87%`) {
		t.Error("the drive page does not show the limit")
	}

	// «Возможности Диска»: public links and locking off — not in the file's menu, the link made does not open
	token, err := srv.Drive.SetLink(ctx, anna, drive.Drive{Kind: "user", OwnerID: anna, CanWrite: true}, "file", file, "", false, nil)
	must(err)
	send(anna, "GET", "/s/"+token, "", nil, 200)
	page = send(anna, "GET", "/disk/my", "", nil, 200)
	for _, want := range []string{`action="/disk/my/link"`, `action="/disk/my/lock"`, "/s/" + token} {
		if !strings.Contains(page, want) {
			t.Errorf("the drive page has no %s", want)
		}
	}
	send(boss, "POST", "/settings/disk", formCT, form(url.Values{"user": {"1"}, "features": {"1"}, "content": {"1"}}), 200)
	if l, _ := srv.Drive.Limits(ctx); !l.NoLinks || !l.NoLocks || l.NoContent || l.User != 1<<30 {
		t.Errorf("switches = %+v", l)
	}
	if page := send(boss, "GET", "/settings/disk", "", nil, 200); !strings.Contains(page, `name="content" value="1" checked`) || strings.Contains(page, `name="links" value="1" checked`) {
		t.Error("the settings page does not show the switches")
	}
	page = send(anna, "GET", "/disk/my", "", nil, 200)
	for _, nope := range []string{`action="/disk/my/link"`, `action="/disk/my/lock"`, "/s/" + token} {
		if strings.Contains(page, nope) {
			t.Errorf("the drive page has %s with it off", nope)
		}
	}
	send(anna, "GET", "/s/"+token, "", nil, 404)
	send(boss, "POST", "/settings/disk", formCT, form(url.Values{"user": {"1"}, "company": {"0,5"}}), 200) // no switches in the form: kept
	if l, _ := srv.Drive.Limits(ctx); !l.NoLinks || !l.NoLocks || l.Company != 1<<29 {
		t.Errorf("switches after a form without them = %+v", l)
	}

	// an upload over the limit: refused before anything is stored
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "more.bin")
	fw.Write(bytes.Repeat([]byte("x"), 1024))
	mw.Close()
	must(srv.Drive.SaveLimits(ctx, drive.Limits{User: 900<<20 + 100}))
	if body := send(anna, "POST", "/disk/my/upload", mw.FormDataContentType(), &buf, 422); !strings.Contains(body, "err.disk_full") {
		t.Errorf("upload = %s", body)
	}
}
