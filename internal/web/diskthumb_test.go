package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

// TestDiskThumb: the thumbnails of the tiles, as the previews of the Disk — a picture made smaller on the server,
// kept for the file's state (asked again — from the kept one, 304 by its ETag; a new version — a new thumbnail); a
// file of which none can be made — 404, remembered; another's private file — 404; REST gives the same as PREVIEW_URL.
func TestDiskThumb(t *testing.T) {
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
	pic := func(w, h int, c color.Color) string {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		must(png.Encode(&buf, img))
		return buf.String()
	}
	upload := func(name, data string) string {
		up := multi("/disk/my/upload", "file", name, data, "folder", "")
		up.AddCookie(annaC)
		var got struct{ IDs []string }
		must(json.Unmarshal([]byte(send(up, 200)), &got))
		return got.IDs[0]
	}
	photo := upload("фото.png", pic(1600, 800, color.RGBA{R: 255, A: 255}))
	note := upload("заметка.zip", "PK")
	text := upload("план.txt", "текст") // a document: without the document server — no thumbnail

	thumbReq := func(c *http.Cookie, id, etag string) *http.Request {
		req, _ := http.NewRequest("GET", hs.URL+"/files/"+id+"/thumb", nil)
		req.AddCookie(c)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		return req
	}
	get := func(id string) (image.Image, string) {
		t.Helper()
		res, err := http.DefaultClient.Do(thumbReq(annaC, id, ""))
		must(err)
		defer res.Body.Close()
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("thumb %s: %d %s", id, res.StatusCode, res.Header.Get("Content-Type"))
		}
		img, err := jpeg.Decode(res.Body)
		must(err)
		return img, res.Header.Get("ETag")
	}
	img, etag := get(photo)
	if b := img.Bounds(); b.Dx() != 400 || b.Dy() != 200 {
		t.Errorf("the thumbnail is %v", b)
	}
	if etag == "" {
		t.Fatal("no ETag")
	}
	send(thumbReq(annaC, photo, etag), http.StatusNotModified)
	var kept int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM file_thumbs WHERE file_id = $1 AND data IS NOT NULL`, photo).Scan(&kept))
	if kept != 1 {
		t.Errorf("%d thumbnails kept", kept)
	}
	send(thumbReq(ivanC, photo, ""), http.StatusNotFound) // Анна's «Мой диск»
	send(thumbReq(annaC, note, ""), http.StatusNotFound)
	send(thumbReq(annaC, text, ""), http.StatusNotFound)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM file_thumbs WHERE file_id = $1 AND data IS NULL`, text).Scan(&kept))
	if kept != 1 {
		t.Error("«no thumbnail» of a document is not remembered")
	}

	// a new version: a new thumbnail, a new ETag
	nv := multi("/disk/my/version-upload", "data", "x.png", pic(300, 600, color.RGBA{B: 255, A: 255}), "file", photo, "folder", "")
	nv.AddCookie(annaC)
	send(nv, 200)
	send(thumbReq(annaC, photo, etag), 200)
	img, etag2 := get(photo)
	if b := img.Bounds(); b.Dx() != 200 || b.Dy() != 400 || etag2 == etag {
		t.Errorf("after a new version: %v %s", b, etag2)
	}

	// the tiles ask for the thumbnails of pictures and documents, not of other files
	list := page(annaC, "/disk/my")
	for _, want := range []string{`src="/files/` + photo + `/thumb"`, `src="/files/` + text + `/thumb"`, "data-thumb"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %s", want)
		}
	}
	if strings.Contains(list, note+"/thumb") {
		t.Error("a thumbnail of a ZIP is asked for")
	}

	// REST: thumb of the files, as PREVIEW_URL
	k, err := keys.Create(ctx, anna, "k", []string{"disk:read"}, time.Hour)
	must(err)
	rest := func(url string, want int) string {
		req, _ := http.NewRequest("GET", hs.URL+url, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		return send(req, want)
	}
	var folder struct{ Files []apiDiskFile }
	must(json.Unmarshal([]byte(rest("/api/v1/disk/drives/my", 200)), &folder))
	for _, f := range folder.Files {
		if want := map[string]bool{photo: true, text: true}[f.ID]; (f.Thumb != "") != want {
			t.Errorf("REST thumb of %s: %q", f.Name, f.Thumb)
		}
	}
	rest("/api/v1/disk/files/"+photo+"/thumb", 200)
	rest("/api/v1/disk/files/"+note+"/thumb", http.StatusNotFound)
}
