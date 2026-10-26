package bp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestLoopStart: «Цикл» goes back while its condition holds (a request asked again), MaxLoops times at most;
// «Запуск бизнес-процесса» starts another with the fields filled and goes on; a chain of starts is MaxDepth long at most.
func TestLoopStart(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, ann := newUser("boss"), newUser("ann")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))

	var ve *i18n.Error
	is := func(err error, key string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	fixed := []Field{{Key: "fixed", Label: "Исправлено", Kind: "bool"}}
	for _, steps := range [][]Step{
		{{Kind: "notify", To: "starter", Text: "x"}, {Kind: "loop", Field: "sum", Op: "eq", Value: "1", Goto: 2}}, // not back
		{{Kind: "notify", To: "starter", Text: "x"}, {Kind: "loop", Field: "sum", Op: "eq", Value: "1"}},          // nowhere
		{{Kind: "parallel", Branches: []int{3}, Goto: 4}, {Kind: "notify", To: "starter", Text: "a"}, {Kind: "notify", To: "starter", Text: "b"},
			{Kind: "loop", Field: "sum", Op: "eq", Value: "1", Goto: 1}}, // over a parallel step
	} {
		_, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}}, Steps: steps})
		is(err, "err.bp_loop")
	}

	// a request asked again until «Исправлено»
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Правки", Active: true, Steps: []Step{
		{Kind: "request", To: boss, Text: "Исправьте", Fields: fixed},
		{Kind: "loop", Field: "fixed", Op: "eq", Value: "N", Goto: 1},
		{Kind: "notify", To: "starter", Text: "Готово"}}})
	must(err)
	id, err := s.Start(ctx, ann, tpl, "", nil)
	must(err)
	for _, v := range []string{"N", "N", "Y"} {
		a, _ := s.MyApprovals(ctx, boss)
		if len(a) != 1 {
			t.Fatalf("boss is asked %d times", len(a))
		}
		must(s.Answer(ctx, boss, a[0].ID, map[string]string{"fixed": v}, ""))
	}
	if x, _ := s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}
	// a counter: «= {n} + 1» while n < 3
	tpl, err = s.SaveTemplate(ctx, boss, Template{Name: "Счётчик", Active: true, Fields: []Field{{Key: "n", Label: "N", Kind: "number", Default: "0", Scope: "var"}},
		Steps: []Step{{Kind: "set", Field: "n", Value: "= {n} + 1"}, {Kind: "loop", Field: "n", Op: "lt", Value: "= 6 / 2", Goto: 1}}})
	must(err)
	id, err = s.Start(ctx, ann, tpl, "", nil)
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "done" || x.Values["n"] != "3" {
		t.Fatalf("the counter: %s %+v", x.Status, x.Values)
	}
	// «Пауза» till the date of a field: a past one does not stop the process
	_, err = s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Fields: []Field{{Key: "x", Label: "X", Kind: "string"}},
		Steps: []Step{{Kind: "wait", Field: "x"}}})
	is(err, "err.bp_step")
	tpl, err = s.SaveTemplate(ctx, boss, Template{Name: "Пауза", Active: true, Fields: []Field{{Key: "at", Label: "Когда", Kind: "datetime"}},
		Steps: []Step{{Kind: "wait", Field: "at"}, {Kind: "notify", To: "starter", Text: "Пора"}}})
	must(err)
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"at": "2020-01-01 10:00"})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("a past date paused: %s", x.Status)
	}
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"at": "2099-01-01 10:00"})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "running" || x.WakeAt == nil || x.WakeAt.In(time.Local).Format("2006-01-02T15:04") != "2099-01-01T10:00" {
		t.Fatalf("the pause: %s %v", x.Status, x.WakeAt)
	}
	// «Сообщение в ленту» of boss to ann, the fields filled; without the feed — an error
	tpl, err = s.SaveTemplate(ctx, boss, Template{Name: "Новость", Active: true, Fields: []Field{{Key: "what", Label: "Что", Kind: "string"}},
		Steps: []Step{{Kind: "post", From: boss, Users: []string{"starter"}, Subject: "Новость: {what}", Text: "Готово: {what}"}}})
	must(err)
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"what": "отчёт"})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "failed" {
		t.Fatalf("posted without the feed: %s", x.Status)
	}
	s.Feed = feed.NewStore(pool, s.Tasks)
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"what": "отчёт"})
	must(err)
	posts, err := s.Feed.List(ctx, ann, feed.Filter{})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "done" || len(posts) == 0 || posts[0].Post.AuthorID != boss ||
		posts[0].Post.Title != "Новость: отчёт" || posts[0].Post.Body != "Готово: отчёт" {
		t.Fatalf("the post: %s %+v", x.Status, posts)
	}
	s.Feed = nil
	// a loop that never ends fails
	tpl, err = s.SaveTemplate(ctx, boss, Template{Name: "Вечный", Active: true, Fields: []Field{{Key: "x", Label: "X", Kind: "string", Default: "a"}},
		Steps: []Step{{Kind: "set", Field: "x", Value: "a"}, {Kind: "loop", Field: "x", Op: "eq", Value: "a", Goto: 1}}})
	must(err)
	id, err = s.Start(ctx, ann, tpl, "", nil)
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "failed" {
		t.Fatalf("an endless loop: %s", x.Status)
	}

	// another process, of the starter, with the fields filled
	child, err := s.SaveTemplate(ctx, boss, Template{Name: "Оплата", Active: true,
		Fields: []Field{{Key: "who", Label: "Кто", Kind: "user", Required: true}, {Key: "sum", Label: "Сумма", Kind: "number", Required: true}},
		Steps:  []Step{{Kind: "approve", To: FieldPrefix + "who", Text: "Оплатить {sum}?"}}})
	must(err)
	_, err = s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: []Step{{Kind: "start"}}})
	is(err, "err.bp_step")
	parent, err := s.SaveTemplate(ctx, boss, Template{Name: "Закупка", Active: true, Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}},
		Steps: []Step{{Kind: "start", Template: child, Sets: []Assign{{Field: "who", Value: "boss@t"}, {Field: "sum", Value: "{sum}"}}}}})
	must(err)
	id, err = s.Start(ctx, ann, parent, "", map[string]string{"sum": "70"})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("the parent waits: %s", x.Status)
	}
	a, _ := s.MyApprovals(ctx, boss)
	if len(a) != 1 || a[0].Text != "Оплатить 70?" || a[0].StartedBy != "ann" {
		t.Fatalf("the child's approval: %+v", a)
	}
	if x, _ := s.Instance(ctx, ann, a[0].InstanceID); x.Parent != id || x.ParentName != "Закупка" {
		t.Fatalf("the child's parent: %q %q", x.Parent, x.ParentName)
	}
	// one starting itself: MaxDepth+1 processes, the last fails
	self, err := s.SaveTemplate(ctx, boss, Template{Name: "Сам", Active: true, Steps: []Step{{Kind: "notify", To: "starter", Text: "x"}}})
	must(err)
	st, _ := s.Template(ctx, self)
	st.Steps = []Step{{Kind: "start", Template: self}}
	_, err = s.SaveTemplate(ctx, boss, st)
	must(err)
	_, err = s.Start(ctx, ann, self, "", nil)
	must(err)
	var n, failed int
	must(pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status = 'failed') FROM bp_instances WHERE template_id = $1`, self).Scan(&n, &failed))
	if n != MaxDepth+1 || failed != 1 {
		t.Fatalf("a chain of %d, %d failed", n, failed)
	}
}
