package web

import (
	"context"
	"encoding/json"
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

// TestFeedCommentsHTTP: the menu of a feed comment — «Ответить», «Ссылка», «Изменить», «Удалить».
func TestFeedCommentsHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	post, err := srv.Feed.CreatePost(ctx, anna, "", "", "Новость", false)
	must(err)
	old, err := srv.Feed.CreatePost(ctx, anna, "", "", "Старая новость", false)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), ivan: cookie(ivan)}
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
	html := func(raw string) string {
		var v map[string]string
		must(json.Unmarshal([]byte(raw), &v))
		return v["html"]
	}

	c := html(do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"body": {"Отлично"}}, 201))
	for _, want := range []string{"data-creply=\"Иван\"", "data-clink", "data-cedit", "data-cdelete", "data-cedit-form"} {
		if !strings.Contains(c, want) {
			t.Errorf("own comment has no %s: %s", want, c)
		}
	}
	var cid string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_comments`).Scan(&cid))

	// «Цитировать» (GO24DEV-692): a comment is formatted as a chat message, its notification is plain text
	q := html(do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"body": {"[quote]Анна:\nНовость[/quote]\n[b]Согласен[/b]"}}, 201))
	for _, want := range []string{"<blockquote>Анна:<br>Новость</blockquote>", "<b>Согласен</b>", `data-cquote="Иван"`, "data-cinput"} {
		if !strings.Contains(q, want) {
			t.Errorf("a quoting comment has no %s: %s", want, q)
		}
	}
	var note string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, anna).Scan(&note))
	if strings.Contains(note, "[quote]") || !strings.Contains(note, "Согласен") {
		t.Errorf("the notification of a comment: %q", note)
	}
	if p := do(anna, "GET", "/feed?post="+post, nil, 200); !strings.Contains(p, `data-quote="Анна"`) || !strings.Contains(p, `data-cmore=`) {
		t.Error("the post has no «Цитировать»")
	}
	// «Ещё» (GO24DEV-766): the date is the link of the post, «Ссылка на сообщение» and the marks are in the menu
	if p := do(anna, "GET", "/feed?post="+post, nil, 200); !strings.Contains(p, `class="fdate" href="/feed?post=`+post+`"`) ||
		!strings.Contains(p, "data-plink") || !strings.Contains(p, `class="tsettings dmore fmore"`) {
		t.Error("the post has no «Ещё» menu")
	}
	_, err = pool.Exec(ctx, `DELETE FROM feed_comments WHERE id <> $1`, cid)
	must(err)

	// another person replies and links, but neither edits nor deletes
	page := do(anna, "GET", "/feed", nil, 200)
	if !strings.Contains(page, `id="comment-`+cid+`"`) || !strings.Contains(page, "data-clink") {
		t.Fatal("the feed has no comment menu")
	}
	if strings.Contains(page, "data-cedit-form") || strings.Contains(page, "data-cdelete") {
		t.Error("someone else's comment is editable")
	}
	do(anna, "POST", "/feed/comments/"+cid+"/edit", url.Values{"body": {"чужое"}}, 403)
	do(anna, "POST", "/feed/comments/"+cid+"/delete", url.Values{}, 403)

	ed := html(do(ivan, "POST", "/feed/comments/"+cid+"/edit", url.Values{"body": {"Просто отлично"}}, 200))
	if !strings.Contains(ed, "Просто отлично") || !strings.Contains(ed, "изменено") {
		t.Errorf("edited comment: %s", ed)
	}
	do(ivan, "POST", "/feed/comments/"+cid+"/edit", url.Values{"body": {" "}}, 422)

	// the link of a comment opens its post alone
	if one := do(anna, "GET", "/feed?post="+post, nil, 200); !strings.Contains(one, `id="comment-`+cid+`"`) || strings.Contains(one, "Старая новость") {
		t.Error("the link of a comment does not show its post alone")
	}
	if one := do(anna, "GET", "/feed?post="+old, nil, 200); !strings.Contains(one, "Старая новость") {
		t.Error("the link of a post does not show it")
	}

	do(ivan, "POST", "/feed/comments/"+cid+"/delete", url.Values{}, 200)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM feed_comments`).Scan(&n))
	if n != 0 {
		t.Fatal("the comment is not deleted")
	}
	do(ivan, "POST", "/feed/comments/"+cid+"/delete", url.Values{}, 404)

	// «Поделиться»: a post to everyone has no button; a post of Анна to herself is shared with Иван, who then sees it
	if strings.Contains(do(anna, "GET", "/feed?post="+post, nil, 200), "data-share title") {
		t.Error("a post to everyone can be shared")
	}
	lim, err := srv.Feed.CreatePostTo(ctx, anna, "", feed.Audience{Users: []string{anna}}, "", "Себе", false)
	must(err)
	do(ivan, "POST", "/feed/"+lim+"/share", url.Values{"to": {ivan}}, 403)
	if !strings.Contains(do(anna, "GET", "/feed?post="+lim, nil, 200), "data-share-box") {
		t.Error("the feed has no share picker")
	}
	sh := html(do(anna, "POST", "/feed/"+lim+"/share", url.Values{"to": {ivan}}, 200))
	if !strings.Contains(sh, "Иван") || !strings.Contains(sh, "data-share") {
		t.Errorf("the shared post: %s", sh)
	}
	if !strings.Contains(do(ivan, "GET", "/feed?post="+lim, nil, 200), "Себе") {
		t.Error("Иван does not see the shared post")
	}

	// «Скрыть»: out of Иван's feed into «Скрытые»
	if on := do(ivan, "POST", "/feed/"+old+"/hide", url.Values{}, 200); !strings.Contains(on, `"on": true`) {
		t.Errorf("hide: %s", on)
	}
	if strings.Contains(do(ivan, "GET", "/feed", nil, 200), "Старая новость") {
		t.Error("a hidden post is in the feed")
	}
	if hid := do(ivan, "GET", "/feed?view=hidden", nil, 200); !strings.Contains(hid, "Старая новость") || !strings.Contains(hid, "data-hidden-view") {
		t.Error("«Скрытые» has no hidden post")
	}
	// «Просмотры»
	do(ivan, "POST", "/feed/views", url.Values{"id": {post, old}}, 200)
	if v := do(anna, "GET", "/feed/"+post+"/views", nil, 200); !strings.Contains(v, "Иван") {
		t.Errorf("viewers: %s", v)
	}
	if !strings.Contains(do(anna, "GET", "/feed?post="+post, nil, 200), `<span data-views-n>1</span>`) {
		t.Error("the post shows no view")
	}
	// «Важное до»: a past date is refused, a week is shown
	do(anna, "POST", "/feed", url.Values{"body": {"Срочно"}, "important": {"on"}, "important_for": {"date"},
		"important_until": {time.Now().AddDate(0, 0, -2).Format("2006-01-02")}}, 422)
	if imp := html(do(anna, "POST", "/feed", url.Values{"body": {"Срочно"}, "important": {"on"}, "important_for": {"week"}}, 201)); !strings.Contains(imp, "Важное сообщение") || !strings.Contains(imp, "· до ") {
		t.Errorf("important for a week: %s", imp)
	}
}
