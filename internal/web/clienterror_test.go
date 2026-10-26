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

// TestClientErrorHTTP: an answer a page's form got that go24 did not mean (forms.js reports it) is kept with who,
// where and what; without a session nothing is kept (GO24DEV-349).
func TestClientErrorHTTP(t *testing.T) {
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
	var boss string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'b@t') RETURNING id`).Scan(&boss); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	if err := srv.Sessions.Set(rec, auth.Session{UserID: boss}); err != nil {
		t.Fatal(err)
	}
	send := func(c *http.Cookie) int {
		form := url.Values{"page": {"/t/CL1-1"}, "url": {"/t/CL1-1/action"}, "status": {"502"}, "body": {strings.Repeat("ш", 900)}}
		req, _ := http.NewRequest("POST", hs.URL+"/client-error", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c != nil {
			req.AddCookie(c)
		}
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := send(rec.Result().Cookies()[0]); code != http.StatusNoContent {
		t.Fatalf("report: %d", code)
	}
	if code := send(nil); code != http.StatusFound {
		t.Fatalf("report without a session: %d", code)
	}
	var who, page, target string
	var status, n, body int
	if err := pool.QueryRow(ctx, `SELECT user_id::text, page, url, status, length(body), (SELECT count(*) FROM client_errors)::int FROM client_errors`).
		Scan(&who, &page, &target, &status, &body, &n); err != nil {
		t.Fatal(err)
	}
	if who != boss || page != "/t/CL1-1" || target != "/t/CL1-1/action" || status != 502 || body != 500 || n != 1 {
		t.Fatalf("kept: %s %s %s %d %d, %d rows", who, page, target, status, body, n)
	}
}
