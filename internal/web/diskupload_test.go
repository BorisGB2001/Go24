package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
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
// the way (a folder of the same name takes the files in) and a file of the same name becomes its new version.
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
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
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	upload := func(dir, name, text string, want int) string {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("folder", "")
		mw.WriteField("path", dir)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(text))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/disk/my/upload", &buf)
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
}
