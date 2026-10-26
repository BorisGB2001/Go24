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

// TestCollabChats: group chats of a collab, as «Чаты в проекте» — made in it by any of its people (those
// brought in join the collab), a group chat of one's own added by its owner (its people join the collab), people added
// later join the collab too; who leaves the collab leaves its chats; a chat taken out of it, or of a collab deleted,
// loses its guests.
func TestCollabChats(t *testing.T) {
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
	newUser := func(username string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username, guest) VALUES ('sub-' || $1, $1, $1 || '@x', $1, $2) RETURNING id`,
			username, guest).Scan(&id))
		return id
	}
	owner, client := newUser("owner", false), newUser("client", true)
	colleague, outsider, late := newUser("colleague", false), newUser("outsider", false), newUser("late", false)

	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IDP: "oidc-authentik", Company: "ООО Ромашка", GuestMark: "гость"})
	key, err := ts.CreateCollab(ctx, owner, "Клиент", "", []string{client})
	must(err)
	c, _ := ts.CollabByKey(ctx, owner, key)
	must(s.Reconcile(ctx))
	octx := tasks.WithViewer(ctx, owner, false)
	member := func(id string) bool {
		var yes bool
		must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members WHERE project_id = $1 AND user_id = $2)`, c.ID, id).Scan(&yes))
		return yes
	}
	logged := func(action string) string {
		var name string
		_ = pool.QueryRow(ctx, `SELECT detail FROM collab_log WHERE project_id = $1 AND action = $2 ORDER BY id DESC LIMIT 1`, c.ID, action).Scan(&name)
		return name
	}

	// made in the collab: the colleague brought in joins it; the chat is the collab's on the messenger's list
	if _, err := s.CreateCollabChat(octx, owner, c.ID, " ", []string{client}); err == nil {
		t.Fatal("a chat without a name")
	}
	if _, err := s.CreateCollabChat(tasks.WithViewer(ctx, outsider, false), outsider, c.ID, "Чужой", []string{owner}); err == nil {
		t.Fatal("someone outside the collab made a chat in it")
	}
	chat, err := s.CreateCollabChat(octx, owner, c.ID, "Дизайн", []string{client, colleague})
	must(err)
	if m := fake.rooms[chat]; !m["@owner:t.lan"] || !m["@client:t.lan"] || !m["@colleague:t.lan"] || fake.names[chat] != "Дизайн" {
		t.Fatalf("the collab chat's people: %v", m)
	}
	if !member(colleague) || logged("chat_created") != "Дизайн" || s.CollabOf(ctx, chat) != c.ID {
		t.Fatal("the colleague did not join the collab, or the chat is not the collab's")
	}
	rs, err := s.Rooms(ctx, client)
	must(err)
	if i := indexRoom(rs, chat); i < 0 || rs[i].Kind != "group" || rs[i].CollabKey != key || rs[i].CollabName != "Клиент" {
		t.Fatalf("the guest's list: %+v", rs)
	}
	// a guest makes one too, with the collab's people only
	gctx := tasks.WithViewer(ctx, client, true)
	if _, err := s.CreateCollabChat(gctx, client, c.ID, "Гостевой", []string{outsider}); err == nil {
		t.Fatal("a guest brought someone from outside into the collab")
	}
	gchat, err := s.CreateCollabChat(gctx, client, c.ID, "Гостевой", []string{owner})
	must(err)
	if cs, err := s.CollabChats(ctx, owner, c.ID); err != nil || len(cs) != 2 {
		t.Fatalf("the collab's chats of the owner: %+v %v", cs, err)
	}
	if cs, _ := s.CollabChats(ctx, colleague, c.ID); len(cs) != 1 || cs[0].Room != chat {
		t.Fatalf("the colleague sees chats they are not in: %+v", cs)
	}
	if err := s.AddChatToCollab(gctx, client, c.ID, gchat); err == nil {
		t.Fatal("a chat added twice")
	}

	// an own group chat added: only by its owner; its people join the collab
	own, err := s.CreateGroup(ctx, owner, "Подготовка", []string{outsider}, false)
	must(err)
	if err := s.AddChatToCollab(tasks.WithViewer(ctx, colleague, false), colleague, c.ID, own); err == nil {
		t.Fatal("someone outside the chat added it")
	}
	must(s.AddChatToCollab(octx, owner, c.ID, own))
	if !member(outsider) || s.CollabOf(ctx, own) != c.ID || logged("chat_added") != "Подготовка" {
		t.Fatal("the chat's people did not join the collab")
	}
	// people added to a collab's chat join the collab
	must(s.AddToGroup(octx, owner, chat, []string{late}))
	if !member(late) || !fake.rooms[chat]["@late:t.lan"] {
		t.Fatal("the person added to the collab's chat is not in the collab")
	}

	// who leaves the collab leaves its chats
	_, err = pool.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, c.ID, colleague)
	must(err)
	must(s.Reconcile(ctx))
	if fake.rooms[chat]["@colleague:t.lan"] || !fake.rooms[chat]["@client:t.lan"] {
		t.Fatalf("the chat after the colleague left the collab: %v", fake.rooms[chat])
	}

	// taken out of the collab: by the chat's owner; its guests leave it
	if err := s.RemoveChatFromCollab(tasks.WithViewer(ctx, late, false), late, chat); err == nil {
		t.Fatal("a member who owns neither the chat nor the collab took it out")
	}
	must(s.RemoveChatFromCollab(octx, owner, chat))
	if fake.rooms[chat]["@client:t.lan"] || !fake.rooms[chat]["@late:t.lan"] || s.CollabOf(ctx, chat) != "" || logged("chat_removed") != "Дизайн" {
		t.Fatalf("the chat taken out: %v", fake.rooms[chat])
	}

	// the collab deleted: its chats lose their guests and are no more its
	_, err = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, c.ID)
	must(err)
	must(s.Reconcile(ctx))
	if fake.rooms[gchat]["@client:t.lan"] || !fake.rooms[gchat]["@owner:t.lan"] || s.CollabOf(ctx, gchat) != "" || s.CollabOf(ctx, own) != "" {
		t.Fatalf("the chats of the deleted collab: %v", fake.rooms[gchat])
	}
}
