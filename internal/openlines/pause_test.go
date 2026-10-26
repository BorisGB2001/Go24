package openlines

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestPausePin: an operator on a pause is passed by the queue and its notifications; a pinned dialogue is not passed on
// when its operator does not answer in time and comes back to them; only one's own or an unassigned dialogue is pinned.
func TestPausePin(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	boss, a, b := newUser("boss"), newUser("a"), newUser("b")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	strict, err := s.SaveWidget(ctx, boss, Widget{Name: "Строго", Active: true, Operators: []string{a, b}, QueueMode: "strict", AnswerWait: 3})
	must(err)
	all, err := s.SaveWidget(ctx, boss, Widget{Name: "Всем", Active: true, Operators: []string{a, b}})
	must(err)
	visit := func(wid string) (string, string) {
		t.Helper()
		id, token, err := s.Visit(ctx, wid, "", "")
		must(err)
		must(s.VisitorSay(ctx, wid, token, "?"))
		return id, token
	}
	operator := func(id string) string {
		t.Helper()
		x, err := s.Session(ctx, id)
		must(err)
		return x.OperatorID
	}
	notified := func(user, id string) bool {
		var ok bool
		must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications WHERE user_id = $1 AND url = '/openlines?s=' || $2)`, user, id).Scan(&ok))
		return ok
	}

	// a on a pause: the strict queue gives the dialogue to b, the line «to all» tells only b
	must(s.Pause(ctx, a, true))
	must(s.Pause(ctx, a, true))
	if on, _ := s.Paused(ctx, a); !on {
		t.Fatal("a is not on a pause")
	}
	d1, _ := visit(strict)
	if operator(d1) != b {
		t.Fatalf("d1 went to %s", operator(d1))
	}
	d2, _ := visit(all)
	if notified(a, d2) || !notified(b, d2) {
		t.Fatal("the paused operator is told of a new dialogue")
	}
	must(s.Pause(ctx, a, false))
	if on, _ := s.Paused(ctx, a); on {
		t.Fatal("a is still on a pause")
	}

	// pinned: b does not answer in time, the dialogue stays; unpinned — it goes to a
	var ve *i18n.Error
	if err := s.Pin(ctx, a, d1, true); !errors.As(err, &ve) || ve.Key != "err.ol_pin" {
		t.Fatalf("a pins b's dialogue: %v", err)
	}
	must(s.Pin(ctx, b, d1, true))
	if err := s.Pin(ctx, b, d1, true); !errors.As(err, &ve) {
		t.Fatalf("pinned twice: %v", err)
	}
	if x, _ := s.Session(ctx, d1); !x.Pinned {
		t.Fatal("not pinned")
	}
	later := time.Now().Add(10 * time.Minute)
	_, err = s.Due(ctx, later)
	must(err)
	if operator(d1) != b {
		t.Fatalf("the pinned dialogue went to %s", operator(d1))
	}
	must(s.Pin(ctx, b, d1, false))
	_, err = s.Due(ctx, later)
	must(err)
	if operator(d1) != a {
		t.Fatalf("the unpinned dialogue is %s's", operator(d1))
	}

	// pinned and closed, the client comes back: to the same operator, though the queue would give it another
	d3, t3 := visit(strict)
	who := operator(d3)
	must(s.Pin(ctx, who, d3, true))
	must(s.SetStatus(ctx, who, d3, false))
	must(s.Pause(ctx, who, true))
	must(s.VisitorSay(ctx, strict, t3, "снова я"))
	if operator(d3) != who {
		t.Fatalf("the pinned dialogue came back to %s, not %s", operator(d3), who)
	}

	// an unassigned dialogue is taken by the one who pins it
	d4, _ := visit(all)
	must(s.Pin(ctx, a, d4, true))
	if operator(d4) != a {
		t.Fatal("pinning did not take the dialogue")
	}
	msgs, err := s.Messages(ctx, d4, 0)
	must(err)
	if last := msgs[len(msgs)-1]; last.Kind != "note" || last.Body != "ol.note_pinned" {
		t.Fatalf("the note %+v", last)
	}
}
