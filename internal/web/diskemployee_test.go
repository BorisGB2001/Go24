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

// TestDiskEmployeeHTTP: «Диск сотрудника» — an administrator opens any employee's «Мой диск» (a dismissed one's too) from
// «Настройки Диска», the profile or the file's card, and manages it: moves files to the company's drive, deletes;
// nobody else reaches it, and it is not shared on its owner's behalf.
func TestDiskEmployeeHTTP(t *testing.T) {
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
	folder, err := srv.Drive.CreateFolder(ctx, eve, my, "", "Сметы")
	must(err)
	add := func(name, folder string) string {
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: name, ContentType: "text/plain",
			UploadedBy: eve, Drive: "user", DriveOwner: eve, FolderID: folder})
		must(err)
		return id
	}
	plan, smeta := add("план.txt", ""), add("смета.txt", folder)

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

	do(ivan, "GET", "/disk/u/"+eve, nil, 404)
	do(ivan, "GET", "/mail/drive?which=u:"+eve, nil, 404)
	page := do(boss, "GET", "/disk/u/"+eve, nil, 200)
	for _, want := range []string{"Диск сотрудника Ева", `data-disk="/disk/u/` + eve + `"`, `data-id="` + plan + `"`, "Сметы", "data-disk-employee"} {
		if !strings.Contains(page, want) {
			t.Errorf("the employee's drive has no %q", want)
		}
	}
	if strings.Contains(page, "сотрудник уволен") || strings.Contains(page, `action="/disk/u/`+eve+`/share"`) {
		t.Error("the employee's drive: «уволен» or «Доступ» shown")
	}
	if p := do(boss, "GET", "/disk/u/"+eve+"?folder="+folder, nil, 200); !strings.Contains(p, `data-id="`+smeta+`"`) {
		t.Error("a folder of the employee's drive")
	}
	if p := do(boss, "GET", "/disk/u/"+boss, nil, 200); strings.Contains(p, "data-disk-employee") || !strings.Contains(p, `data-disk="/disk/my"`) {
		t.Error("one's own /disk/u/ is «Мой диск»")
	}
	if p := do(boss, "GET", "/mail/drive?which=u:"+eve, nil, 200); !strings.Contains(p, "Диск сотрудника Ева") {
		t.Errorf("the picker has no employee's drive: %.300s", p)
	}
	if p := do(boss, "GET", "/settings/disk", nil, 200); !strings.Contains(p, `href="/disk/u/`+eve+`"`) {
		t.Error("«Настройки Диска» do not lead to the employee's drive")
	}
	if p := do(boss, "GET", "/files/"+smeta+"/about", nil, 200); !strings.Contains(p, `/disk/u/`+eve+`?folder=`+folder) {
		t.Error("the file's card does not lead to the employee's drive")
	}
	// not shared on the owner's behalf
	do(boss, "POST", "/disk/u/"+eve+"/share", url.Values{"folder_id": {folder}, "whom": {"user:" + ivan}, "access": {"read"}}, 403)
	// the administrator moves a file to the company's drive
	do(boss, "POST", "/disk/u/"+eve+"/move", url.Values{"file": {plan}, "to": {"company"}}, 200)
	var where string
	must(pool.QueryRow(ctx, `SELECT drive FROM files WHERE id::text = $1`, plan).Scan(&where))
	if where != "company" {
		t.Errorf("the moved file is on %q", where)
	}
	// a dismissed employee's drive is still reached
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id::text = $1`, eve)
	must(err)
	if p := do(boss, "GET", "/disk/u/"+eve, nil, 200); !strings.Contains(p, "сотрудник уволен") {
		t.Error("a dismissed employee's drive")
	}
	do(boss, "POST", "/disk/u/"+eve+"/delete", url.Values{"file": {smeta}, "folder": {folder}}, 200)
	must(pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE deleted_at IS NULL)::text FROM files WHERE id::text = $1`, smeta).Scan(&where))
	if where != "0" {
		t.Error("the administrator did not delete the employee's file")
	}
}
