package matrixsync

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/matrix"
	"go24/internal/tasks"
)

// TestChatNews: the first call only takes the position; then someone else's unread message comes with its sender,
// one's own does not.
func TestChatNews(t *testing.T) {
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
	newUser := func(username string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ('sub-' || $1, $1, $1) RETURNING id`, username).Scan(&id))
		return id
	}
	ann, bob := newUser("ann"), newUser("bob")
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	room, err := s.DirectChat(ctx, ann, bob)
	must(err)
	news, since, err := s.ChatNews(ctx, ann, "")
	must(err)
	if len(news) != 0 || since == "" {
		t.Fatalf("the first call: %v %q", news, since)
	}
	_, err = s.Send(ctx, bob, room, "привет", "")
	must(err)
	news, _, err = s.ChatNews(ctx, ann, since)
	must(err)
	if len(news) != 1 || news[0].Room != room || news[0].Sender != "bob" || news[0].Text != "привет" || news[0].Root != "" || news[0].Event == "" {
		t.Fatalf("the news: %+v", news)
	}
	_, err = s.Send(ctx, ann, room, "сама", "")
	must(err)
	if news, _, err = s.ChatNews(ctx, ann, since); err != nil || len(news) != 0 {
		t.Fatalf("her own message: %+v %v", news, err)
	}
}
