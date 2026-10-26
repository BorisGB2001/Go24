package feed

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

func TestReactionOf(t *testing.T) {
	for key, want := range map[string]string{"👍": "like", "👍🏽": "like", "😂": "laugh", "❤️": "kiss", "🤦‍♂️": "facepalm", "😡": "angry", "🎉": "like"} {
		if got := ReactionOf(key); got != want {
			t.Errorf("ReactionOf(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestReactions(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, n).Scan(&id))
		return id
	}
	boss, anna, ivan, guest := newUser("boss"), newUser("anna"), newUser("ivan"), newUser("guest")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	post, err := s.CreatePost(ctx, boss, "", "", "Новость", false)
	must(err)
	secret, err := s.CreatePost(ctx, boss, p.ID, "", "Проекту", false)
	must(err)
	c, err := s.AddComment(ctx, anna, post, "Здорово")
	must(err)

	// each person has one reaction; another one replaces it, the same one again takes it back
	_, err = s.React(ctx, anna, post, "laugh")
	must(err)
	_, err = s.React(ctx, ivan, post, "laugh")
	must(err)
	sm, err := s.React(ctx, boss, post, "kiss")
	must(err)
	if sm.Mine != "kiss" || sm.Total != 3 || len(sm.Top) != 2 || sm.Top[0].Reaction != "laugh" || sm.Top[0].N != 2 || sm.Top[0].Emoji() != "😂" {
		t.Fatalf("summary = %+v", sm)
	}
	if sm, _ = s.React(ctx, ivan, post, "angry"); sm.Mine != "angry" || sm.Total != 3 || sm.Top[0].Reaction != "kiss" {
		t.Fatalf("changed = %+v", sm) // a tie: in the picker's order (kiss, laugh, angry)
	}
	if sm, _ = s.React(ctx, ivan, post, "angry"); sm.Mine != "" || sm.Total != 2 {
		t.Fatalf("taken back = %+v", sm)
	}
	if _, err := s.React(ctx, ivan, post, "boo"); err == nil {
		t.Fatal("unknown reaction accepted")
	}
	if _, err := s.React(ctx, ivan, secret, "like"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider reacts to a project post: %v", err)
	}
	who, err := s.Reactors(ctx, ivan, post)
	must(err)
	if len(who) != 2 || who[0].ID != boss || who[0].Emoji() != "😍" || who[1].Reaction != "laugh" {
		t.Fatalf("reactors = %+v", who)
	}

	// comments: the same, shown with the post
	_, sm, err = s.ReactComment(ctx, boss, c.ID, "wonder")
	must(err)
	if sm.Mine != "wonder" || sm.Total != 1 {
		t.Fatalf("comment reaction = %+v", sm)
	}
	if _, _, err := s.ReactComment(ctx, guest, c.ID, "like"); err != nil {
		t.Fatalf("a company post's comment: %v", err)
	}
	if who, _ = s.CommentReactors(ctx, anna, c.ID); len(who) != 2 || who[1].ID != boss {
		t.Fatalf("comment reactors = %+v", who)
	}
	it, err := s.Item(ctx, anna, post)
	must(err)
	if it.Post.Reactions.Mine != "laugh" || it.Post.Reactions.Total != 2 || it.Post.Comments[0].Reactions.Total != 2 || it.Post.Comments[0].Reactions.Mine != "" {
		t.Fatalf("item = %+v", it.Post)
	}

	// the authors are notified with the reaction's emoji (not of their own ones, not again on a change)
	var kinds []string
	rows, err := pool.Query(ctx, `SELECT kind || ':' || body FROM notifications WHERE user_id = $1 ORDER BY id`, boss)
	must(err)
	for rows.Next() {
		var k string
		must(rows.Scan(&k))
		kinds = append(kinds, k)
	}
	if len(kinds) != 3 || kinds[0] != "post_comment:Здорово" || kinds[1] != "like:😂" || kinds[2] != "like:😂" {
		t.Fatalf("boss notified = %v", kinds)
	}
	var body string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'comment_like' ORDER BY id LIMIT 1`, anna).Scan(&body))
	if body != "😮 Здорово" {
		t.Fatalf("comment like notification = %q", body)
	}

	// the messenger bridge: a reaction keeps its event, the event finds it
	must(s.SetCommentReaction(ctx, ivan, c.ID, "cry", "$r1"))
	if what, id, owner, _ := s.ByEvent(ctx, "$r1"); what != "clike" || id != c.ID || owner != ivan {
		t.Fatalf("by event = %s %s %s", what, id, owner)
	}
	if r, ev := s.MyReaction(ctx, c.ID, ivan, true); r != "cry" || ev != "$r1" {
		t.Fatalf("my reaction = %s %s", r, ev)
	}
	must(s.SetCommentReaction(ctx, ivan, c.ID, "", ""))
	if r, _ := s.MyReaction(ctx, c.ID, ivan, true); r != "" {
		t.Fatalf("taken back from the messenger: %s", r)
	}
}
