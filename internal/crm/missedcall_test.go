package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestMissedCall: a missed call plans «Перезвонить» for the card's responsible (not who was called), a second one
// while it is open adds nothing, talking with the client closes it and the next missed call plans a new one.
func TestMissedCall(t *testing.T) {
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
	var boss, op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'op', 'op@t') RETURNING id`).Scan(&op))
	s := NewStore(pool)
	contact, err := s.SaveContact(ctx, boss, Contact{Name: "Клиент", Phone: "+79990000001", Responsible: Ref{ID: boss}})
	must(err)

	at := time.Now().Add(-time.Minute)
	id, err := s.MissedCall(ctx, "contact", contact, "+79990000001", op, "Перезвонить: +79990000001", at)
	must(err)
	if id == "" {
		t.Fatal("no deed")
	}
	a, err := s.Activity(ctx, id)
	must(err)
	if a.Kind != "call" || a.Responsible.ID != boss || a.DoneAt != nil {
		t.Fatalf("deed %+v", a)
	}
	if again, err := s.MissedCall(ctx, "contact", contact, "+79990000001", op, "Перезвонить", at); err != nil || again != "" {
		t.Fatalf("second missed call: %q %v", again, err)
	}
	if n, err := s.CalledBack(ctx, "contact", contact, op); err != nil || n != 1 {
		t.Fatalf("called back: %d %v", n, err)
	}
	if a, _ = s.Activity(ctx, id); a.DoneAt == nil {
		t.Fatal("deed still open")
	}
	if n, _ := s.CalledBack(ctx, "contact", contact, op); n != 0 {
		t.Fatalf("closed again: %d", n)
	}
	// an ordinary call deed is not closed by a conversation
	plain, err := s.AddActivity(ctx, boss, Activity{Entity: "contact", EntityID: contact, Kind: "call", Subject: "Позвонить", Deadline: at})
	must(err)
	next, err := s.MissedCall(ctx, "contact", contact, "+79990000001", op, "Перезвонить", at)
	must(err)
	if next == "" || next == id {
		t.Fatalf("next missed call: %q", next)
	}
	n, _ := s.CalledBack(ctx, "contact", contact, op)
	if p, _ := s.Activity(ctx, plain); n != 1 || p.DoneAt != nil {
		t.Fatalf("plain deed closed: %d %v", n, p.DoneAt)
	}
}
