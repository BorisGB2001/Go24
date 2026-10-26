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
	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskSpaceHTTP: «Очистка» of one's «Мой диск» — what the place is made of and the biggest files; the former
// versions and the trash are cleaned from it; the company's drive is cleaned by an administrator only; the
// administrator sets how many former versions are kept.
func TestDiskSpaceHTTP(t *testing.T) {
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
	var boss, eve, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	my, _ := srv.Drive.Open(ctx, eve, "my")
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "смета.xlsx", ContentType: "x", Size: 300,
		UploadedBy: eve, Drive: "user", DriveOwner: eve})
	must(err)
	must(srv.Drive.AddVersion(ctx, eve, my, file, drive.NewVersion{Key: "k/" + randomHex(8), Size: 200, ContentType: "x"}))
	gone, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "мусор.txt", ContentType: "x", Size: 40,
		UploadedBy: eve, Drive: "user", DriveOwner: eve})
	must(err)
	must(srv.Drive.DeleteFile(ctx, eve, my, gone))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin, AdminMode: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), eve: cookie(eve, false), ivan: cookie(ivan, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
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

	page := do(eve, "GET", "/disk/my", nil, 200)
	if !strings.Contains(page, `href="/disk/my?space"`) {
		t.Fatal("no «Очистка» on the drive")
	}
	space := do(eve, "GET", "/disk/my?space", nil, 200)
	for _, want := range []string{"смета.xlsx", `action="/disk/my/clean"`, `value="versions"`, `value="trash"`, `name="file" value="` + file + `"`} {
		if !strings.Contains(space, want) {
			t.Fatalf("«Очистка» has no %q", want)
		}
	}
	if strings.Contains(do(ivan, "GET", "/disk/company", nil, 200), `href="/disk/company?space"`) {
		t.Error("«Очистка» of the company's drive for one who is not an administrator")
	}
	do(ivan, "GET", "/disk/company?space", nil, 403)
	do(ivan, "POST", "/disk/company/clean", url.Values{"what": {"trash"}}, 403)
	do(boss, "GET", "/disk/company?space", nil, 200)

	do(eve, "POST", "/disk/my/clean", url.Values{"what": {"versions"}, "file": {file}}, 200)
	if vs, _ := srv.Drive.Versions(ctx, file); len(vs) != 1 {
		t.Errorf("versions after the cleanup = %+v", vs)
	}
	do(eve, "POST", "/disk/my/clean", url.Values{"what": {"trash"}}, 200)
	if sp, _ := srv.Drive.Space(ctx, my); sp.Used() != 200 || sp.TrashCount != 0 {
		t.Errorf("space after the cleanup = %+v", sp)
	}

	// the versions kept: an administrator's setting
	do(eve, "POST", "/settings/disk", url.Values{"versions": {"3"}}, 403)
	do(boss, "POST", "/settings/disk", url.Values{"versions": {"abc"}}, 422)
	do(boss, "POST", "/settings/disk", url.Values{"versions": {"3"}}, 200)
	if l, _ := srv.Drive.Limits(ctx); l.Versions != 3 {
		t.Errorf("limits = %+v", l)
	}
	if !strings.Contains(do(boss, "GET", "/settings/disk", nil, 200), `name="versions" value="3"`) {
		t.Error("the versions kept are not shown")
	}
}
