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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskVersionUpload: «Загрузить новую версию» — a file picked on the computer, whatever its name,
// becomes the file's new version (the name stays, the former bytes are a version); one who may not change the file
// gets nothing stored; the menu, «История версий» and «Подробнее» offer it to those who may; REST
// POST /api/v1/disk/drives/{drive}/files/{id}/versions does the same (disk.file.uploadversion).
func TestDiskVersionUpload(t *testing.T) {
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
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	login := func(uid string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: uid}))
		return rec.Result().Cookies()[0]
	}
	annaC, ivanC := login(anna), login(ivan)
	send := func(req *http.Request, want int) string {
		t.Helper()
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	multi := func(url, field, name, text string, fields ...string) *http.Request {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for i := 0; i+1 < len(fields); i += 2 {
			mw.WriteField(fields[i], fields[i+1])
		}
		fw, _ := mw.CreateFormFile(field, name)
		fw.Write([]byte(text))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+url, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		return req
	}
	page := func(c *http.Cookie, url string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+url, nil)
		req.AddCookie(c)
		return send(req, 200)
	}
	company, err := srv.Drive.Open(ctx, anna, "company")
	must(err)
	up := multi("/disk/company/upload", "file", "план.txt", "1", "folder", "")
	up.AddCookie(annaC)
	var got struct{ IDs []string }
	must(json.Unmarshal([]byte(send(up, 200)), &got))
	id := got.IDs[0]

	// the menu «…» and «История версий» of the list, «Подробнее»: for the uploader, not for another
	list := page(annaC, "/disk/company")
	for _, want := range []string{`data-disk-newver="` + id + `" data-action="/disk/company/version-upload"`, "js/diskversion.js", "data-disk-ver-up"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
	if strings.Contains(page(ivanC, "/disk/company"), `data-disk-newver="`+id) {
		t.Error("Иван is offered a new version of Анна's file")
	}
	if card := page(annaC, "/files/"+id+"/about"); !strings.Contains(card, `data-disk-newver="`+id+`"`) || !strings.Contains(card, "js/diskversion.js") {
		t.Error("«Подробнее» lacks «Загрузить новую версию»")
	}
	if strings.Contains(page(ivanC, "/files/"+id+"/about"), "data-disk-newver") {
		t.Error("Иван's «Подробнее» offers a new version")
	}

	// another name on the computer: the file keeps its own, the former bytes become a version
	nv := multi("/disk/company/version-upload", "data", "новое имя.docx", "22", "file", id, "folder", "")
	nv.AddCookie(annaC)
	send(nv, 200)
	on, err := srv.Drive.FileOn(ctx, company, id)
	must(err)
	if on.Name != "план.txt" || on.Size != 2 {
		t.Fatalf("after a new version the file is %+v", on)
	}
	vs, err := srv.Drive.Versions(ctx, id)
	must(err)
	if len(vs) != 2 || vs[1].Size != 1 {
		t.Fatalf("versions %+v", vs)
	}
	var objects int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_file_versions`).Scan(&objects))
	nv = multi("/disk/company/version-upload", "data", "x.txt", "333", "file", id, "folder", "")
	nv.AddCookie(ivanC)
	send(nv, http.StatusForbidden)
	nv = multi("/disk/company/version-upload", "data", "x.txt", "333", "file", "00000000-0000-0000-0000-000000000000", "folder", "")
	nv.AddCookie(annaC)
	send(nv, http.StatusNotFound)
	if vs, _ := srv.Drive.Versions(ctx, id); len(vs) != 2 {
		t.Errorf("a refused version is kept: %+v", vs)
	}

	// REST, as disk.file.uploadversion
	k, err := keys.Create(ctx, anna, "k", []string{"disk:read", "disk:write"}, time.Hour)
	must(err)
	ro, err := keys.Create(ctx, anna, "r", []string{"disk:read"}, time.Hour)
	must(err)
	rest := func(token, text string, want int) string {
		req := multi("/api/v1/disk/drives/company/files/"+id+"/versions", "file", "любое.bin", text)
		req.Header.Set("Authorization", "Bearer "+token)
		return send(req, want)
	}
	rest(ro, "4444", http.StatusForbidden)
	var f apiDiskFile
	must(json.Unmarshal([]byte(rest(k, "4444", 200)), &f))
	if f.ID != id || f.Name != "план.txt" || f.Size != 4 {
		t.Errorf("REST answered %+v", f)
	}
	if vs, _ := srv.Drive.Versions(ctx, id); len(vs) != 3 {
		t.Errorf("REST: %d versions, want 3", len(vs))
	}
}
