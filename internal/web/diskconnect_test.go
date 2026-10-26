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

// TestDiskConnectHTTP: «Подключить к Диску» of a folder shared with the user and of a project's drive — in «Общие со
// мной» and on the drive's page; the connected are listed at the top of «Мой диск» and disconnected from there; the
// menus have «Копировать внутреннюю ссылку».
func TestDiskConnectHTTP(t *testing.T) {
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
	_, err = ts.CreateProject(ctx, eve, "CN", "Стройка", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, eve, "CN")
	must(ts.SetMember(ctx, p, ivan, "viewer"))
	my, _ := srv.Drive.Open(ctx, eve, "my")
	folder, err := srv.Drive.CreateFolder(ctx, eve, my, "", "Сметы")
	must(err)
	must(srv.Drive.SetShare(ctx, eve, my, folder, "", "user", ivan, "read"))
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "план.txt", ContentType: "text/plain",
		UploadedBy: eve, Drive: "user", DriveOwner: eve})
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

	shared := do(ivan, "GET", "/disk/shared", nil, 200)
	if !strings.Contains(shared, `name="which" value="sh:`+folder+`"`) {
		t.Fatal("no «Подключить к Диску» in «Общие со мной»")
	}
	do(ivan, "POST", "/disk/connect", url.Values{"which": {"sh:" + folder}, "back": {"/disk/shared"}}, 200)
	if !strings.Contains(do(ivan, "GET", "/disk/shared", nil, 200), `name="off" value="1"`) {
		t.Error("the connected folder is not shown so in «Общие со мной»")
	}
	if !strings.Contains(do(ivan, "GET", "/disk/p/CN", nil, 200), `name="which" value="CN"`) {
		t.Error("no «Подключить к Диску» on the project's drive")
	}
	do(ivan, "POST", "/disk/connect", url.Values{"which": {"CN"}, "back": {"//evil"}}, 200)
	page := do(ivan, "GET", "/disk/my", nil, 200)
	if strings.Count(page, "data-connected") != 2 {
		t.Errorf("«Мой диск» lists %d connected", strings.Count(page, "data-connected"))
	}
	for _, want := range []string{`href="/disk/sh/` + folder + `"`, "Сметы", "Ева", "Стройка"} {
		if !strings.Contains(page, want) {
			t.Fatalf("«Мой диск» has no %q", want)
		}
	}
	if !strings.Contains(do(ivan, "GET", "/disk/sh/"+folder, nil, 200), `name="off" value="1"`) {
		t.Error("the shared folder's page does not show it connected")
	}
	do(eve, "POST", "/disk/connect", url.Values{"which": {"my"}}, 404)
	do(ivan, "POST", "/disk/connect", url.Values{"which": {"CN"}, "off": {"1"}}, 200)
	if strings.Count(do(ivan, "GET", "/disk/my", nil, 200), "data-connected") != 1 {
		t.Error("the disconnected project's drive is still in «Мой диск»")
	}
	// «Копировать внутреннюю ссылку»
	if !strings.Contains(do(eve, "GET", "/disk/my", nil, 200), `data-copy="/files/`+file+`/about"`) {
		t.Error("no internal link of the file")
	}
	if !strings.Contains(do(eve, "GET", "/disk/my", nil, 200), `data-copy="/disk/my?folder=`+folder+`"`) {
		t.Error("no internal link of the folder")
	}
}
