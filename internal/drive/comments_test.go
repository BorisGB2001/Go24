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

// TestDriveFileComments: «Обсуждение» of a file — the card tells of the file and its comments; the uploader and the
// earlier commenters who may still read the file hear of a new comment; its author or an administrator deletes it.
func TestDriveFileComments(t *testing.T) {
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
	var anna, ivan, olga string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'a@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'i@t') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'o@t') RETURNING id`).Scan(&olga))
	s := NewStore(pool, tasks.NewStore(pool))
	my, _ := s.Open(ctx, anna, "my")
	folder, err := s.CreateFolder(ctx, anna, my, "", "Работа")
	must(err)
	var plan string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner, folder_id)
		VALUES (gen_random_uuid()::text, 'план.docx', 3, $1, 'user', $1, $2) RETURNING id`, anna, folder).Scan(&plan))
	notes := func(user string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_comment' AND body = 'план.docx'`, user).Scan(&n))
		return n
	}

	if _, err := s.AddComment(ctx, anna, plan, "  "); err == nil {
		t.Error("an empty comment is added")
	}
	if _, err := s.AddComment(ctx, anna, "nope", "?"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a comment to no file: %v", err)
	}
	// the owner comments: nobody to tell
	_, err = s.AddComment(ctx, anna, plan, "Посмотрите, пожалуйста")
	must(err)
	if notes(anna) != 0 {
		t.Error("the author hears of their own comment")
	}
	// Ivan, the file shared with him, answers: Anna hears of it
	must(s.SetShare(ctx, anna, my, "", plan, "user", ivan, "read"))
	ivanSaid, err := s.AddComment(ctx, ivan, plan, "Готово\nвторая строка")
	must(err)
	if notes(anna) != 1 {
		t.Errorf("anna's notifications = %d", notes(anna))
	}
	var url2 string
	must(pool.QueryRow(ctx, `SELECT url FROM notifications WHERE user_id = $1 AND kind = 'disk_comment'`, anna).Scan(&url2))
	if url2 != "/files/"+plan+"/about#c-"+ivanSaid {
		t.Errorf("url = %s", url2)
	}
	// the file no longer shared with Ivan: he does not hear of Anna's next comment
	var share string
	must(pool.QueryRow(ctx, `SELECT id FROM drive_shares WHERE file_id = $1`, plan).Scan(&share))
	must(s.DeleteShare(ctx, anna, my, share))
	_, err = s.AddComment(ctx, anna, plan, "Спасибо")
	must(err)
	if notes(ivan) != 0 {
		t.Errorf("ivan, who may not read the file, has %d notifications", notes(ivan))
	}
	// shared again: he does
	must(s.SetShare(ctx, anna, my, "", plan, "user", ivan, "read"))
	_, err = s.AddComment(ctx, anna, plan, "Ещё правка")
	must(err)
	if notes(ivan) != 1 || notes(olga) != 0 {
		t.Errorf("notifications: ivan %d, olga %d", notes(ivan), notes(olga))
	}

	c, err := s.FileCard(ctx, plan)
	must(err)
	if c.Name != "план.docx" || c.Where != "Работа" || c.Uploader != "Анна" || c.Drive != "user" || c.DriveOwner != anna || len(c.Comments) != 4 {
		t.Fatalf("card = %+v", c)
	}
	if c.Comments[1].Author != "Иван" || c.Comments[1].Body != "Готово\nвторая строка" {
		t.Errorf("second comment = %+v", c.Comments[1])
	}
	counts, err := s.CommentCounts(ctx, []string{plan, folder})
	must(err)
	if len(counts) != 1 || counts[plan] != 4 {
		t.Errorf("counts = %v", counts)
	}
	if _, err := s.FileCard(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("card of no file: %v", err)
	}

	// only the author deletes a comment, or an administrator
	if err := s.DeleteComment(ctx, anna, plan, ivanSaid); !errors.Is(err, ErrNotFound) {
		t.Errorf("anna deletes ivan's comment: %v", err)
	}
	must(s.DeleteComment(ctx, ivan, plan, ivanSaid))
	if err := s.DeleteComment(ctx, olga, plan, c.Comments[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("olga deletes anna's comment: %v", err)
	}
	must(s.DeleteComment(tasks.WithAdmin(ctx), olga, plan, c.Comments[0].ID))
	if c, _ = s.FileCard(ctx, plan); len(c.Comments) != 2 {
		t.Errorf("comments left: %d", len(c.Comments))
	}
	// an administrator's comment: those who may not read the file do not hear of it even so
	var ext string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner)
		VALUES (gen_random_uuid()::text, 'план.docx', 3, $1, 'user', $1) RETURNING id`, olga).Scan(&ext))
	_, err = s.AddComment(ctx, anna, ext, "?")
	must(err)
	_, err = s.AddComment(tasks.WithAdmin(ctx), ivan, ext, "!")
	must(err)
	if notes(anna) != 1 {
		t.Errorf("anna, who may not read olga's file, heard of the admin's comment: %d", notes(anna))
	}
}
