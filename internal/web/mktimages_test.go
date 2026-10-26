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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/marketing"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMktImagesHTTP: a picture of a letter is uploaded by who works with campaigns and is served to anyone by its
// address; only images are taken.
func TestMktImagesHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseURL: "http://go24.test/"}
	srv.Marketing = marketing.NewStore(pool, srv.CRM)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]

	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0}, 100)...)
	upload := func(c *http.Cookie, data []byte) (int, map[string]string) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "pic.png")
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/marketing/images", &buf)
		if c != nil {
			req.AddCookie(c)
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		must(err)
		defer res.Body.Close()
		out := map[string]string{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	code, out := upload(cookie, png)
	if code != http.StatusOK || !strings.HasPrefix(out["url"], "http://go24.test/m/i/") || !strings.HasSuffix(out["url"], ".png") {
		t.Fatalf("upload: %d %v", code, out)
	}
	name := strings.TrimPrefix(out["url"], "http://go24.test/m/i/")
	res, err := http.Get(hs.URL + "/m/i/" + name) // no sign-in: the addressee's mail client
	must(err)
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, png) || res.Header.Get("Content-Type") != "image/png" ||
		res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the picture: %d %s %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(got))
	}
	for _, bad := range []string{"0123456789abcdef0123456789abcdef.png", "../x.png", name + "x", strings.TrimSuffix(name, ".png") + ".svg"} {
		if res, err := http.Get(hs.URL + "/m/i/" + bad); err == nil {
			res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("%s: %d", bad, res.StatusCode)
			}
		}
	}
	if code, _ := upload(cookie, []byte("<svg xmlns='http://www.w3.org/2000/svg'><script>alert(1)</script></svg>")); code == http.StatusOK {
		t.Fatal("an SVG is taken")
	}
	if code, out := upload(nil, png); code == http.StatusOK && out["url"] != "" {
		t.Fatal("uploaded without signing in")
	}
}
