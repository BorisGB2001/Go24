package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestFeedEditWhole: «Изменить» a post as a whole — the form of a new post filled in (its author or
// an administrator only): addressees (all ↔ chosen people ↔ a project, the new readers told), «Важное» and its term
// (told once, also to the new readers), files taken off, the gratitude and the poll (until somebody votes).
func TestFeedEditWhole(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, ivan, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('petr', 'Пётр') RETURNING id`).Scan(&petr))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "ED", "Стройка", "")
	must(err)
	var project string
	must(pool.QueryRow(ctx, `SELECT id FROM projects WHERE key = 'ED'`).Scan(&project))
	srv := &Server{DB: pool, Tasks: ts, Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), ivan: cookie(ivan), petr: cookie(petr)}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	told := func(user, kind string) (n int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, user, kind).Scan(&n))
		return n
	}
	sees := func(user, post string) bool {
		t.Helper()
		return srv.Feed.CanSee(ctx, user, post) == nil
	}

	do(anna, "POST", "/feed", url.Values{"title": {"План"}, "body": {"Текст"}, "poll_question": {"Когда?"}, "poll_options": {"Утром\nВечером"}}, 201)
	var post string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_posts`).Scan(&post))
	var f1, f2 string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, post_id) VALUES ('k1', 'смета.pdf', 10, $1) RETURNING id`, post).Scan(&f1))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, post_id) VALUES ('k2', 'план.pdf', 10, $1) RETURNING id`, post).Scan(&f2))

	// the form: the author's, filled in; nobody else's
	form := do(anna, "GET", "/feed/"+post+"/edit", nil, 200)
	for _, want := range []string{`name=\"full\"`, `name=\"poll_edit\"`, `Когда?`, `Утром\nВечером`, `смета.pdf`, `name=\"detach\"`, `data-feed-to`} {
		if !strings.Contains(form, want) {
			t.Errorf("the edit form has no %s: %.2000s", want, form)
		}
	}
	do(ivan, "GET", "/feed/"+post+"/edit", nil, 403)
	do(ivan, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "body": {"чужое"}}, 403)

	// to Иван only, important for a week, a gratitude to Пётр, another poll, one file off
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "title": {"План работ"}, "body": {"Новый текст"},
		"project": {"people"}, "to": {ivan}, "important": {"on"}, "important_for": {"week"},
		"gratitude": {"on"}, "badge": {"star"}, "thank": {petr},
		"poll_edit": {"1"}, "poll_question": {"Когда лучше?"}, "poll_options": {"Утром\nДнём\nВечером"}, "poll_multiple": {"on"},
		"detach": {f1}}, 200)
	if !sees(ivan, post) || sees(petr, post) {
		t.Fatalf("addressees after the edit: Иван %v, Пётр %v", sees(ivan, post), sees(petr, post))
	}
	if told(ivan, "important") != 1 || told(ivan, "post_to_you") != 0 || told(petr, "important") != 0 {
		t.Errorf("told of the important post: Иван %d/%d, Пётр %d", told(ivan, "important"), told(ivan, "post_to_you"), told(petr, "important"))
	}
	it, err := srv.Feed.Item(ctx, anna, post)
	must(err)
	p := it.Post
	if p.Title != "План работ" || p.Body != "Новый текст" || !p.Important || p.Until.Before(time.Now().AddDate(0, 0, 6)) || !p.Edited {
		t.Errorf("the post after the edit: %q %q important %v until %v", p.Title, p.Body, p.Important, p.Until)
	}
	if len(p.Files) != 1 || p.Files[0].ID != f2 {
		t.Errorf("files after the edit: %+v", p.Files)
	}
	if p.Gratitude == nil || p.Gratitude.Badge != "star" || len(p.Gratitude.People) != 1 || p.Gratitude.People[0].ID != petr || told(petr, "gratitude") != 1 {
		t.Errorf("gratitude after the edit: %+v", p.Gratitude)
	}
	if p.Poll == nil || p.Poll.Question != "Когда лучше?" || len(p.Poll.Options) != 3 || !p.Poll.Multiple {
		t.Errorf("poll after the edit: %+v", p.Poll)
	}

	// the term as it was stays when the date is not touched; the post to the project: Иван (not a member) loses it
	form = do(anna, "GET", "/feed/"+post+"/edit", nil, 200)
	was := p.Until.Add(-time.Second).Format("2006-01-02")
	if !strings.Contains(form, `value=\"`+was+`\"`) {
		t.Errorf("the form has no term %s", was)
	}
	_, err = pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')`, project, petr)
	must(err)
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "title": {"План работ"}, "body": {"Новый текст"},
		"project": {project}, "important": {"on"}, "important_for": {"date"}, "important_until": {was}, "important_was": {p.Until.Format(time.RFC3339Nano)},
		"gratitude": {"on"}, "badge": {"star"}, "thank": {petr}, "poll_edit": {"1"}, "poll_question": {"Когда лучше?"}, "poll_options": {"Утром\nДнём\nВечером"}}, 200)
	it, err = srv.Feed.Item(ctx, anna, post)
	must(err)
	if !it.Post.Until.Equal(p.Until) || it.Post.ProjectKey != "ED" {
		t.Errorf("after the move: until %v (was %v), project %q", it.Post.Until, p.Until, it.Post.ProjectKey)
	}
	if sees(ivan, post) || !sees(petr, post) || told(petr, "important") != 1 {
		t.Errorf("the project post: Иван %v, Пётр %v, told Пётр %d", sees(ivan, post), sees(petr, post), told(petr, "important"))
	}

	// a vote fixes the poll: the form says so, a changed poll is refused; back to all, ordinary, no gratitude
	must(srv.Feed.Vote(ctx, petr, post, []string{it.Post.Poll.Options[0].ID}))
	if form = do(anna, "GET", "/feed/"+post+"/edit", nil, 200); strings.Contains(form, `name=\"poll_edit\"`) {
		t.Error("a voted poll is editable")
	}
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "body": {"x"}, "poll_edit": {"1"}, "poll_question": {"Другой"}, "poll_options": {"А\nБ"}}, 422)
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "title": {"Итог"}, "body": {"Для всех"}}, 200)
	it, err = srv.Feed.Item(ctx, anna, post)
	must(err)
	if it.Post.Important || it.Post.Gratitude != nil || it.Post.Poll == nil || it.Post.ProjectKey != "" || !sees(ivan, post) {
		t.Errorf("back to everyone: important %v, gratitude %+v, poll %v, project %q", it.Post.Important, it.Post.Gratitude, it.Post.Poll != nil, it.Post.ProjectKey)
	}
	// «Кому» chosen people without anybody — refused, nothing changes
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"full": {"1"}, "body": {"Пусто"}, "project": {"people"}}, 422)
	if it, _ = srv.Feed.Item(ctx, anna, post); it.Post.Body != "Для всех" {
		t.Errorf("a refused edit changed the text: %q", it.Post.Body)
	}
	// the plain edit (title and text only, as REST) keeps everything else
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"body": {"Только текст"}}, 200)
	if it, _ = srv.Feed.Item(ctx, anna, post); it.Post.Poll == nil || len(it.Post.Files) != 1 {
		t.Errorf("the plain edit lost the poll or the file")
	}
}
