package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestHashtags: the #tags of a text — not the "#" of a link, of escaped HTML or of a number.
func TestHashtags(t *testing.T) {
	got := feed.Hashtags("#План на #неделю: см. /feed#post-1, задача #42, (#Отпуск) и снова #план")
	if want := []string{"план", "неделю", "отпуск"}; !slices.Equal(got, want) {
		t.Errorf("Hashtags = %q, want %q", got, want)
	}
	if got := feed.ParseTags("#Итоги, отчёт ; ,  "); !slices.Equal(got, []string{"итоги", "отчёт"}) {
		t.Errorf("ParseTags with commas = %q", got)
	}
	if got := feed.ParseTags("итоги #отчёт итоги"); !slices.Equal(got, []string{"итоги", "отчёт"}) {
		t.Errorf("ParseTags with spaces = %q", got)
	}
	h := markHashtags("Про #план &#39;#нет&#39; a#нет")
	if h != `Про <a class="ftag" href="/feed?tag=%D0%BF%D0%BB%D0%B0%D0%BD">#план</a> &#39;#нет&#39; a#нет` {
		t.Errorf("markHashtags = %s", h)
	}
}

// TestFeedTags: tags of posts — typed in «Теги» and the #tags of the text, shown under the post as
// links to the feed of the tag; the edit changes the typed ones (without the field they stay); REST tags and ?tag=;
// «Создать задачу» of a comment fills the new task with the comment.
func TestFeedTags(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	tags := func(post string) []string {
		t.Helper()
		var out []string
		rows, err := pool.Query(ctx, `SELECT tag FROM feed_tags WHERE post_id = $1 ORDER BY tag`, post)
		must(err)
		for rows.Next() {
			var tag string
			must(rows.Scan(&tag))
			out = append(out, tag)
		}
		return out
	}

	// typed tags and the #tags of the text
	do(anna, "POST", "/feed", url.Values{"title": {"Итоги #Квартала"}, "body": {"Готовим #отчёт к пятнице"}, "tags": {"Финансы, #планы"}}, 201)
	var post string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_posts`).Scan(&post))
	if got := tags(post); !slices.Equal(got, []string{"квартала", "отчёт", "планы", "финансы"}) {
		t.Fatalf("tags of the new post: %q", got)
	}
	other, err := srv.Feed.CreatePost(ctx, ivan, "", "", "Без тегов", false)
	must(err)
	must(srv.Feed.SetTags(ctx, other, nil))

	page := do(ivan, "GET", "/feed", nil, 200)
	for _, want := range []string{`<a class="ftag" href="/feed?tag=%d0%be%d1%82%d1%87%d1%91%d1%82">#отчёт</a>`, `class="ftags"`, `name="tags"`} {
		if !strings.Contains(strings.ToLower(page), strings.ToLower(want)) {
			t.Errorf("the feed has no %s", want)
		}
	}
	byTag := do(ivan, "GET", "/feed?tag="+url.QueryEscape("#Финансы"), nil, 200)
	if !strings.Contains(byTag, `id="post-`+post+`"`) || strings.Contains(byTag, `id="post-`+other+`"`) {
		t.Error("the feed of a tag: wrong posts")
	}

	// the edit: the typed tags as the field says; without the field they stay, the #tags follow the text
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"title": {"Итоги"}, "body": {"Готовим #отчёт"}, "tags": {"финансы"}}, 200)
	if got := tags(post); !slices.Equal(got, []string{"отчёт", "финансы"}) {
		t.Fatalf("tags after the edit: %q", got)
	}
	do(anna, "POST", "/feed/"+post+"/edit", url.Values{"body": {"Готовим #сводку"}}, 200)
	if got := tags(post); !slices.Equal(got, []string{"сводку", "финансы"}) {
		t.Fatalf("tags after the edit without the field: %q", got)
	}
	do(ivan, "POST", "/feed/"+post+"/edit", url.Values{"body": {"x"}, "tags": {"чужое"}}, 403)

	// REST: tags of a new post, ?tag=, the tags alone changed (the text is not «изменено»)
	k, err := keys.Create(ctx, anna, "k", []string{"feed:read", "feed:write"}, time.Hour)
	must(err)
	call := func(method, path string, body any, want int, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	var p apiFeedPost
	call("POST", "/api/v1/feed/posts", map[string]any{"body": "Новый #регламент", "tags": []string{"Склад"}}, 201, &p)
	if !slices.Equal(p.Tags, []string{"регламент", "склад"}) {
		t.Fatalf("REST tags: %q", p.Tags)
	}
	var l struct {
		Posts []apiFeedPost `json:"posts"`
	}
	call("GET", "/api/v1/feed/posts?tag=склад", nil, 200, &l)
	if len(l.Posts) != 1 || l.Posts[0].ID != p.ID {
		t.Fatalf("REST ?tag=: %+v", l.Posts)
	}
	call("PATCH", "/api/v1/feed/posts/"+p.ID, map[string]any{"tags": []string{}}, 200, &p)
	if !slices.Equal(p.Tags, []string{"регламент"}) || p.Edited {
		t.Fatalf("REST tags changed alone: %q, edited %v", p.Tags, p.Edited)
	}

	// «Создать задачу» of a comment
	_, err = ts.CreateProject(ctx, anna, "WRK", "Работа", "")
	must(err)
	do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"body": {"Проверить цифры\nпо складу"}}, 201)
	var cid string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_comments`).Scan(&cid))
	if page := do(anna, "GET", "/feed?post="+post, nil, 200); !strings.Contains(page, `/tasks/new?feed_comment=`+cid) {
		t.Error("the comment has no «Создать задачу»")
	}
	form := do(anna, "GET", "/tasks/new?feed_comment="+cid, nil, 200)
	if !strings.Contains(form, "Проверить цифры") || !strings.Contains(form, "#comment-"+cid) {
		i := strings.Index(form, "<main")
		t.Errorf("the new task is not of the comment: %.3000s", form[max(i, 0):])
	}
}
