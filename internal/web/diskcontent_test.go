package web

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// TestFileText: the text of a document for the search — Word, text in Windows-1251; a picture has none.
func TestFileText(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	w, _ := z.Create("word/document.xml")
	io.WriteString(w, `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`+
		`<w:p><w:r><w:t>Договор поставки</w:t></w:r></w:p><w:p><w:r><w:t>№ 15 &amp; приложение</w:t></w:r></w:p></w:body></w:document>`)
	z.Close()
	ctx := context.Background()
	if got := fileText(ctx, "д.docx", "", buf.Bytes()); !strings.Contains(got, "Договор поставки") || !strings.Contains(got, "№ 15 & приложение") {
		t.Errorf("docx text = %q", got)
	}
	if got := fileText(ctx, "a.txt", "text/plain", []byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}); got != "Привет" {
		t.Errorf("cp1251 text = %q", got)
	}
	if textual("a.png", "image/png") || !textual("a.pdf", "") || !textual("a.xlsx", "") {
		t.Error("textual")
	}
}

// TestDiskContentHTTP: a file found by its text shows where, on the drive and in «Документы»; the files with no
// bytes are read at once by IndexDrive.
func TestDiskContentHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	key := "k/" + randomHex(8)
	id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "отчёт.docx", UploadedBy: anna, Drive: "user", DriveOwner: anna})
	must(err)
	if n, err := srv.IndexDrive(ctx, 10); err != nil || n != 1 {
		t.Fatalf("index = %d, %v", n, err)
	}
	if n, _ := srv.IndexDrive(ctx, 10); n != 0 {
		t.Fatalf("indexed again: %d", n)
	}
	must(srv.Drive.SetContent(ctx, id, key, "Итоги года: <script>выручка</script> выросла"))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	get := func(path string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, res.StatusCode)
		}
		return string(raw)
	}
	for _, path := range []string{"/disk/my?q=%D0%B2%D1%8B%D1%80%D1%83%D1%87", "/docs?q=%D0%B2%D1%8B%D1%80%D1%83%D1%87"} { // «выруч»
		page := get(path)
		if !strings.Contains(page, `data-id="`+id+`"`) || !strings.Contains(page, `class="dsnip small"`) ||
			!strings.Contains(page, "script <mark>выручка</mark>") {
			t.Errorf("%s: no file found by its text with the place: %.2000s", path, page)
		}
	}
	if page := get("/disk/my?q=%D0%BE%D1%82%D1%87%D1%91%D1%82"); !strings.Contains(page, `data-id="`+id+`"`) || strings.Contains(page, `class="dsnip`) {
		t.Error("a file found by its name shows no text")
	}
}
