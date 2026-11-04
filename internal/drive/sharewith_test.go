package drive

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestShareFileWith: a document made in a chat is opened to the chat's people for editing without «Вам открыли файл»;
// the maker and the inactive are skipped, an edit access already given is not lowered, and only the owner of «Мой
// диск» shares its file.
func TestShareFileWith(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	var anna, ivan, gone string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, active) VALUES ('gone', 'Уволен', 'g@t', false) RETURNING id`).Scan(&gone))
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	var doc string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner)
		VALUES (gen_random_uuid()::text, 'Документ.docx', 3, $1, 'user', $1) RETURNING id`, anna).Scan(&doc))

	must(s.ShareFileWith(ctx, anna, my, doc, []string{anna, ivan, gone}, "edit"))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_shares WHERE file_id = $1`, doc).Scan(&n))
	if n != 1 {
		t.Errorf("shares: %d, want Ivan's only", n)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n))
	if n != 0 {
		t.Errorf("notifications: %d, the chat's message tells", n)
	}
	d, err := s.Open(ctx, ivan, "sf:"+doc)
	must(err)
	if d.Access != "edit" {
		t.Errorf("Ivan's access: %q", d.Access)
	}
	must(s.ShareFileWith(ctx, anna, my, doc, []string{ivan}, "read"))
	if d, _ := s.Open(ctx, ivan, "sf:"+doc); d.Access != "edit" {
		t.Errorf("the edit access went down to %q", d.Access)
	}
	ivanMy, _ := s.Open(ctx, ivan, "my")
	if err := s.ShareFileWith(ctx, ivan, ivanMy, doc, []string{anna}, "edit"); !errors.Is(err, ErrNotFound) {
		t.Errorf("someone else's file: %v", err)
	}
	if err := s.ShareFileWith(ctx, anna, my, doc, []string{ivan}, "full"); !errors.Is(err, ErrForbidden) {
		t.Errorf("a file has no «full»: %v", err)
	}
}
