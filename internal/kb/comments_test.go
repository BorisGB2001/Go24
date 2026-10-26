package kb

import (
	"errors"
	"testing"
)

// TestKBComments: who sees a page comments on it, a reply to a reply goes under the same comment; the mentioned,
// the author replied to and the watchers are told once each, not who wrote it nor who does not see the base; only
// the author edits, the author or who manages the base deletes (with the replies); «Нравится» tells the page's
// author once; the views count those who opened the page.
func TestKBComments(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, dev, anna, out := newUser("boss"), newUser("dev"), newUser("anna"), newUser("out")
	s := NewStore(pool)
	sp, err := s.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	page, err := s.CreatePage(ctx, boss, sp, "", "Отпуск", "текст")
	must(err)
	talk := func(user string) Discussion {
		t.Helper()
		p, space, err := s.Page(ctx, user, page)
		must(err)
		d, err := s.Discussion(ctx, user, p, space)
		must(err)
		return d
	}
	notes := func(user, kind string) (n int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n))
		return n
	}

	// dev comments, mentioning anna: anna is mentioned, boss watches the page
	c1, err := s.AddComment(ctx, dev, page, "", "  Сколько дней? @anna  ")
	must(err)
	if c1.Body != "Сколько дней? @anna" || c1.Author != "dev" {
		t.Fatalf("comment %+v", c1)
	}
	if notes(anna, "kb_mention") != 1 || notes(boss, "kb_comment") != 1 || notes(dev, "kb_comment") != 0 || notes(anna, "kb_comment") != 0 {
		t.Fatal("comment notices")
	}
	// boss replies: dev hears of the reply (not also as a watcher), boss — nothing of their own
	r1, err := s.AddComment(ctx, boss, page, c1.ID, "28 дней")
	must(err)
	if notes(dev, "kb_reply") != 1 || notes(boss, "kb_reply") != 0 || notes(boss, "kb_comment") != 1 {
		t.Fatal("reply notices")
	}
	// a reply to the reply goes under the first comment
	r2, err := s.AddComment(ctx, anna, page, r1.ID, "спасибо")
	must(err)
	if r2.ParentID != c1.ID {
		t.Fatalf("reply to a reply under %q", r2.ParentID)
	}
	if _, err := s.AddComment(ctx, dev, page, "", " "); err == nil {
		t.Fatal("empty comment")
	}

	// a closed base: out neither comments nor hears of a mention
	must(s.SetRight(ctx, boss, sp, "user", dev, "edit"))
	must(s.SetRight(ctx, boss, sp, "user", anna, "read"))
	if _, err := s.AddComment(ctx, out, page, "", "можно?"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("outsider comments: %v", err)
	}
	if _, err := s.AddComment(ctx, anna, page, "", "@out посмотри"); err != nil {
		t.Fatal(err)
	}
	if notes(out, "kb_mention") != 0 {
		t.Fatal("mention outside the base")
	}

	d := talk(dev)
	if d.Count != 4 || len(d.Comments) != 2 || len(d.Comments[0].Replies) != 2 || !d.Comments[0].CanEdit || d.Comments[1].CanDelete {
		t.Fatalf("discussion %+v", d)
	}

	// only the author edits; anna (reads) cannot delete dev's comment, boss (manages) can — with its replies
	if err := s.EditComment(ctx, anna, c1.ID, "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("edit by another: %v", err)
	}
	must(s.EditComment(ctx, dev, c1.ID, "Сколько дней отпуска?"))
	if err := s.DeleteComment(ctx, anna, c1.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete by a reader: %v", err)
	}
	must(s.DeleteComment(ctx, boss, c1.ID))
	if d = talk(dev); d.Count != 1 {
		t.Fatalf("after delete %d", d.Count)
	}

	// likes: boss hears once; views count who opened the page
	must(s.Like(ctx, dev, page, true))
	must(s.Like(ctx, dev, page, true))
	must(s.Like(ctx, anna, page, true))
	must(s.Like(ctx, boss, page, true))
	must(s.Like(ctx, boss, page, false))
	must(s.Viewed(ctx, dev, page))
	must(s.Viewed(ctx, anna, page))
	must(s.Viewed(ctx, dev, page))
	d = talk(anna)
	if len(d.Likes) != 2 || !d.Liked || d.Views != 2 || notes(boss, "kb_like") != 1 {
		t.Fatalf("likes %+v, notices %d", d, notes(boss, "kb_like"))
	}
}
