package feed

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

func TestFeed(t *testing.T) {
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
	boss, dev, other := newUser("boss"), newUser("dev"), newUser("other")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	must(ts.SetMember(ctx, p, dev, "member"))

	if _, err := s.CreatePost(ctx, boss, "", "", "  ", false); err == nil {
		t.Fatal("empty post accepted")
	}
	all, err := s.CreatePost(ctx, boss, "", "Новость", "Всем привет", true)
	must(err)
	proj, err := s.CreatePost(ctx, dev, p.ID, "", "Только проекту", false)
	must(err)
	if _, err := s.CreatePost(ctx, other, p.ID, "", "чужой проект", false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider posts to a project: %v", err)
	}
	key, err := ts.CreateTask(ctx, dev, p, tasks.NewTask{Title: "Задача ленты"})
	must(err)

	items, err := s.List(ctx, other, Filter{})
	must(err)
	if len(items) != 1 || items[0].Post == nil || items[0].Post.ID != all {
		t.Fatalf("outsider sees %d items", len(items)) // no project post, no task event
	}
	items, err = s.List(ctx, dev, Filter{})
	must(err)
	if len(items) != 3 || items[0].Event == nil || items[0].Event.TaskKey != key || items[1].Post.ID != proj {
		t.Fatalf("member feed = %+v", items)
	}
	if _, err := s.AddComment(ctx, other, proj, "я тут"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider comments a project post: %v", err)
	}
	_, err = s.AddComment(ctx, other, all, "спасибо")
	must(err)
	sm, err := s.React(ctx, dev, all, "like")
	must(err)
	if sm.Mine != "like" || sm.Total != 1 {
		t.Fatalf("like = %+v", sm)
	}
	if sm, _ = s.React(ctx, dev, all, "like"); sm.Mine != "" || sm.Total != 0 {
		t.Fatalf("unlike = %+v", sm)
	}
	if u, _ := s.Unread(ctx, dev); u != 1 {
		t.Fatalf("unread important = %d", u)
	}
	if u, _ := s.Unread(ctx, boss); u != 0 {
		t.Fatalf("author's own important post counted: %d", u)
	}
	n, err := s.MarkRead(ctx, dev, all)
	must(err)
	if u, _ := s.Unread(ctx, dev); u != 0 || n != 1 {
		t.Fatalf("after read: unread %d, readers %d", u, n)
	}
	imp, _ := s.List(ctx, dev, Filter{Important: true})
	if len(imp) != 1 || !imp[0].Post.Read || len(imp[0].Post.Comments) != 1 {
		t.Fatalf("important view = %+v", imp)
	}
	if err := s.DeletePost(ctx, dev, all); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-author deletes: %v", err)
	}
	// files of a post: listed with it, seen by its audience only
	_, err = pool.Exec(ctx, `INSERT INTO files (object_key, name, size, content_type, uploaded_by, post_id)
		VALUES ('k1', 'схема.png', 10, 'image/png', $1, $2), ('k2', 'план.pdf', 20, 'application/pdf', $1, $2)`, dev, proj)
	must(err)
	mine, _ := s.List(ctx, dev, Filter{Mine: true})
	if len(mine) != 1 || len(mine[0].Post.Files) != 2 || !mine[0].Post.Files[0].Image() || mine[0].Post.Files[1].Image() {
		t.Fatalf("post files = %+v", mine)
	}
	if err := s.CanSee(ctx, other, proj); err == nil {
		t.Fatal("a stranger sees the project post's files")
	}
	// a poll in the post to everyone: one answer, a vote can be changed and taken back
	if err := s.AddPoll(ctx, boss, all, "Когда?", []string{"Пн", " Пн "}, PollKind{}); err == nil {
		t.Fatal("a poll with one distinct answer")
	}
	if err := s.AddPoll(ctx, dev, all, "Когда?", []string{"Пн", "Вт"}, PollKind{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a poll in someone else's post: %v", err)
	}
	must(s.AddPoll(ctx, boss, all, "Когда?", []string{"Пн", "Вт", "Ср"}, PollKind{}))
	poll := func(user string) *Poll {
		t.Helper()
		it, err := s.Item(ctx, user, all)
		must(err)
		return it.Post.Poll
	}
	opts := poll(dev).Options
	if err := s.Vote(ctx, dev, all, []string{opts[0].ID, opts[1].ID}); err == nil {
		t.Fatal("two answers in a single-choice poll")
	}
	must(s.Vote(ctx, dev, all, []string{opts[0].ID}))
	must(s.Vote(ctx, other, all, []string{opts[0].ID}))
	must(s.Vote(ctx, dev, all, []string{opts[1].ID})) // changed
	if p := poll(dev); p.Voters != 2 || p.Options[0].Votes != 1 || !p.Options[1].Mine || !p.Voted || p.Percent(p.Options[1]) != 50 {
		t.Fatalf("poll = %+v", p)
	}
	must(s.Vote(ctx, dev, all, nil))
	if p := poll(dev); p.Voted || p.Voters != 1 {
		t.Fatalf("after taking the vote back: %+v", p)
	}
	// «Кто голосовал»; the author stops the poll — no more votes — and resumes it
	if vs, err := s.PollVoters(ctx, dev, all); err != nil || len(vs) != 1 || vs[0].ID != other || vs[0].OptionID != opts[0].ID {
		t.Fatalf("voters = %+v, %v", vs, err)
	}
	if err := s.ClosePoll(ctx, dev, all, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else stops the poll: %v", err)
	}
	must(s.ClosePoll(ctx, boss, all, true))
	if err := s.Vote(ctx, dev, all, []string{opts[2].ID}); err == nil {
		t.Fatal("a vote in a stopped poll")
	}
	if p := poll(dev); !p.Closed || p.Voters != 1 {
		t.Fatalf("stopped poll = %+v", p)
	}
	must(s.ClosePoll(ctx, boss, all, false))
	must(s.Vote(ctx, dev, all, []string{opts[2].ID}))
	// an anonymous poll: the counts only
	anon, err := s.CreatePost(ctx, boss, "", "", "Опрос", false)
	must(err)
	must(s.AddPoll(ctx, boss, anon, "Анонимно?", []string{"Да", "Нет"}, PollKind{Anonymous: true}))
	ap, err := s.Item(ctx, dev, anon)
	must(err)
	if !ap.Post.Poll.Anonymous {
		t.Fatalf("anonymous poll = %+v", ap.Post.Poll)
	}
	must(s.Vote(ctx, dev, anon, []string{ap.Post.Poll.Options[0].ID}))
	if _, err := s.PollVoters(ctx, dev, anon); !errors.Is(err, ErrForbidden) {
		t.Fatalf("voters of an anonymous poll: %v", err)
	}
	// a gratitude: dev and other thanked; they are notified, the profile lists it to those who see the post
	thanks, err := s.CreatePost(ctx, boss, p.ID, "", "Спасибо за релиз", false)
	must(err)
	if err := s.SetGratitude(ctx, boss, thanks, "medal", []string{dev}); err == nil {
		t.Fatal("an unknown badge")
	}
	must(s.SetGratitude(ctx, boss, thanks, "cup", []string{dev, boss}))
	it, err := s.Item(ctx, dev, thanks)
	must(err)
	if g := it.Post.Gratitude; g == nil || g.Emoji() != "🏆" || len(g.People) != 1 || g.People[0].ID != dev {
		t.Fatalf("gratitude = %+v (the author does not thank himself)", it.Post.Gratitude)
	}
	var notified int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'gratitude' AND user_id = $1 AND post_id = $2`, dev, thanks).Scan(&notified))
	if got, _ := s.Gratitudes(ctx, dev, dev); len(got) != 1 || notified != 1 {
		t.Fatalf("dev's thanks = %+v, notified %d", got, notified)
	}
	if got, _ := s.Gratitudes(ctx, other, dev); len(got) != 0 {
		t.Fatal("a project post's gratitude seen outside the project")
	}
	// «Благодарности» of the company: the project's gratitude only to its members; the honor board by count
	general, err := s.CreatePost(ctx, boss, "", "", "Спасибо всем", false)
	must(err)
	must(s.SetGratitude(ctx, boss, general, "star", []string{dev, other}))
	if b, _ := s.Board(ctx, dev, BoardFilter{}); len(b) != 2 || b[0].PostID != general || len(b[0].People) != 2 || b[0].Author.ID != boss {
		t.Fatalf("dev's board = %+v", b)
	}
	if b, _ := s.Board(ctx, other, BoardFilter{}); len(b) != 1 {
		t.Fatalf("other's board = %+v (the project's gratitude is not seen)", b)
	}
	if l, _ := s.Leaders(ctx, dev, BoardFilter{}, 10); len(l) != 2 || l[0].ID != dev || l[0].Count != 2 || len(l[0].Badges) != 2 {
		t.Fatalf("leaders = %+v", l)
	}
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Отдел') RETURNING id`).Scan(&dept))
	var sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Подотдел', $1) RETURNING id`, dept).Scan(&sub))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, other, sub)
	must(err)
	if l, _ := s.Leaders(ctx, dev, BoardFilter{DepartmentID: dept}, 10); len(l) != 1 || l[0].ID != other {
		t.Fatalf("leaders of the department (with sub-departments) = %+v", l)
	}
	if b, _ := s.Board(ctx, dev, BoardFilter{DepartmentID: dept}); len(b) != 1 || len(b[0].People) != 2 {
		t.Fatalf("board of the department = %+v (a gratitude keeps all its people)", b)
	}
	if b, _ := s.Board(ctx, dev, BoardFilter{Badge: "cup", UserID: dev}); len(b) != 1 || b[0].PostID != thanks {
		t.Fatalf("board by badge and person = %+v", b)
	}
	if b, _ := s.Board(ctx, dev, BoardFilter{From: time.Now().AddDate(0, 0, 1)}); len(b) != 0 {
		t.Fatalf("board from tomorrow = %+v", b)
	}
	if b, _ := s.Board(ctx, dev, BoardFilter{To: time.Now()}); len(b) != 2 {
		t.Fatalf("board till today = %+v (the day is included)", b)
	}
	must(s.DeletePost(ctx, boss, all))
	if err := s.DeletePost(tasks.WithAdmin(ctx), other, proj); err != nil { // admin mode
		t.Fatal(err)
	}
}
