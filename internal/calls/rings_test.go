package calls

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRings: a call rings the people of its chat; each answers once while it rings; the caller's end leaves who has
// not answered as missed; only the caller and the people rung see the ring; an old ring no longer rings; the events
// go out through go24_notify to the person's key.
func TestRings(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	ann, bob, eve, ghost := user("ann"), user("bob"), user("eve"), user("ghost")
	rs := &Rings{DB: pool}

	r, err := rs.Start(ctx, ann, "!g:x", true, []string{bob, eve})
	must(err)
	for _, who := range []string{ann, bob, eve} {
		if g, err := rs.Get(ctx, who, r.ID); err != nil || g.Room != "!g:x" || !g.Video || len(g.People) != 2 {
			t.Fatalf("the ring as %s sees it: %+v %v", who, g, err)
		}
	}
	if _, err := rs.Get(ctx, ghost, r.ID); err != ErrNoRing {
		t.Fatal("someone not rung sees the ring")
	}
	if list, _ := rs.Ringing(ctx, bob); len(list) != 1 || list[0].ID != r.ID || list[0].CallerID != ann {
		t.Fatalf("bob's ringing: %+v", list)
	}
	if ok, err := rs.Answer(ctx, bob, r.ID, "declined"); err != nil || !ok {
		t.Fatalf("bob declines: %v %v", ok, err)
	}
	if ok, _ := rs.Answer(ctx, bob, r.ID, "accepted"); ok {
		t.Fatal("bob answered twice")
	}
	if list, _ := rs.Ringing(ctx, bob); len(list) != 0 {
		t.Fatal("still ringing for bob")
	}
	missed, err := rs.End(ctx, r.ID)
	must(err)
	if len(missed) != 1 || missed[0] != eve {
		t.Fatalf("missed: %v", missed)
	}
	if again, _ := rs.End(ctx, r.ID); len(again) != 0 {
		t.Fatal("ended twice")
	}
	if ok, _ := rs.Answer(ctx, eve, r.ID, "accepted"); ok {
		t.Fatal("eve answered after the end")
	}
	// a ring nobody ended: past its time it does not ring and cannot be answered
	old, err := rs.Start(ctx, ann, "!d:x", false, []string{bob})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE call_rings SET created_at = now() - interval '1 minute' WHERE id::text = $1`, old.ID)
	must(err)
	if list, _ := rs.Ringing(ctx, bob); len(list) != 0 {
		t.Fatalf("an old ring rings: %+v", list)
	}
	if ok, _ := rs.Answer(ctx, bob, old.ID, "accepted"); ok {
		t.Fatal("an old ring answered")
	}
	// the event reaches the person's key
	conn, err := pool.Acquire(ctx)
	must(err)
	defer conn.Release()
	_, err = conn.Exec(ctx, "LISTEN go24_notify")
	must(err)
	must(rs.Notify(ctx, bob, Event{Ring: r.ID, Kind: "stop"}))
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n, err := conn.Conn().WaitForNotification(wctx)
	must(err)
	key, value, _ := strings.Cut(n.Payload, ":")
	var ev Event
	if key != "c"+bob || json.Unmarshal([]byte(value), &ev) != nil || ev.Ring != r.ID || ev.Kind != "stop" {
		t.Fatalf("the notification: %q", n.Payload)
	}
}

// TestSessions: the first person starts a chat's call (its line is to be posted), the others join it once each;
// it ends once; then a new call of the same room starts anew.
func TestSessions(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	ann, bob := user("ann"), user("bob")
	ss := &Sessions{DB: pool}
	s1, isNew, err := ss.Join(ctx, "im-1", "!g:x", ann, true)
	must(err)
	if !isNew || s1.StartedBy != ann || !s1.Video || len(s1.People) != 1 {
		t.Fatalf("the start: %+v %v", s1, isNew)
	}
	must(ss.SetLine(ctx, s1.ID, "$line"))
	for _, who := range []string{bob, bob, ann} {
		s, isNew, err := ss.Join(ctx, "im-1", "!g:x", who, false)
		must(err)
		if isNew || s.ID != s1.ID || s.Event != "$line" {
			t.Fatalf("a join: %+v %v", s, isNew)
		}
	}
	going, err := ss.Going(ctx)
	must(err)
	if len(going) != 1 || strings.Join(going[0].People, ",") != ann+","+bob {
		t.Fatalf("going: %+v", going)
	}
	done, ok, err := ss.Finish(ctx, s1.ID)
	must(err)
	if !ok || done.Ended.Before(done.Started) || done.Event != "$line" {
		t.Fatalf("the end: %+v %v", done, ok)
	}
	if _, ok, _ := ss.Finish(ctx, s1.ID); ok {
		t.Fatal("ended twice")
	}
	if s2, isNew, err := ss.Join(ctx, "im-1", "!g:x", bob, false); err != nil || !isNew || s2.ID == s1.ID {
		t.Fatalf("the next call: %+v %v %v", s2, isNew, err)
	}
}
