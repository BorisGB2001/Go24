package calendar

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestEventDiscussion: comments of a meeting — who may comment (not someone who sees only «Занят» of a private one),
// who hears of a comment (the organizer, the invited who did not decline, those who commented, never the author),
// deleting (the author only, with the comment's files leaving the meeting), the meeting's files taken off.
func TestEventDiscussion(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, petr, olga := user("boss"), user("anna"), user("petr"), user("olga")
	s := NewStore(pool)
	start := time.Now().Truncate(time.Hour).AddDate(0, 0, 1)
	id, err := s.Save(ctx, boss, Event{Title: "Планёрка", Start: start, End: start.Add(time.Hour), Private: true}, []string{anna, petr})
	must(err)
	if err := s.Respond(ctx, petr, id, "declined"); err != nil {
		t.Fatal(err)
	}
	notified := func(u string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'event_comment' AND event_id = $2`, u, id).Scan(&n))
		return n
	}

	if _, err := s.AddComment(ctx, olga, id, "что там?", nil); err == nil { // a private meeting: olga sees only «Занят»
		t.Fatal("olga commented on a private meeting")
	}
	if _, err := s.AddComment(ctx, anna, id, "  ", nil); err == nil {
		t.Fatal("an empty comment")
	}
	c1, err := s.AddComment(ctx, anna, id, "Возьму отчёт", nil)
	must(err)
	if notified(boss) != 1 || notified(anna) != 0 || notified(petr) != 0 {
		t.Fatalf("after anna: boss %d, anna %d, petr %d", notified(boss), notified(anna), notified(petr))
	}
	var url1 string
	must(pool.QueryRow(ctx, `SELECT url FROM notifications WHERE user_id = $1 AND kind = 'event_comment'`, boss).Scan(&url1))
	if url1 != "/calendar/"+id+"#c-"+c1 {
		t.Fatalf("url %q", url1)
	}
	// petr declined but comments: he hears of the next comment, as anna does
	c2, err := s.AddComment(ctx, petr, id, "", []string{"план.pdf"})
	must(err)
	_, err = s.AddComment(ctx, boss, id, "Хорошо", nil)
	must(err)
	if notified(boss) != 2 || notified(anna) != 2 || notified(petr) != 1 {
		t.Fatalf("after all: boss %d, anna %d, petr %d", notified(boss), notified(anna), notified(petr))
	}
	var body string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'event_comment' AND actor_id = $2`, anna, petr).Scan(&body))
	if body != "📎 план.pdf" {
		t.Fatalf("a comment of files only: %q", body)
	}

	// files: one of the meeting, one of petr's comment
	file := func(comment string) string {
		var f string
		must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, event_id, event_comment_id)
			VALUES ('k/' || gen_random_uuid(), 'план.pdf', 1, 'application/pdf', $1, $2, nullif($3, '')::uuid) RETURNING id`, boss, id, comment).Scan(&f))
		return f
	}
	own, cf := file(""), file(c2)
	d, err := s.Discussion(ctx, id)
	must(err)
	if len(d.Files) != 1 || d.Files[0].ID != own || len(d.Comments) != 3 || d.Comments[0].Body != "Возьму отчёт" ||
		len(d.Comments[1].Files) != 1 || d.Comments[1].Files[0].ID != cf {
		t.Fatalf("discussion %+v", d)
	}
	if err := s.DeleteComment(ctx, anna, id, c2); err != ErrNotFound {
		t.Fatalf("anna deleted petr's comment: %v", err)
	}
	must(s.DeleteComment(ctx, petr, id, c2))
	if d, _ = s.Discussion(ctx, id); len(d.Comments) != 2 || len(d.Files) != 1 {
		t.Fatalf("after deleting: %d comments, %d files", len(d.Comments), len(d.Files))
	}
	if err := s.RemoveFile(ctx, anna, id, own); err != ErrNotFound {
		t.Fatalf("anna removed the organizer's file: %v", err)
	}
	must(s.RemoveFile(ctx, boss, id, own))
	if d, _ = s.Discussion(ctx, id); len(d.Files) != 0 {
		t.Fatalf("files left %d", len(d.Files))
	}
	if _, err := s.Seen(ctx, olga, id); err == nil {
		t.Fatal("olga sees a private meeting in full")
	}
}
