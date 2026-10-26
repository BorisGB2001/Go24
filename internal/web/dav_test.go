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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDavHTTP: the drives over WebDAV — only with an API key that may «disk:read»; the top lists the user's drives and
// «Общие со мной»; folders are made, renamed, moved and deleted (to the trash) with «disk:write» only; a file put
// is read back, put again it becomes a new version.
func TestDavHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	_, err = ts.CreateProject(ctx, anna, "DV", "Отдел/продаж", "")
	must(err)
	my, _ := srv.Drive.Open(ctx, anna, "my")
	shared, err := srv.Drive.CreateFolder(ctx, anna, my, "", "Для Ивана")
	must(err)
	must(srv.Drive.SetShare(ctx, anna, my, shared, "", "user", ivan, "read"))
	old, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/old", Name: "старый.txt", UploadedBy: anna, Drive: "user", DriveOwner: anna})
	must(err)
	key := func(who string, scopes ...string) string {
		k, err := srv.Keys.Create(ctx, who, "dav", scopes, 0)
		must(err)
		return k
	}
	readKey, writeKey, mailKey := key(anna, "disk:read"), key(anna, "disk:read", "disk:write"), key(anna, "mail:read")
	ivanKey := key(ivan, "disk:read", "disk:write")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	do := func(token, method, p string, hdr map[string]string, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+p, strings.NewReader(body))
		if token != "" {
			req.SetBasicAuth("anyone", token)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, p, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	depth1 := map[string]string{"Depth": "1"}
	myDav := "/dav/" + url.PathEscape("Мой диск")

	do("", "PROPFIND", "/dav/", depth1, "", 401)
	do(mailKey, "PROPFIND", "/dav/", depth1, "", 401)
	top := do(readKey, "PROPFIND", "/dav/", depth1, "", 207)
	for _, want := range []string{"Мой диск", "Общий диск", "Отдел-продаж (DV)", "Общие со мной"} {
		if !strings.Contains(top, url.PathEscape(want)) && !strings.Contains(top, want) {
			t.Errorf("the top has no %s: %.1500s", want, top)
		}
	}
	if list := do(readKey, "PROPFIND", myDav+"/", depth1, "", 207); !strings.Contains(list, "старый.txt") && !strings.Contains(list, url.PathEscape("старый.txt")) {
		t.Errorf("my drive: %.1500s", list)
	}
	do(readKey, "MKCOL", myDav+"/"+url.PathEscape("Отчёты"), nil, "", 403)
	do(writeKey, "MKCOL", myDav+"/"+url.PathEscape("Отчёты"), nil, "", 201)
	do(writeKey, "MKCOL", "/dav/"+url.PathEscape("Новый диск"), nil, "", 405) // the top is go24's
	do(writeKey, "MOVE", myDav+"/"+url.PathEscape("Отчёты"), map[string]string{"Destination": hs.URL + myDav + "/" + url.PathEscape("Для Ивана") + "/" + url.PathEscape("Итоги")}, "", 201)
	var parent, name string
	must(pool.QueryRow(ctx, `SELECT coalesce(parent_id::text, ''), name FROM drive_folders WHERE name IN ('Отчёты', 'Итоги')`).Scan(&parent, &name))
	if parent != shared || name != "Итоги" {
		t.Errorf("moved folder = %s %s", parent, name)
	}
	do(writeKey, "DELETE", myDav+"/"+url.PathEscape("старый.txt"), nil, "", 204)
	var deleted bool
	must(pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM files WHERE id = $1`, old).Scan(&deleted))
	if !deleted {
		t.Error("the deleted file is not in the trash")
	}
	do(writeKey, "DELETE", myDav, nil, "", 405) // a drive itself

	// Ivan: the folder shared with him, read only
	sh := "/dav/" + url.PathEscape("Общие со мной")
	if list := do(ivanKey, "PROPFIND", sh+"/", depth1, "", 207); !strings.Contains(list, url.PathEscape("Для Ивана (Анна)")) && !strings.Contains(list, "Для Ивана (Анна)") {
		t.Errorf("ivan's shared: %.1500s", list)
	}
	if list := do(ivanKey, "PROPFIND", sh+"/"+url.PathEscape("Для Ивана (Анна)")+"/", depth1, "", 207); !strings.Contains(list, "Итоги") && !strings.Contains(list, url.PathEscape("Итоги")) {
		t.Errorf("inside the shared folder: %.1500s", list)
	}
	do(ivanKey, "MKCOL", sh+"/"+url.PathEscape("Для Ивана (Анна)")+"/x", nil, "", 405) // reading only

	if s3ep == "" {
		t.Log("no GO24_TEST_S3: files put over WebDAV are not checked")
		return
	}
	f := myDav + "/" + url.PathEscape("план.txt")
	do(writeKey, "PUT", f, nil, "первый", 201)
	if got := do(writeKey, "GET", f, nil, "", 200); got != "первый" {
		t.Errorf("GET = %q", got)
	}
	do(writeKey, "PUT", f, nil, "второй вариант", 201)
	if got := do(readKey, "GET", f, nil, "", 200); got != "второй вариант" {
		t.Errorf("GET = %q", got)
	}
	var id string
	must(pool.QueryRow(ctx, `SELECT id::text FROM files WHERE name = 'план.txt'`).Scan(&id))
	if vs, _ := srv.Drive.Versions(ctx, id); len(vs) != 2 || vs[0].Size != int64(len("второй вариант")) {
		t.Errorf("versions = %+v", vs)
	}
	do(readKey, "PUT", f, nil, "x", 403)
}
