package web

import (
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
	"path"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskFolderUploadHTTP: an uploaded folder — each file comes with its path, the folders are made on
// the way (a folder of the same name takes the files in) and a file of the same name becomes its new version. Before
// an upload the page asks which files the folder already has (a folder of the path not there yet holds none) and
// whether each may be uploaded again; «Сохранить под другим именем» (conflict=rename) puts «name (2)» beside it.
func TestDiskFolderUploadHTTP(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
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
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	my, _ := srv.Drive.Open(ctx, anna, "my")
	photos, err := srv.Drive.CreateFolder(ctx, anna, my, "", "Фото")
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	login := func(uid string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: uid}))
		return rec.Result().Cookies()[0]
	}
	cookie := login(anna)
	put := func(cookie *http.Cookie, base, conflict, dir, name, text string, want int) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("folder", "")
		mw.WriteField("path", dir)
		mw.WriteField("conflict", conflict)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(text))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+base+"/upload", &buf)
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Go24-Form", "1")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("upload %s/%s: %d, want %d: %.300s", dir, name, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	upload := func(dir, name, text string, want int) string {
		return put(cookie, "/disk/my", "", dir, name, text, want)
	}
	upload("Фото/Лето", "море.txt", "1", 200)
	upload("фото/Лето", "море.txt", "22", 200) // the same file again: a new version
	upload("Фото", "лес.txt", "3", 200)
	upload("", "корень.txt", "4", 200)
	upload("Фото/../..", "x.txt", "5", 422)

	folders, files, _, err := srv.Drive.List(ctx, my, photos)
	must(err)
	if len(folders) != 1 || folders[0].Name != "Лето" || len(files) != 1 || files[0].Name != "лес.txt" {
		t.Fatalf("«Фото» holds %+v %+v", folders, files)
	}
	_, files, _, err = srv.Drive.List(ctx, my, folders[0].ID)
	must(err)
	if len(files) != 1 || files[0].Name != "море.txt" || files[0].Size != 2 {
		t.Fatalf("«Лето» holds %+v", files)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_folders WHERE deleted_at IS NULL`).Scan(&n))
	if n != 2 {
		t.Errorf("%d folders, want 2", n)
	}

	// what the folder already has, asked before an upload
	clash := func(cookie *http.Cookie, base string, names ...string) string {
		t.Helper()
		form := url.Values{"folder": {""}}
		for _, n := range names {
			dir, name := path.Split(n)
			form.Add("name", name)
			form.Add("path", strings.TrimSuffix(dir, "/"))
		}
		req, _ := http.NewRequest("POST", hs.URL+base+"/clash", strings.NewReader(form.Encode()))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Go24-Form", "1")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("clash: %d %s", res.StatusCode, raw)
		}
		var out bytes.Buffer
		must(json.Compact(&out, raw))
		return out.String()
	}
	if got := clash(cookie, "/disk/my", "корень.txt", "новый.txt", "Фото/Лето/море.txt", "Фото/Зима/море.txt", "Фото/лес.txt"); got != `{"clashes":[{"i":0,"version":true},{"i":2,"version":true},{"i":4,"version":true}]}` {
		t.Errorf("clashes: %s", got)
	}
	// «Сохранить под другим именем»: «корень (2).txt», then «корень (3).txt»; the first one keeps its bytes
	put(cookie, "/disk/my", "rename", "", "корень.txt", "55", 200)
	put(cookie, "/disk/my", "rename", "", "корень.txt", "666", 200)
	put(cookie, "/disk/my", "rename", "", "другой.txt", "7", 200) // no clash — its own name
	_, files, _, err = srv.Drive.List(ctx, my, "")
	must(err)
	got := map[string]int64{}
	for _, f := range files {
		got[f.Name] = f.Size
	}
	if len(got) != 4 || got["корень.txt"] != 1 || got["корень (2).txt"] != 2 || got["корень (3).txt"] != 3 || got["другой.txt"] != 1 {
		t.Errorf("the top holds %v", got)
	}
	// on the Common drive Иван may not upload Анна's file again: only another name or skip
	put(cookie, "/disk/company", "", "", "устав.pdf", "1", 200)
	if got := clash(login(ivan), "/disk/company", "устав.pdf"); got != `{"clashes":[{"i":0,"version":false}]}` {
		t.Errorf("Иван's clashes: %s", got)
	}
	if got := clash(cookie, "/disk/company", "устав.pdf"); got != `{"clashes":[{"i":0,"version":true}]}` {
		t.Errorf("Анна's clashes: %s", got)
	}
}
