package openlines

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestInvite: «Пригласить в диалог» — the invited colleague sees the dialogue among their own and answers the client
// without taking it, is told of the client's messages; leaves it, or its operator removes them; who may not.
func TestInvite(t *testing.T) {
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
	boss, a, b, c := newUser("boss"), newUser("a"), newUser("b"), newUser("c")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a}})
	must(err)
	sid, tok, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok, "Здравствуйте"))
	if err := s.Invite(ctx, a, sid, b); err == nil {
		t.Fatal("invited into a dialogue nobody has taken")
	}
	must(s.Reply(ctx, a, sid, "Слушаю"))

	sees := func(u string) bool { return s.See(ctx, Who{UserID: u}, sid) == nil }
	mine := func(u string) bool {
		t.Helper()
		list, err := s.Sessions(ctx, Filter{Tab: "mine", UserID: u, Who: Who{UserID: u}})
		must(err)
		return len(list) == 1 && list[0].ID == sid
	}
	unread := func(u string) (n int) {
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id::text = $1 AND kind = 'openline' AND read_at IS NULL`, u).Scan(&n))
		return n
	}
	if sees(b) || mine(b) {
		t.Fatal("b sees the dialogue before the invitation")
	}
	for _, to := range []string{a, "", boss + "x"} { // the operator, nobody
		if err := s.Invite(ctx, a, sid, to); err == nil {
			t.Fatalf("invited %q", to)
		}
	}
	must(s.Invite(ctx, a, sid, b))
	if err := s.Invite(ctx, a, sid, b); err == nil {
		t.Fatal("invited twice")
	}
	if !sees(b) || !mine(b) || unread(b) != 1 {
		t.Fatalf("invited b: sees %v, mine %v, notifications %d", sees(b), mine(b), unread(b))
	}
	if c, _ := s.Counts(ctx, Who{UserID: b}); c["mine"] != 1 {
		t.Fatalf("b's counter: %v", c)
	}
	x, _, err := s.Open(ctx, b, sid)
	must(err)
	if x.OperatorID != a || len(x.InvitedIDs) != 1 || x.InvitedIDs[0] != b || x.InvitedNames[0] != "b" {
		t.Fatalf("the dialogue: operator %s, invited %v %v", x.OperatorID, x.InvitedIDs, x.InvitedNames)
	}
	must(pool.QueryRow(ctx, `UPDATE notifications SET read_at = now() WHERE user_id::text = $1 RETURNING 1`, b).Scan(new(int)))
	// the client writes: the invited one is told too; answers — the dialogue stays a's
	must(s.VisitorSay(ctx, wid, tok, "Есть вопрос"))
	if unread(b) != 1 {
		t.Fatal("the invited one is not told of the client's message")
	}
	must(s.Reply(ctx, b, sid, "Помогу я"))
	x, msgs, err := s.Open(ctx, a, sid)
	must(err)
	if x.OperatorID != a || msgs[len(msgs)-1].AuthorID != b {
		t.Fatalf("after b's answer: operator %s", x.OperatorID)
	}
	notes := func() string {
		var all []string
		for _, m := range msgs {
			if m.Kind == "note" {
				all = append(all, m.Body)
			}
		}
		return strings.Join(all, " ")
	}
	if !strings.Contains(notes(), "ol.note_invited") {
		t.Fatalf("no service line of the invitation: %s", notes())
	}

	// c (not the operator, not a head) may not remove b; b leaves; a removes c
	if err := s.Uninvite(ctx, Who{UserID: c}, sid, b); err == nil {
		t.Fatal("a stranger removed the invited one")
	}
	must(s.Uninvite(ctx, Who{UserID: b}, sid, b))
	if sees(b) || mine(b) {
		t.Fatal("b still sees the dialogue after leaving it")
	}
	must(s.Invite(ctx, a, sid, c))
	must(s.Uninvite(ctx, Who{UserID: a}, sid, c))
	if sees(c) {
		t.Fatal("c removed but sees the dialogue")
	}
	_, msgs, _ = s.Open(ctx, a, sid)
	if n := notes(); !strings.Contains(n, "ol.note_left") || !strings.Contains(n, "ol.note_uninvited") {
		t.Fatalf("service lines: %s", n)
	}

	// transferred to the invited one: they are its operator, not invited any more
	must(s.Invite(ctx, a, sid, b))
	must(s.Transfer(ctx, a, sid, b))
	if x, _, _ = s.Open(ctx, b, sid); x.OperatorID != b || len(x.InvitedIDs) != 0 {
		t.Fatalf("transferred to the invited one: operator %s, invited %v", x.OperatorID, x.InvitedIDs)
	}
}
