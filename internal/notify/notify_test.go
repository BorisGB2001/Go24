package notify

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTriggers: task and feed events notify the right people (never the actor, never on imports), and the
// list, counter and read marks work.
func TestTriggers(t *testing.T) {
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
	boss, worker, watcher, fired := user("boss"), user("worker"), user("watcher"), user("fired")
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, fired)
	must(err)
	var project, task string
	must(pool.QueryRow(ctx, `INSERT INTO projects (key, name, owner_id) VALUES ('GO', 'go', $1) RETURNING id`, boss).Scan(&project))
	must(pool.QueryRow(ctx, `INSERT INTO tasks (project_id, number, title, creator_id, responsible_id) VALUES ($1, 1, 'Отчёт', $2, $3) RETURNING id`,
		project, boss, worker).Scan(&task))
	_, err = pool.Exec(ctx, `INSERT INTO task_participants (task_id, user_id, role) VALUES ($1, $2, 'auditor'), ($1, $3, 'auditor')`, task, watcher, fired)
	must(err)
	history := func(actor, field, v string) {
		_, err := pool.Exec(ctx, `INSERT INTO task_history (task_id, actor_id, field, new_value) VALUES ($1, $2, $3, $4)`, task, actor, field, v)
		must(err)
	}
	s := NewStore(pool)
	kinds := func(u string) []string {
		t.Helper()
		list, err := s.List(ctx, u, 0, 50)
		must(err)
		var out []string
		for _, i := range list {
			out = append(out, i.Kind)
		}
		return out
	}

	history(boss, "created", "Отчёт")
	_, err = pool.Exec(ctx, `INSERT INTO task_comments (task_id, author_id, body) VALUES ($1, $2, 'готово')`, task, worker)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO task_comments (task_id, author_id, body, legacy_id) VALUES ($1, $2, 'импорт', 7)`, task, worker)
	must(err)
	history(worker, "status", "completed")
	history(boss, "title", "Новое имя") // not worth a notification

	if got := kinds(boss); join(got) != "status,comment" {
		t.Errorf("boss got %v, want status, comment", got)
	}
	if got := kinds(worker); join(got) != "task_new" {
		t.Errorf("worker got %v, want task_new only (own comment and status are not notified)", got)
	}
	if got := kinds(watcher); join(got) != "status,comment,task_new" {
		t.Errorf("watcher got %v", got)
	}
	if got := kinds(fired); len(got) != 0 {
		t.Errorf("a dismissed employee is notified: %v", got)
	}

	// quiet mode (imports)
	tx, err := pool.Begin(ctx)
	must(err)
	_, err = tx.Exec(ctx, `SELECT set_config('go24.quiet', 'on', true)`)
	must(err)
	_, err = tx.Exec(ctx, `INSERT INTO task_history (task_id, actor_id, field, new_value) VALUES ($1, $2, 'status', 'deferred')`, task, boss)
	must(err)
	must(tx.Commit(ctx))
	if got := kinds(worker); join(got) != "task_new" {
		t.Errorf("quiet import notified: %v", got)
	}

	// feed: likes go to the author, an important post to everyone active (comments are told by feed.AddComment)
	var post string
	must(pool.QueryRow(ctx, `INSERT INTO feed_posts (author_id, body, important) VALUES ($1, 'Всем!', true) RETURNING id`, boss).Scan(&post))
	_, err = pool.Exec(ctx, `INSERT INTO feed_likes (post_id, user_id) VALUES ($1, $2), ($1, $3)`, post, worker, boss)
	must(err)
	if got := kinds(boss); join(got) != "like,status,comment" {
		t.Errorf("boss after feed got %v", got)
	}
	if got := kinds(watcher); got[0] != "important" {
		t.Errorf("watcher did not get the important post: %v", got)
	}

	// counters and reading
	n, err := s.Unread(ctx, watcher)
	must(err)
	if n != 4 {
		t.Errorf("watcher unread = %d, want 4", n)
	}
	must(s.MarkTaskRead(ctx, watcher, task))
	if n, _ = s.Unread(ctx, watcher); n != 1 {
		t.Errorf("after opening the task unread = %d, want 1 (the post)", n)
	}
	list, err := s.List(ctx, watcher, 0, 1)
	must(err)
	it, err := s.Get(ctx, watcher, strconv.FormatInt(list[0].ID, 10))
	must(err)
	if it.Link() != "/feed#post-"+post || it.PostTitle != "Всем!" || it.ActorName != "boss" {
		t.Errorf("item = %+v, link %s", it, it.Link())
	}
	if _, err := s.Get(ctx, worker, strconv.FormatInt(it.ID, 10)); err != ErrNotFound {
		t.Errorf("someone else's notification: err = %v", err)
	}
	must(s.MarkRead(ctx, watcher, 0))
	if n, _ = s.Unread(ctx, watcher); n != 0 {
		t.Errorf("after read all unread = %d", n)
	}
	// settings: a kind turned off is not written; the messenger choice is kept
	must(s.SaveSettings(ctx, worker, map[string]bool{"comment": true}, true))
	st, err := s.Settings(ctx, worker)
	must(err)
	if !st.Matrix || st.Off["comment"] || !st.Off["status"] {
		t.Errorf("settings = %+v", st)
	}
	before, _ := s.Unread(ctx, worker)
	history(boss, "status", "in_progress")
	if after, _ := s.Unread(ctx, worker); after != before {
		t.Errorf("a status notice reached someone who turned them off")
	}
	_, err = pool.Exec(ctx, `INSERT INTO task_comments (task_id, author_id, body) VALUES ($1, $2, 'ещё')`, task, boss)
	must(err)
	if after, _ := s.Unread(ctx, worker); after != before+1 {
		t.Errorf("a wanted comment notice is missing")
	}
	tl, err := s.List(ctx, boss, 0, 50)
	must(err)
	for _, i := range tl {
		if i.Kind == "status" && (i.TaskKey != "GO-1" || i.Link() != "/t/GO-1") {
			t.Errorf("task item = %+v", i)
		}
	}
}

func join(list []string) string {
	out := ""
	for i, s := range list {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
