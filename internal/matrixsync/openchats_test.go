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

// TestOpenChats: open group chats and channels — the owner opens one, any employee finds it, reads it
// and joins without an invitation; guests do not; a closed one is gone from the list.
func TestOpenChats(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username, guest) VALUES ('sub-' || $1, $1, $1, $2) RETURNING id`, username, guest).Scan(&id))
		return id
	}
	ann, bob, eve, gus := newUser("ann", false), newUser("bob", false), newUser("eve", false), newUser("gus", true)
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: tasks.NewStore(pool),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), IDP: "oidc-authentik", Company: "Co"})
	must(s.Reconcile(ctx))
	mx := func(id string) string {
		var m string
		must(pool.QueryRow(ctx, `SELECT mxid FROM matrix_users WHERE user_id = $1`, id).Scan(&m))
		return m
	}

	// an open channel of ann alone: it needs a name; bob sees it, not in it yet
	if _, err := s.CreateOpenGroup(ctx, ann, " ", nil, true, true); err == nil {
		t.Fatal("an open chat without a name")
	}
	ch, err := s.CreateOpenGroup(ctx, ann, "Новости компании", nil, true, true)
	must(err)
	if jr := fake.lastState(ch, "m.room.join_rules"); jr["join_rule"] != "restricted" {
		t.Fatalf("the join rule: %v", jr)
	}
	if g, err := s.GroupInfo(ctx, ann, ch); err != nil || !g.Open || !g.CanOpen || !g.Channel {
		t.Fatalf("ann's open channel: %+v %v", g, err)
	}
	_, err = s.Send(ctx, ann, ch, "Добро пожаловать", "")
	must(err)
	list, err := s.OpenChats(ctx, bob, "")
	must(err)
	if len(list) != 1 || list[0].ID != ch || list[0].Name != "Новости компании" || !list[0].Channel || list[0].Members != 1 || list[0].Joined {
		t.Fatalf("bob's open chats: %+v", list)
	}
	if l, _ := s.OpenChats(ctx, bob, "бухгалтерия"); len(l) != 0 {
		t.Fatalf("searched for another: %+v", l)
	}
	if l, _ := s.OpenChats(ctx, gus, ""); len(l) != 0 {
		t.Fatalf("a guest sees open chats: %+v", l)
	}
	// bob reads it before joining, then joins: «bob вступает в чат»; he reads, does not write (a channel)
	if _, _, err := s.Messages(ctx, bob, ch, ""); err == nil {
		t.Fatal("bob read the chat as its member")
	}
	msgs, _, err := s.OpenMessages(ctx, bob, ch, "")
	must(err)
	if len(msgs) == 0 || msgs[len(msgs)-1].Body != "Добро пожаловать" {
		t.Fatalf("the preview: %+v", msgs)
	}
	if _, _, err := s.OpenMessages(ctx, gus, ch, ""); err == nil {
		t.Fatal("a guest read an open chat")
	}
	if err := s.JoinOpen(ctx, gus, ch); err == nil {
		t.Fatal("a guest joined an open chat")
	}
	must(s.JoinOpen(ctx, bob, ch))
	if !fake.rooms[ch][mx(bob)] {
		t.Fatal("bob is not in the channel")
	}
	msgs, _, err = s.Messages(ctx, bob, ch, "")
	must(err)
	if last := msgs[len(msgs)-1]; last.Event != "joined" || last.Target != "bob" {
		t.Fatalf("the line of joining: %+v", last)
	}
	if g, _ := s.GroupInfo(ctx, bob, ch); g.Writer || g.CanOpen || !g.Open {
		t.Fatalf("the channel for bob: %+v", g)
	}
	if l, _ := s.OpenChats(ctx, bob, "новости"); len(l) != 1 || !l[0].Joined || l[0].Members != 2 {
		t.Fatalf("bob in it: %+v", l)
	}
	// only the owner opens and closes it; closed, it is gone and nobody joins
	if err := s.SetOpen(ctx, bob, ch, false); err == nil {
		t.Fatal("bob closed ann's channel")
	}
	must(s.SetOpen(ctx, ann, ch, false))
	if l, _ := s.OpenChats(ctx, eve, ""); len(l) != 0 {
		t.Fatalf("a closed chat listed: %+v", l)
	}
	if err := s.JoinOpen(ctx, eve, ch); err == nil || fake.rooms[ch][mx(eve)] {
		t.Fatal("eve joined a closed chat")
	}
	// a group chat made the usual way, opened later; going into a collab closes it
	grp, err := s.CreateGroup(ctx, ann, "Обеды", []string{bob}, false)
	must(err)
	must(s.SetOpen(ctx, ann, grp, true))
	if l, _ := s.OpenChats(ctx, eve, ""); len(l) != 1 || l[0].ID != grp || l[0].Channel {
		t.Fatalf("the open group chat: %+v", l)
	}
	must(s.JoinOpen(ctx, eve, grp))
	_, err = s.Send(ctx, eve, grp, "Я с вами", "")
	must(err)
	var pid string
	must(pool.QueryRow(ctx, `INSERT INTO projects (key, name, kind, owner_id) VALUES ('CO', 'Коллаб', 'collab', $1) RETURNING id`, ann).Scan(&pid))
	must(s.closeOpen(ctx, ann, "tok-"+mx(ann), grp))
	if l, _ := s.OpenChats(ctx, eve, ""); len(l) != 0 {
		t.Fatalf("the chat closed for a collab: %+v", l)
	}
	if jr := fake.lastState(grp, "m.room.join_rules"); jr["join_rule"] != "invite" {
		t.Fatalf("the closed join rule: %v", jr)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO collab_chats (room_id, project_id, added_by) VALUES ($1, $2, $3)`, grp, pid, ann); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOpen(ctx, ann, grp, true); err == nil {
		t.Fatal("a collab's chat opened")
	}
}
