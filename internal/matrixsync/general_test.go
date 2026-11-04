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

// TestGeneralChat: the general chat is every employee's (not a guest's), the messenger lists it as such; the new
// employee is welcomed once (those there when it was made are not), one who left comes back, the dismissed leaves;
// «только администраторы» lets only them write.
func TestGeneralChat(t *testing.T) {
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
	newUser := func(username string, guest, admin bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username, guest, admin) VALUES ('sub-' || $1, $1, $1 || '@x', $1, $2, $3) RETURNING id`,
			username, guest, admin).Scan(&id))
		return id
	}
	boss, anna := newUser("boss", false, true), newUser("anna", false, false)
	guest := newUser("guest", true, false)
	var late string // an employee of before, with no Matrix account yet (no username until the first sign-in)
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username) VALUES ('sub-late', 'late', 'late@x', '') RETURNING id`).Scan(&late))
	fake := newFake()
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ts := tasks.NewStore(pool)
	s := New(&Service{DB: pool, M: matrix.New(srv.URL, "t.lan", "bot"), Tasks: ts, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IDP: "oidc-authentik", Company: "ООО Ромашка", GeneralName: "Общий чат", GeneralHello: func(name string) string { return "Новый сотрудник: " + name }})
	_, err = ts.CreateCollab(ctx, boss, "Клиент", "", []string{guest})
	must(err)
	must(s.Reconcile(ctx))

	room, err := s.GeneralRoom(ctx)
	must(err)
	if room == "" || fake.names[room] != "Общий чат" {
		t.Fatalf("the general chat: %q", room)
	}
	if m := fake.rooms[room]; !m["@boss:t.lan"] || !m["@anna:t.lan"] || m["@guest:t.lan"] {
		t.Fatalf("the general chat's people: %v", m)
	}
	hellos := func() []string {
		var out []string
		for _, e := range fake.sent {
			if e.Room == room && e.Content["go24.general_hello"] != nil {
				out = append(out, e.Content["body"].(string))
			}
		}
		return out
	}
	if h := hellos(); len(h) != 0 {
		t.Fatalf("those there when it was made welcomed: %v", h)
	}
	rooms, err := s.Rooms(ctx, anna)
	must(err)
	found := false
	for _, r := range rooms {
		found = found || r.ID == room && r.Kind == "general"
	}
	if !found {
		t.Fatalf("the messenger's chats: %+v", rooms)
	}

	// an employee of before who comes in later is no new one
	_, err = pool.Exec(ctx, `UPDATE users SET username = 'late' WHERE id = $1`, late)
	must(err)
	must(s.Reconcile(ctx))
	if h := hellos(); len(h) != 0 || !fake.rooms[room]["@late:t.lan"] {
		t.Fatalf("an employee of before welcomed: %v %v", h, fake.rooms[room])
	}
	// a new employee comes in with a welcome, once; one who left comes back without it
	newUser("ivan", false, false)
	must(s.Reconcile(ctx))
	if h := hellos(); len(h) != 1 || h[0] != "Новый сотрудник: ivan" || !fake.rooms[room]["@ivan:t.lan"] {
		t.Fatalf("the new employee: %v %v", h, fake.rooms[room])
	}
	delete(fake.rooms[room], "@ivan:t.lan")
	must(s.Reconcile(ctx))
	if h := hellos(); len(h) != 1 || !fake.rooms[room]["@ivan:t.lan"] {
		t.Fatalf("one who left: %v %v", h, fake.rooms[room])
	}
	// no welcome when it is off
	must(s.SetGeneralSettings(ctx, boss, General{Who: "all", NoHello: true}))
	newUser("olga", false, false)
	must(s.Reconcile(ctx))
	if h := hellos(); len(h) != 1 || !fake.rooms[room]["@olga:t.lan"] {
		t.Fatalf("a welcome turned off: %v", h)
	}

	// only administrators write: messages need the writers' level, which the administrator has
	if s.GeneralClosed(ctx, anna) {
		t.Fatal("everyone writes by default")
	}
	must(s.SetGeneralSettings(ctx, boss, General{Who: "admins"}))
	must(s.Reconcile(ctx))
	var pl map[string]any
	for _, st := range fake.state {
		if st.Path == room+"/state/m.room.power_levels/" && st.Content != nil {
			pl = st.Content
		}
	}
	users, _ := pl["users"].(map[string]any)
	if pl["events_default"] != float64(writerLevel) || users["@boss:t.lan"] != float64(writerLevel) || users["@anna:t.lan"] != nil {
		t.Fatalf("administrators only: %v", pl)
	}
	if !s.GeneralClosed(ctx, anna) || s.GeneralClosed(ctx, boss) {
		t.Fatal("who may write in the general chat")
	}
	if err := s.SetGeneralSettings(ctx, boss, General{Who: "some"}); err == nil {
		t.Fatal("a rule that is not a choice saved")
	}

	// the dismissed leaves it
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, anna)
	must(err)
	must(s.Reconcile(ctx))
	if fake.rooms[room]["@anna:t.lan"] {
		t.Fatal("the dismissed stays in the general chat")
	}
}
