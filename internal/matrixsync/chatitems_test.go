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

// TestChatItems: the tasks and meetings tied to a chat («Задачи», «Встречи» of its panel) — only one in the chat ties
// and reads them, one who left does not; the latest tied comes first, a tie is made once.
func TestChatItems(t *testing.T) {
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
	srv := httptest.NewServer(newFake())
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	room, err := s.CreateGroup(ctx, ann, "Запуск", []string{bob}, false)
	must(err)
	msg, err := s.Send(ctx, ann, room, "Сделать макет", "")
	must(err)

	const t1, t2, e1 = "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"
	must(s.TieItem(ctx, ann, room, "task", t1, msg.ID))
	must(s.TieItem(ctx, ann, room, "task", t1, msg.ID)) // twice: still one
	must(s.TieItem(ctx, bob, room, "task", t2, ""))
	must(s.TieItem(ctx, bob, room, "event", e1, ""))
	if err := s.TieItem(ctx, eve, room, "task", t1, ""); err == nil {
		t.Fatal("eve, not in the chat, tied a task to it")
	}
	must2 := func(list []ChatItem, err error) []ChatItem {
		t.Helper()
		must(err)
		return list
	}
	if l := must2(s.ChatItems(ctx, bob, room, "task")); len(l) != 2 || l[0].ItemID != t2 || l[1].ItemID != t1 || l[1].EventID != msg.ID || l[0].EventID != "" {
		t.Fatalf("the chat's tasks: %+v", l)
	}
	if l := must2(s.ChatItems(ctx, ann, room, "event")); len(l) != 1 || l[0].ItemID != e1 {
		t.Fatalf("the chat's meetings: %+v", l)
	}
	if _, err := s.ChatItems(ctx, eve, room, "task"); err == nil {
		t.Fatal("eve read the chat's tasks")
	}
	if name, project, err := s.ChatInfo(ctx, bob, room); err != nil || name != "Запуск" || project != "" {
		t.Fatalf("the chat's info: %q %q %v", name, project, err)
	}
	if _, _, err := s.ChatInfo(ctx, eve, room); err == nil {
		t.Fatal("eve got the chat's info")
	}
	must(s.LeaveGroup(ctx, bob, room))
	if _, err := s.ChatItems(ctx, bob, room, "task"); err == nil {
		t.Fatal("bob, who left, reads the chat's tasks")
	}
}
