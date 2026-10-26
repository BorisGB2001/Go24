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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestFeedPollHTTP: a poll of a post — the results without voting, «Кто голосовал» by answer, the
// author stops the poll (no more votes) and resumes it, «Выгрузить результаты» for Excel; an anonymous poll shows
// the counts only.
func TestFeedPollHTTP(t *testing.T) {
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
	fs := feed.NewStore(pool, ts)
	srv := &Server{DB: pool, Tasks: ts, Feed: fs, Notify: notify.NewStore(pool),
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
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
		t.Helper()
		var out map[string]string
		must(json.Unmarshal([]byte(raw), &out))
		return out["html"]
	}

	post, err := fs.CreatePost(ctx, anna, "", "", "Обед", false)
	must(err)
	must(fs.AddPoll(ctx, anna, post, "Где обедаем?", []string{"Кафе", "Столовая"}, feed.PollKind{Multiple: true}))
	it, err := fs.Item(ctx, ivan, post)
	must(err)
	opts := it.Post.Poll.Options
	must(fs.Vote(ctx, anna, post, []string{opts[1].ID}))

	// before voting: the answers to choose and «Результаты» to peek at
	h := html(do(ivan, "POST", "/feed/"+post+"/vote", url.Values{}, 200))
	if !strings.Contains(h, `class="fpoll-peek"`) || !strings.Contains(h, `name="option"`) || strings.Contains(h, "poll/export") {
		t.Fatalf("a poll not voted in:\n%s", h)
	}
	h = html(do(ivan, "POST", "/feed/"+post+"/vote", url.Values{"option": {opts[0].ID, opts[1].ID}}, 200))
	if !strings.Contains(h, `data-poll-who="`+opts[0].ID+`"`) || strings.Contains(h, "data-poll-close") {
		t.Fatalf("a poll voted in (not the author):\n%s", h)
	}
	var voters []map[string]string
	must(json.Unmarshal([]byte(do(ivan, "GET", "/feed/"+post+"/poll/voters", nil, 200)), &voters))
	if len(voters) != 3 || voters[0]["option"] != opts[0].ID || voters[0]["id"] != ivan {
		t.Fatalf("voters = %v", voters)
	}

	// the author stops the poll: the results to everyone, no votes; then resumes it
	do(ivan, "POST", "/feed/"+post+"/poll/stop", nil, 403)
	h = html(do(anna, "POST", "/feed/"+post+"/poll/stop", nil, 200))
	if !strings.Contains(h, `data-poll-close="resume"`) || strings.Contains(h, `name="option"`) {
		t.Fatalf("a stopped poll:\n%s", h)
	}
	do(ivan, "POST", "/feed/"+post+"/vote", url.Values{"option": {opts[1].ID}}, 422)
	h = html(do(anna, "POST", "/feed/"+post+"/poll/resume", nil, 200))
	if !strings.Contains(h, `data-poll-close="stop"`) {
		t.Fatalf("a resumed poll:\n%s", h)
	}

	// «Выгрузить результаты»: the author only
	do(ivan, "GET", "/feed/"+post+"/poll/export", nil, 403)
	csv := do(anna, "GET", "/feed/"+post+"/poll/export", nil, 200)
	if !strings.Contains(csv, "Где обедаем?") || !strings.Contains(csv, "Кафе,1,50") || !strings.Contains(csv, ",,,Иван") {
		t.Fatalf("export:\n%s", csv)
	}

	// an anonymous poll: the counts only
	anon, err := fs.CreatePost(ctx, anna, "", "", "Анонимно", false)
	must(err)
	must(fs.AddPoll(ctx, anna, anon, "Довольны?", []string{"Да", "Нет"}, feed.PollKind{Anonymous: true}))
	it, err = fs.Item(ctx, ivan, anon)
	must(err)
	h = html(do(ivan, "POST", "/feed/"+anon+"/vote", url.Values{"option": {it.Post.Poll.Options[0].ID}}, 200))
	if strings.Contains(h, "data-poll-who") {
		t.Fatalf("who voted in an anonymous poll:\n%s", h)
	}
	do(ivan, "GET", "/feed/"+anon+"/poll/voters", nil, 403)
	if csv := do(anna, "GET", "/feed/"+anon+"/poll/export", nil, 200); strings.Contains(csv, "Иван") {
		t.Fatalf("export of an anonymous poll names the voters:\n%s", csv)
	}
}
