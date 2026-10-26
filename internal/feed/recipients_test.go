package feed

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestRecipients: a post to chosen people and departments — seen by them (a department with its sub-departments,
// people who join it later too) and its author, not by others or guests; the addressees are told, an important post
// as important and only to them.
func TestRecipients(t *testing.T) {
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
	user := func(n string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, guest) VALUES ($1, $1, $2) RETURNING id`, n, guest).Scan(&id))
		return id
	}
	dept := func(name string, parent any) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ($1, $2) RETURNING id`, name, parent).Scan(&id))
		return id
	}
	boss, anna, ivan, petr, late, guest := user("boss", false), user("anna", false), user("ivan", false), user("petr", false), user("late", false), user("guest", true)
	root := dept("Компания", nil)
	sales := dept("Продажи", root)
	east := dept("Восток", sales)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, anna, east, petr, root)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool))

	if _, err := s.CreatePostTo(ctx, boss, "", Audience{Users: []string{guest}}, "", "гостю", false); err == nil {
		t.Fatal("a post to a guest only")
	}
	id, err := s.CreatePostTo(ctx, boss, "", Audience{Users: []string{ivan, guest}, Depts: []string{sales}}, "План", "Только продажам", false)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, late, east)
	must(err)
	sees := func(u string) bool {
		t.Helper()
		items, err := s.List(ctx, u, Filter{})
		must(err)
		for _, it := range items {
			if it.Post != nil && it.Post.ID == id {
				if len(it.Post.To) != 2 || !it.Post.To[0].Dept || it.Post.To[0].Name != "Продажи" || it.Post.To[1].ID != ivan {
					t.Fatalf("the addressees = %+v", it.Post.To)
				}
				return true
			}
		}
		return false
	}
	for u, want := range map[string]bool{boss: true, anna: true, ivan: true, late: true, petr: false, guest: false} {
		if sees(u) != want {
			t.Errorf("%s sees the post: %v, want %v", u, !want, want)
		}
	}
	if err := s.CanSee(ctx, petr, id); err == nil {
		t.Error("petr may open the post")
	}
	told := func(kind string) (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE post_id = $1 AND kind = $2`, id, kind).Scan(&n))
		return
	}
	if n := told("post_to_you"); n != 2 { // anna and ivan (late joined afterwards)
		t.Fatalf("told %d", n)
	}
	id, err = s.CreatePostTo(ctx, boss, "", Audience{Users: []string{ivan}}, "", "Важно", true)
	must(err)
	if n := told("important"); n != 1 {
		t.Fatalf("an important post to ivan told %d", n)
	}
}

// TestFeedFilter: words in a post or its comments, the author, the date, polls and gratitudes; a search shows no task
// events.
func TestFeedFilter(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('boss', 'boss') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'anna') RETURNING id`).Scan(&anna))
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	old, err := s.CreatePost(ctx, boss, "", "Отпуск", "График отпусков на 100%", false)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE feed_posts SET created_at = now() - interval '40 days' WHERE id = $1`, old)
	must(err)
	news, err := s.CreatePost(ctx, anna, "", "", "Новый сервер", false)
	must(err)
	_, err = s.AddComment(ctx, boss, news, "Где лежит конфиг?")
	must(err)
	poll, err := s.CreatePost(ctx, boss, "", "", "Куда едем?", false)
	must(err)
	must(s.AddPoll(ctx, boss, poll, "Куда?", []string{"Море", "Горы"}, false))
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	_, err = ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Задача", ResponsibleID: boss})
	must(err)
	got := func(f Filter) (out []string) {
		t.Helper()
		items, err := s.List(ctx, boss, f)
		must(err)
		for _, it := range items {
			if it.Post != nil {
				out = append(out, it.Post.ID)
			} else {
				out = append(out, "event")
			}
		}
		return
	}
	same := func(name string, a []string, b ...string) {
		t.Helper()
		if len(a) != len(b) {
			t.Fatalf("%s: %v, want %v", name, a, b)
		}
		for i := range a {
			if a[i] != b[i] {
				t.Fatalf("%s: %v, want %v", name, a, b)
			}
		}
	}
	same("words in a comment", got(Filter{Query: "КОНФИГ"}), news)
	same("words in the title", got(Filter{Query: "отпуск"}), old)
	same("a percent is a letter", got(Filter{Query: "100%"}), old)
	same("the author", got(Filter{Author: anna}), news)
	same("polls", got(Filter{Kind: "poll"}), poll)
	same("task events", got(Filter{Kind: "tasks"}), "event")
	if f := got(Filter{From: time.Now().AddDate(0, 0, -7)}); len(f) != 3 { // the event and two posts; not the old one
		t.Fatalf("the last week: %v", f)
	}
}

// TestMarks: «В избранное» and «Закрепить» are the viewer's own; pinned posts are left out of the general feed.
func TestMarks(t *testing.T) {
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
	var boss, anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('boss', 'boss') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'anna') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'ivan') RETURNING id`).Scan(&ivan))
	s := NewStore(pool, tasks.NewStore(pool))
	a, err := s.CreatePost(ctx, boss, "", "", "Первое", false)
	must(err)
	b, err := s.CreatePost(ctx, boss, "", "", "Второе", false)
	must(err)
	secret, err := s.CreatePostTo(ctx, boss, "", Audience{Users: []string{ivan}}, "", "Ивану", false)
	must(err)
	if _, err := s.Mark(ctx, anna, secret, "fav"); err == nil {
		t.Fatal("anna marked a post she does not see")
	}
	if _, err := s.Mark(ctx, anna, a, "star"); err == nil {
		t.Fatal("an unknown mark")
	}
	on, err := s.Mark(ctx, anna, a, "fav")
	must(err)
	if !on {
		t.Fatal("fav is off")
	}
	_, err = s.Mark(ctx, anna, b, "pin")
	must(err)
	ids := func(u string, f Filter) (out []string) {
		items, err := s.List(ctx, u, f)
		must(err)
		for _, it := range items {
			if it.Post != nil {
				out = append(out, it.Post.ID)
			}
		}
		return
	}
	if f := ids(anna, Filter{Favorites: true}); len(f) != 1 || f[0] != a {
		t.Fatalf("anna's favourites = %v", f)
	}
	if f := ids(anna, Filter{Pinned: true}); len(f) != 1 || f[0] != b {
		t.Fatalf("anna's pinned = %v", f)
	}
	if f := ids(anna, Filter{NoPinned: true}); len(f) != 1 || f[0] != a {
		t.Fatalf("anna's feed without the pinned = %v", f)
	}
	if f := ids(boss, Filter{Favorites: true}); len(f) != 0 {
		t.Fatalf("boss's favourites = %v", f)
	}
	on, err = s.Mark(ctx, anna, a, "fav")
	must(err)
	if on || len(ids(anna, Filter{Favorites: true})) != 0 {
		t.Fatal("fav did not go off")
	}
}

// TestShare: «Поделиться» adds people and departments to a post to chosen addressees or to a project; those who could
// not read it before see it and are told once, the others are not; a post to everyone and an unseen post are not shared.
func TestShare(t *testing.T) {
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
	user := func(n string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, guest) VALUES ($1, $1, $2) RETURNING id`, n, guest).Scan(&id))
		return id
	}
	boss, dev, anna, ivan, petr, guest := user("boss", false), user("dev", false), user("anna", false), user("ivan", false), user("petr", false), user("guest", true)
	var sales string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $2)`, anna, sales, dev)
	must(err)
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	must(ts.SetMember(ctx, p, dev, "member"))
	told := func(post string) (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE post_id = $1 AND kind = 'post_shared'`, post).Scan(&n))
		return
	}

	proj, err := s.CreatePost(ctx, dev, p.ID, "", "Проекту", false)
	must(err)
	if err := s.CanSee(ctx, anna, proj); err == nil {
		t.Fatal("anna sees a project post before the share")
	}
	if _, err := s.Share(ctx, petr, proj, Audience{Users: []string{petr}}); err == nil {
		t.Fatal("an outsider shared a project post")
	}
	// dev is a member already, anna comes with the department; the guest is no addressee
	n, err := s.Share(ctx, dev, proj, Audience{Users: []string{guest}, Depts: []string{sales}})
	must(err)
	if n != 1 || told(proj) != 1 {
		t.Fatalf("shared with %d, told %d", n, told(proj))
	}
	must(s.CanSee(ctx, anna, proj))
	if err := s.CanSee(ctx, guest, proj); err == nil {
		t.Error("the guest sees the shared post")
	}
	it, err := s.Item(ctx, anna, proj)
	must(err)
	if len(it.Post.To) != 1 || it.Post.To[0].Name != "Продажи" {
		t.Fatalf("the addressees of the project post = %+v", it.Post.To)
	}
	// anna, now a reader, shares on; the same department again adds nobody
	n, err = s.Share(ctx, anna, proj, Audience{Users: []string{ivan}, Depts: []string{sales}})
	must(err)
	if n != 1 || told(proj) != 2 {
		t.Fatalf("shared on with %d, told %d", n, told(proj))
	}
	must(s.CanSee(ctx, ivan, proj))

	lim, err := s.CreatePostTo(ctx, boss, "", Audience{Users: []string{ivan}}, "", "Ивану", false)
	must(err)
	if n, err = s.Share(ctx, ivan, lim, Audience{Users: []string{petr, boss}}); err != nil || n != 1 {
		t.Fatalf("a limited post shared with %d: %v", n, err)
	}
	must(s.CanSee(ctx, petr, lim))
	all, err := s.CreatePost(ctx, boss, "", "", "Всем", false)
	must(err)
	if _, err := s.Share(ctx, boss, all, Audience{Users: []string{petr}}); err == nil {
		t.Error("a post to everyone shared")
	}
	if _, err := s.Share(ctx, boss, lim, Audience{}); err == nil {
		t.Error("shared with nobody")
	}
}
