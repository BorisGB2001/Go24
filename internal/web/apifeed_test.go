package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestFeedAPI: «Лента» through REST API v1, as log.blogpost.* — a post published to all (important until a
// date, with a poll), to a project and to a person, seen by those it is for; comments with a drive's file, its bytes,
// reactions, the vote, «Прочитано», the marks, the edit and the delete — with the key's scope and the author's rights.
func TestFeedAPI(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	anna, ivan, olga := user("Анна"), user("Иван"), user("Ольга")
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	annaKey, ivanKey, olgaKey := key(anna, "feed:read", "feed:write"), key(ivan, "feed:read", "feed:write"), key(olga, "feed:read", "feed:write")
	readOnly, noFeed := key(ivan, "feed:read"), key(ivan, "tasks:read")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "SEC", "Секрет", "")
	must(err)
	_, err = pool.Exec(ctx, `UPDATE projects SET visibility = 'members'`)
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, path, token string, body any, want int, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
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
	type list struct {
		Posts []apiFeedPost `json:"posts"`
		Next  string        `json:"next"`
	}

	// the key's scope
	call("GET", "/api/v1/feed/posts", noFeed, nil, 403, nil)
	call("POST", "/api/v1/feed/posts", readOnly, map[string]any{"body": "x"}, 403, nil)

	// to all: important until tomorrow, with a poll
	var all apiFeedPost
	tomorrow := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"title": "Собрание", "body": "В пятницу", "important": true,
		"important_until": tomorrow, "poll": map[string]any{"question": "Когда?", "options": []string{"Утром", "Вечером"}}}, 201, &all)
	if all.Title != "Собрание" || !all.Important || all.Until == nil || all.Poll == nil || len(all.Poll.Options) != 2 || all.Author.ID != anna {
		t.Fatalf("post to all: %+v", all)
	}
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"body": "x", "important": true, "important_until": "2001-01-01"}, 422, nil)
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"body": " "}, 422, nil)
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"body": "x", "unknown": 1}, 422, nil)

	// to a project and to a person
	var proj, toIvan apiFeedPost
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"body": "Только проекту", "project": "sec"}, 201, &proj)
	if proj.Project != "SEC" {
		t.Fatalf("project post: %+v", proj)
	}
	call("POST", "/api/v1/feed/posts", annaKey, map[string]any{"body": "Ивану", "to": map[string]any{"users": []string{ivan}}}, 201, &toIvan)
	if len(toIvan.To) != 1 || toIvan.To[0].ID != ivan || toIvan.To[0].Kind != "user" {
		t.Fatalf("post to Ivan: %+v", toIvan)
	}
	call("POST", "/api/v1/feed/posts", ivanKey, map[string]any{"body": "x", "project": "SEC"}, 403, nil)
	call("GET", "/api/v1/feed/posts/"+proj.ID, ivanKey, nil, 403, nil)
	call("GET", "/api/v1/feed/posts/"+toIvan.ID, olgaKey, nil, 403, nil)
	var l list
	call("GET", "/api/v1/feed/posts", ivanKey, nil, 200, &l)
	if len(l.Posts) != 2 || l.Posts[0].ID != toIvan.ID || l.Posts[1].ID != all.ID {
		t.Fatalf("Ivan's feed: %+v", l.Posts)
	}
	call("GET", "/api/v1/feed/posts?view=important", ivanKey, nil, 200, &l)
	if len(l.Posts) != 1 || l.Posts[0].ID != all.ID {
		t.Fatalf("important: %+v", l.Posts)
	}
	call("GET", "/api/v1/feed/posts?project=SEC", annaKey, nil, 200, &l)
	if len(l.Posts) != 1 || l.Posts[0].ID != proj.ID {
		t.Fatalf("project feed: %+v", l.Posts)
	}
	call("GET", "/api/v1/feed/posts?q=пятниц&kind=poll", olgaKey, nil, 200, &l)
	if len(l.Posts) != 1 || l.Posts[0].ID != all.ID {
		t.Fatalf("search: %+v", l.Posts)
	}
	call("GET", "/api/v1/feed/posts?view=nope", olgaKey, nil, 422, nil)

	// the vote, «Прочитано», a reaction
	var got apiFeedPost
	call("POST", "/api/v1/feed/posts/"+all.ID+"/vote", ivanKey, map[string]any{"options": []string{all.Poll.Options[1].ID}}, 200, &got)
	if got.Poll.Voters != 1 || !got.Poll.Options[1].Mine {
		t.Fatalf("vote: %+v", got.Poll)
	}
	var voters []struct {
		Option string  `json:"option"`
		User   apiUser `json:"user"`
	}
	call("GET", "/api/v1/feed/posts/"+all.ID+"/poll/voters", annaKey, nil, 200, &voters)
	if len(voters) != 1 || voters[0].User.ID != ivan || voters[0].Option != all.Poll.Options[1].ID {
		t.Fatalf("poll voters: %+v", voters)
	}
	call("POST", "/api/v1/feed/posts/"+all.ID+"/poll/stop", ivanKey, nil, 403, nil) // not the author
	call("POST", "/api/v1/feed/posts/"+all.ID+"/poll/stop", annaKey, nil, 200, &got)
	if !got.Poll.Closed {
		t.Fatalf("stopped: %+v", got.Poll)
	}
	call("POST", "/api/v1/feed/posts/"+all.ID+"/vote", ivanKey, map[string]any{"options": []string{all.Poll.Options[0].ID}}, 422, nil)
	call("POST", "/api/v1/feed/posts/"+all.ID+"/poll/resume", annaKey, nil, 200, &got)
	if got.Poll.Closed || got.Poll.Voters != 1 {
		t.Fatalf("resumed: %+v", got.Poll)
	}
	var read map[string]int
	call("POST", "/api/v1/feed/posts/"+all.ID+"/read", ivanKey, nil, 200, &read)
	if read["readers"] != 1 {
		t.Fatalf("read: %v", read)
	}
	var rs apiReactions
	call("PUT", "/api/v1/feed/posts/"+all.ID+"/reaction", ivanKey, map[string]any{"reaction": "kiss"}, 200, &rs)
	if rs.Mine != "kiss" || rs.Total != 1 {
		t.Fatalf("reaction: %+v", rs)
	}
	call("PUT", "/api/v1/feed/posts/"+all.ID+"/reaction", ivanKey, map[string]any{"reaction": "kiss"}, 200, &rs) // the same: kept
	if rs.Mine != "kiss" || rs.Total != 1 {
		t.Fatalf("the same reaction again: %+v", rs)
	}
	call("PUT", "/api/v1/feed/posts/"+all.ID+"/reaction", ivanKey, map[string]any{"reaction": "nope"}, 422, nil)
	var who []map[string]string
	call("GET", "/api/v1/feed/posts/"+all.ID+"/reactions", annaKey, nil, 200, &who)
	if len(who) != 1 || who[0]["id"] != ivan || who[0]["reaction"] != "kiss" {
		t.Fatalf("reactors: %v", who)
	}
	call("DELETE", "/api/v1/feed/posts/"+all.ID+"/reaction", ivanKey, nil, 200, &rs)
	rs = apiReactions{}
	call("DELETE", "/api/v1/feed/posts/"+all.ID+"/reaction", ivanKey, nil, 200, &rs) // none: nothing to take back
	if rs.Mine != "" || rs.Total != 0 {
		t.Fatalf("reaction taken back: %+v", rs)
	}

	// a comment with a drive's file: its bytes to those who see the post
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/смета.pdf", Name: "смета.pdf", ContentType: "application/pdf",
		UploadedBy: ivan, Drive: "user", DriveOwner: ivan})
	must(err)
	call("POST", "/api/v1/feed/posts/"+all.ID+"/comments", annaKey, map[string]any{"files": []string{file}}, 403, nil)
	var c apiFeedComment
	call("POST", "/api/v1/feed/posts/"+all.ID+"/comments", ivanKey, map[string]any{"body": "Смета", "files": []string{file}}, 201, &c)
	if c.Body != "Смета" || len(c.Files) != 1 || c.Files[0].Name != "смета.pdf" || !c.CanEdit {
		t.Fatalf("comment: %+v", c)
	}
	call("GET", c.Files[0].URL, olgaKey, nil, 200, nil)
	call("GET", "/api/v1/feed/files/"+file, olgaKey, nil, 403, nil) // the drive's own file is not the feed's
	call("PATCH", "/api/v1/feed/comments/"+c.ID, annaKey, map[string]any{"body": "чужое"}, 403, nil)
	call("PATCH", "/api/v1/feed/comments/"+c.ID, ivanKey, map[string]any{"body": "Смета готова"}, 200, &c)
	if c.Body != "Смета готова" || !c.Edited {
		t.Fatalf("comment edited: %+v", c)
	}
	rs = apiReactions{}
	call("PUT", "/api/v1/feed/comments/"+c.ID+"/reaction", annaKey, nil, 200, &rs)
	if rs.Mine != "like" || rs.Total != 1 {
		t.Fatalf("comment reaction: %+v", rs)
	}
	call("GET", "/api/v1/feed/posts/"+all.ID, olgaKey, nil, 200, &got)
	if len(got.Comments) != 1 || got.Comments[0].Reactions.Top["like"] != 1 || got.Comments[0].CanEdit {
		t.Fatalf("the post's comments: %+v", got.Comments)
	}
	call("DELETE", "/api/v1/feed/comments/"+c.ID, olgaKey, nil, 403, nil)
	call("DELETE", "/api/v1/feed/comments/"+c.ID, ivanKey, nil, 204, nil)

	// the marks
	var on map[string]bool
	call("PUT", "/api/v1/feed/posts/"+toIvan.ID+"/fav", ivanKey, nil, 200, &on)
	call("PUT", "/api/v1/feed/posts/"+toIvan.ID+"/fav", ivanKey, nil, 200, &on)
	if !on["on"] {
		t.Fatalf("fav: %v", on)
	}
	call("GET", "/api/v1/feed/posts?view=fav", ivanKey, nil, 200, &l)
	if len(l.Posts) != 1 || l.Posts[0].ID != toIvan.ID || !l.Posts[0].Fav {
		t.Fatalf("favourites: %+v", l.Posts)
	}
	call("DELETE", "/api/v1/feed/posts/"+toIvan.ID+"/fav", ivanKey, nil, 200, &on)
	if on["on"] {
		t.Fatalf("fav off: %v", on)
	}
	call("PUT", "/api/v1/feed/posts/"+all.ID+"/hide", ivanKey, nil, 200, nil)
	call("GET", "/api/v1/feed/posts", ivanKey, nil, 200, &l)
	if len(l.Posts) != 1 {
		t.Fatalf("hidden left the feed: %+v", l.Posts)
	}

	// «Поделиться»: Olga gets the post to Ivan
	call("POST", "/api/v1/feed/posts/"+toIvan.ID+"/share", annaKey, map[string]any{"users": []string{olga}}, 200, &got)
	call("GET", "/api/v1/feed/posts/"+toIvan.ID, olgaKey, nil, 200, nil)

	// the edit and the delete: the author's
	var ed apiFeedPost
	call("PATCH", "/api/v1/feed/posts/"+all.ID, ivanKey, map[string]any{"body": "чужое"}, 403, nil)
	call("PATCH", "/api/v1/feed/posts/"+all.ID, annaKey, map[string]any{"body": "В субботу", "important_until": ""}, 200, &ed)
	if ed.Body != "В субботу" || ed.Title != "Собрание" || !ed.Important || ed.Until != nil || !ed.Edited {
		t.Fatalf("edited: %+v", ed)
	}
	call("DELETE", "/api/v1/feed/posts/"+all.ID, ivanKey, nil, 403, nil)
	call("DELETE", "/api/v1/feed/posts/"+all.ID, annaKey, nil, 204, nil)
	call("GET", "/api/v1/feed/posts/"+all.ID, annaKey, nil, 404, nil)
}
