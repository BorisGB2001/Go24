package web

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// TestIMQuoteMenu: «Цитировать» is in the menu of every message, «Сохранить на Диск» — of a file's (instead of
// «Копировать»); the message carries its author for the quote.
func TestIMQuoteMenu(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	msgs := []matrixsync.Message{{ID: "$t", RoomID: "!r", SenderName: "Анна", Body: "привет", At: time.Now()},
		{ID: "$f", RoomID: "!r", SenderName: "Борис", Body: "акт.pdf", At: time.Now(), File: &matrixsync.File{Kind: "file", Name: "акт.pdf"}}}
	if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imlist", msgs); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Count(out, `data-act="quote"`) != 2 || strings.Count(out, "Цитировать") != 2 {
		t.Errorf("«Цитировать» should be in both menus:\n%s", out)
	}
	if strings.Count(out, `data-act="drive"`) != 1 || strings.Count(out, `data-act="copy"`) != 1 || !strings.Contains(out, "Сохранить на Диск") {
		t.Errorf("«Сохранить на Диск» is for the file only, «Копировать» for the text:\n%s", out)
	}
	for _, want := range []string{`data-sender="Анна"`, `data-sender="Борис"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the messages lack %q", want)
		}
	}
}

// TestIMDriveFolder: chat files go to «Сохранённые файлы» at the top of «Мой диск» — made once, then the same folder,
// whatever the case of its name.
func TestIMDriveFolder(t *testing.T) {
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
	var ann string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ann', 'Анна') RETURNING id`).Scan(&ann); err != nil {
		t.Fatal(err)
	}
	bundle, _ := i18n.Load("")
	ts := tasks.NewStore(pool)
	s := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), I18n: bundle}
	r := httptest.NewRequest("POST", "/im/file/drive", nil)
	r.Header.Set("Accept-Language", "ru-RU")
	sess := auth.Session{UserID: ann}
	d, err := s.Drive.Open(ctx, ann, "my")
	if err != nil {
		t.Fatal(err)
	}
	name := s.tr(r, "im.drive_folder")
	first, err := s.driveFolderNamed(r, sess, d, name)
	if err != nil || first == "" || name != "Сохранённые файлы" {
		t.Fatalf("the folder: %q %q %v", name, first, err)
	}
	again, err := s.driveFolderNamed(r, sess, d, strings.ToUpper(name))
	if err != nil || again != first {
		t.Errorf("the folder made twice: %q %q %v", first, again, err)
	}
	folders, _, _, err := s.Drive.List(ctx, d, "")
	if err != nil || len(folders) != 1 {
		t.Errorf("the drive's folders: %v %v", folders, err)
	}
	if got := s.tr(r, "im.drive_saved"); !strings.Contains(got, "Сохранённые файлы") {
		t.Errorf("the answer: %q", got)
	}
}
