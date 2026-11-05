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

// TestBridge: the bridge bot joins a group chat invited by one who may rename it; it reads the chat's new messages,
// not those that came from the outside group; a message of the group is the employee's when they are in the chat,
// the bot's with the name otherwise.
func TestBridge(t *testing.T) {
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
	ann, bob, eve := newUser("ann"), newUser("bob"), newUser("eve")
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	room, err := s.CreateGroup(ctx, ann, "Отдел", []string{bob}, false)
	must(err)
	if !s.CanBridge(ctx, ann, room) || s.CanBridge(ctx, eve, room) {
		t.Fatal("who may link the chat")
	}
	must(s.BridgeJoin(ctx, ann, room))
	must(s.BridgeJoin(ctx, ann, room)) // again: already in
	if u := fake.users["@bridge:t.lan"]; u == nil || u.DisplayName != "Telegram" {
		t.Fatalf("the bridge's account: %+v", u)
	}
	news, since, err := s.BridgeNews(ctx, "")
	must(err)
	if len(news) != 0 || since == "" {
		t.Fatalf("the first call: %v %q", news, since)
	}

	_, err = s.Send(ctx, bob, room, "привет", "")
	must(err)
	news, _, err = s.BridgeNews(ctx, since)
	must(err)
	if len(news) != 1 || news[0].Room != room || news[0].Sender != "bob" || news[0].Text != "привет" {
		t.Fatalf("the news: %+v", news)
	}
	must(s.BridgePost(ctx, ann, room, "", "Анна", "из Telegram")) // ann is in the chat: hers
	must(s.BridgePost(ctx, eve, room, "", "Ева", "я не в чате"))  // eve is not: the bridge's with her name
	must(s.BridgePost(ctx, "", room, "", "Пётр", "гость группы")) // no employee
	if news, _, err = s.BridgeNews(ctx, since); err != nil || len(news) != 0 {
		t.Fatalf("what came from the group goes back: %+v %v", news, err)
	}
	msgs, _, err := s.Messages(ctx, bob, room, "")
	must(err)
	got := map[string]string{}
	for _, m := range msgs {
		got[m.Body] = m.SenderName
	}
	if got["из Telegram"] != "ann" || got["Ева: я не в чате"] != "Telegram" || got["Пётр: гость группы"] != "Telegram" {
		t.Fatalf("the chat: %v", got)
	}
	must(s.BridgeLeave(ctx, room))
}
