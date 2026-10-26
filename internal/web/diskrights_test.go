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

// TestDiskRightsHTTP: «Права доступа» of a folder of the company's drive — the administrator opens it to a
// department, the others no longer see it on the page, by its address or by its file's link, and they have no
// «Права доступа» in the menu.
func TestDiskRightsHTTP(t *testing.T) {
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
	var boss, buh, eve, dept string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('buh', 'Бухгалтер', 'buh@go24.test') RETURNING id`).Scan(&buh))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Бухгалтерия') RETURNING id`).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, buh, dept)
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	co, _ := srv.Drive.Open(tasks.WithAdmin(ctx), boss, "company")
	money, err := srv.Drive.CreateFolder(ctx, boss, co, "", "Деньги")
	must(err)
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "зарплата.txt", ContentType: "text/plain",
		UploadedBy: boss, Drive: "company", FolderID: money})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin, AdminMode: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), buh: cookie(buh, false), eve: cookie(eve, false)}
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

	page := do(boss, "GET", "/disk/company", nil, 200)
	if !strings.Contains(page, `action="/disk/company/rights"`) || !strings.Contains(page, `data-toggle="rt-`+money) {
		t.Fatal("the administrator has no «Права доступа» of the folder")
	}
	if strings.Contains(do(eve, "GET", "/disk/company", nil, 200), `action="/disk/company/rights"`) {
		t.Fatal("an employee has «Права доступа» of the company's folder")
	}
	do(eve, "POST", "/disk/company/rights", url.Values{"folder_id": {money}, "whom": {"user:" + eve}, "access": {"full"}}, 403)
	do(boss, "POST", "/disk/company/rights", url.Values{"folder_id": {money}, "whom": {"department:" + dept}, "access": {"read"}}, 200)

	if strings.Contains(do(eve, "GET", "/disk/company", nil, 200), "Деньги") {
		t.Fatal("a closed folder is on the page of one it is not open to")
	}
	do(eve, "GET", "/disk/company?folder="+money, nil, 404)
	do(eve, "GET", "/files/"+file, nil, 403)
	if page := do(buh, "GET", "/disk/company", nil, 200); !strings.Contains(page, "Деньги") || !strings.Contains(page, "data-rights") {
		t.Fatal("the department does not see the folder it is open to, with its mark")
	}
	// read: the department sees the folder but does not upload into it
	if page := do(buh, "GET", "/disk/company?folder="+money, nil, 200); !strings.Contains(page, "зарплата.txt") || strings.Contains(page, `name="file" multiple`) {
		t.Fatal("read access: the folder's page")
	}
	do(buh, "POST", "/disk/company/folders", url.Values{"folder": {money}, "name": {"Новая"}}, 403)

	// «Как у папки выше»: everyone sees it again
	do(boss, "POST", "/disk/company/rights", url.Values{"folder_id": {money}, "clear": {"1"}}, 200)
	if !strings.Contains(do(eve, "GET", "/disk/company", nil, 200), "Деньги") {
		t.Fatal("a folder without rights of its own is not seen")
	}
}
