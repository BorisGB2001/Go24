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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(w, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestStaticAppHTTP: a static application — the administrator uploads a ZIP (index.html in its one folder) with the
// form; a bad archive is refused; installed, its files are given in a sandbox (CSP sandbox, no cookies needed), its page
// opens in the frame posting to its own address in go24, /apps/frame lets the sandbox («null») talk; not installed or
// another application's path — 404.
func TestStaticAppHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, req *http.Request, want int) *http.Response {
		t.Helper()
		if c := as[who]; c != nil {
			req.AddCookie(c)
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := client.Do(req)
		must(err)
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return res
	}
	body := func(res *http.Response) string {
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return string(raw)
	}
	// upload: the form with the archive, as the settings page sends it; the message of the answer
	upload := func(path string, fields url.Values, archive []byte) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, vs := range fields {
			for _, v := range vs {
				_ = mw.WriteField(k, v)
			}
		}
		if archive != nil {
			fw, _ := mw.CreateFormFile("archive", "app.zip")
			_, _ = fw.Write(archive)
		}
		must(mw.Close())
		req, _ := http.NewRequest("POST", hs.URL+path, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res := do(boss, req, 303)
		res.Body.Close()
		u, err := url.Parse(res.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg")
	}
	get := func(who, path string, want int) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		return do(who, req, want)
	}

	good := zipOf(t, map[string]string{"calc/index.html": `<script src="/go24.js"></script><p>Калькулятор</p>`,
		"calc/js/app.js": "go24.init()", "calc/css/app.css": "p{}"})
	if m := upload("/settings/apps/new", url.Values{"op": {"save"}, "name": {"Калькулятор"}, "static": {"on"}, "scopes": {"tasks:read"}},
		zipOf(t, map[string]string{"../evil.html": "x", "index.html": "x"})); !strings.Contains(m, "ZIP") {
		t.Fatalf("bad archive: %q", m)
	}
	list, err := srv.Apps.List(ctx)
	must(err)
	if len(list) != 1 || !list[0].Static || list[0].HandlerURL != "" {
		t.Fatalf("saved: %+v", list)
	}
	a := list[0]
	if m := upload("/settings/apps/"+a.ID, url.Values{"op": {"save"}, "name": {"Калькулятор"}, "static": {"on"}, "scopes": {"tasks:read"}}, good); m != "" {
		t.Fatalf("upload: %q", m)
	}
	if m := upload("/settings/apps/"+a.ID, url.Values{"op": {"save"}, "name": {"Калькулятор"}, "static": {"on"}, "scopes": {"tasks:read"}},
		zipOf(t, map[string]string{"a/index.html": "x", "b/x.js": "x"})); !strings.Contains(m, "ZIP") {
		t.Fatalf("two folders: %q", m)
	}
	page := body(get(boss, "/settings/apps", 200))
	if !strings.Contains(page, "Файлов: 3") || !strings.Contains(page, "/apps/static/"+a.ID+"/") {
		t.Fatalf("settings page lacks the files")
	}
	get("", "/apps/static/"+a.ID+"/index.html", 404) // not installed yet
	upload("/settings/apps/"+a.ID, url.Values{"op": {"install"}}, nil)
	if a, err = srv.Apps.Get(ctx, a.ID); err != nil || !a.Installed() {
		t.Fatalf("install: %v %+v", err, a)
	}

	// the files: without a session, in a sandbox, of their own types
	res := get("", "/apps/static/"+a.ID+"/", 200)
	if csp := res.Header.Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox allow-scripts") || strings.Contains(csp, "allow-same-origin") ||
		!strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(body(res), "Калькулятор") {
		t.Fatalf("index: %v", res.Header)
	}
	if res := get("", "/apps/static/"+a.ID+"/js/app.js", 200); !strings.Contains(res.Header.Get("Content-Type"), "javascript") {
		t.Fatalf("js: %v", res.Header)
	}
	req, _ := http.NewRequest("POST", hs.URL+"/apps/static/"+a.ID+"/index.html", strings.NewReader("AUTH_ID=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body(do("", req, 200)) // the frame's form is posted to the page
	get("", "/apps/static/"+a.ID+"/nope.js", 404)
	get("", "/apps/static/"+a.ID+"/..%2Findex.html", 404)

	// the page: the frame posts to the application's address in go24; the frame's sandbox may talk to the page around
	if p := body(get(anna, "/apps/"+a.ID, 200)); !strings.Contains(p, "https://go24.example/apps/static/"+a.ID+"/index.html?") {
		t.Fatalf("frame action: %.2000s", p)
	}
	req, _ = http.NewRequest("POST", hs.URL+"/apps/frame", strings.NewReader("app="+a.ID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var fr struct {
		Origins []string `json:"origins"`
		Auth    struct {
			Token string `json:"access_token"`
		} `json:"auth"`
	}
	must(json.NewDecoder(do(anna, req, 200).Body).Decode(&fr))
	if len(fr.Origins) != 1 || fr.Origins[0] != "null" || fr.Auth.Token == "" {
		t.Fatalf("frame: %+v", fr)
	}

	// uninstalled — the files are no more given
	upload("/settings/apps/"+a.ID, url.Values{"op": {"uninstall"}}, nil)
	get("", "/apps/static/"+a.ID+"/", 404)
}
