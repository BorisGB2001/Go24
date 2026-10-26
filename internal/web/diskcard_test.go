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

// TestDiskCardHTTP: «Подробнее» of a file of the company's drive — its card with the properties and «Обсуждение»;
// a comment is added and deleted by its author, the list shows how many there are, and the card of a file one may
// not read is not shown.
func TestDiskCardHTTP(t *testing.T) {
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
	co, _ := srv.Drive.Open(tasks.WithAdmin(ctx), boss, "company")
	money, err := srv.Drive.CreateFolder(ctx, boss, co, "", "Отчёты")
	must(err)
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "итоги.txt", ContentType: "text/plain",
		UploadedBy: boss, Drive: "company", FolderID: money})
	must(err)

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

	card := do(eve, "GET", "/files/"+file+"/about", nil, 200)
	for _, want := range []string{"итоги.txt", "Отчёты", "Босс", `action="/files/` + file + `/comments"`, `href="/disk/company?folder=` + money + `"`} {
		if !strings.Contains(card, want) {
			t.Fatalf("the card has no %q", want)
		}
	}
	do(eve, "POST", "/files/"+file+"/comments", url.Values{"body": {"Цифры <b>сходятся</b>"}}, 200)
	card = do(boss, "GET", "/files/"+file+"/about", nil, 200)
	if !strings.Contains(card, "Цифры &lt;b&gt;сходятся&lt;/b&gt;") || !strings.Contains(card, "Ева") {
		t.Fatal("the comment is not on the card, escaped")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_comment'`, boss).Scan(&n))
	if n != 1 {
		t.Fatalf("the uploader's notifications: %d", n)
	}
	if page := do(ivan, "GET", "/disk/company?folder="+money, nil, 200); !strings.Contains(page, `/files/`+file+`/about#comments`) {
		t.Fatal("the list does not show the comments of the file")
	}
	var c string
	must(pool.QueryRow(ctx, `SELECT id FROM drive_file_comments WHERE file_id = $1`, file).Scan(&c))
	do(ivan, "POST", "/files/"+file+"/comments", url.Values{"delete": {c}}, 404)
	do(eve, "POST", "/files/"+file+"/comments", url.Values{"delete": {c}}, 200)
	if strings.Contains(do(eve, "GET", "/files/"+file+"/about", nil, 200), "сходятся") {
		t.Fatal("a deleted comment is on the card")
	}

	// a folder closed to Ivan: no card, no comments
	do(boss, "POST", "/disk/company/rights", url.Values{"folder_id": {money}, "whom": {"user:" + eve}, "access": {"read"}}, 200)
	do(ivan, "GET", "/files/"+file+"/about", nil, 403)
	do(ivan, "POST", "/files/"+file+"/comments", url.Values{"body": {"?"}}, 403)
	// a file not on a drive has no card
	other, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "x.txt", ContentType: "text/plain", UploadedBy: eve})
	must(err)
	do(eve, "GET", "/files/"+other+"/about", nil, 404)
}
