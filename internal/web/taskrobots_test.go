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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestTaskRobotsHTTP: the «Роботы» page of a kanban is the project owner's and managers'; members see neither the
// page nor its link, guests get nothing.
func TestTaskRobotsHTTP(t *testing.T) {
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
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, member, guest := newUser("Ольга", false), newUser("Мария", false), newUser("Гость", true)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{owner, member, guest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}

	_, err = ts.CreateProject(ctx, owner, "RB", "robots", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, owner, "RB")
	must(err)
	must(ts.SetMember(ctx, p, member, "member"))
	st, err := ts.Stages(ctx, p.ID)
	must(err)

	if code, body, _ := do(owner, "GET", "/p/RB/board", nil); code != 200 || !strings.Contains(body, `href="/p/RB/robots"`) {
		t.Fatalf("owner's kanban: %d, robots link %v", code, strings.Contains(body, `href="/p/RB/robots"`))
	}
	if _, body, _ := do(member, "GET", "/p/RB/board", nil); strings.Contains(body, `href="/p/RB/robots"`) {
		t.Fatal("a member is offered the robots")
	}
	code, _, loc := do(owner, "POST", "/p/RB/robots", url.Values{"op": {"add"}, "stage": {st[1].ID}, "kind": {"comment"},
		"text": {"В работе: {title}"}, "delay": {"0"}, "unit": {"min"}})
	if code != http.StatusSeeOther || loc != "/p/RB/robots" {
		t.Fatalf("add: %d %s", code, loc)
	}
	code, body, _ := do(owner, "GET", "/p/RB/robots", nil)
	if code != 200 || !strings.Contains(body, "В работе: {title}") || !strings.Contains(body, `data-robot`) {
		t.Fatalf("robots page: %d", code)
	}
	if code, _, _ := do(member, "GET", "/p/RB/robots", nil); code != http.StatusForbidden {
		t.Fatalf("member's robots page: %d", code)
	}
	if _, _, loc := do(member, "POST", "/p/RB/robots", url.Values{"op": {"add"}, "stage": {st[1].ID}, "kind": {"comment"}, "text": {"x"}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("member adds a robot: %s", loc)
	}
	if code, _, _ := do(guest, "GET", "/p/RB/robots", nil); code != http.StatusNotFound {
		t.Fatalf("guest: %d", code)
	}
	robots, err := ts.Robots(ctx, p.ID)
	must(err)
	if len(robots) != 1 {
		t.Fatalf("robots = %d", len(robots))
	}
	do(owner, "POST", "/p/RB/robots", url.Values{"op": {"off"}, "id": {robots[0].ID}})
	if robots, _ = ts.Robots(ctx, p.ID); robots[0].Active {
		t.Fatal("the robot is still on")
	}
	do(owner, "POST", "/p/RB/robots", url.Values{"op": {"delete"}, "id": {robots[0].ID}})
	if robots, _ = ts.Robots(ctx, p.ID); len(robots) != 0 {
		t.Fatal("the robot is still there")
	}
}
