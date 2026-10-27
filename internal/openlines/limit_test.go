package openlines

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestLimit: «Максимальное количество диалогов у оператора» — an operator at the limit gets no new dialogues, they wait
// in the queue for the first one to be free (till the answer or till closing); «Направлять обращение ответственному
// сотруднику» — a client CRM knows goes to their card's responsible.
func TestLimit(t *testing.T) {
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
	c := crm.NewStore(pool)
	s := NewStore(pool, c)
	s.Tr = func(key string, args ...any) string { return key }
	for _, bad := range []Widget{{MaxChats: -1}, {MaxChats: 5000}, {MaxChatsType: "never"}} {
		if err := cleanLine(&bad); err == nil {
			t.Fatalf("%+v", bad)
		}
	}
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a, b}, QueueMode: "evenly", MaxChats: 1})
	must(err)
	if w, _ := s.Widget(ctx, wid); w.MaxChats != 1 || w.MaxChatsType != "answer" || w.CRMForward {
		t.Fatalf("line = %+v", w)
	}
	op := func(id string) string {
		var o *string
		must(pool.QueryRow(ctx, `SELECT operator_id::text FROM ol_sessions WHERE id = $1`, id).Scan(&o))
		if o == nil {
			return ""
		}
		return *o
	}
	unread := func(user, id string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'openline' AND user_id = $1 AND url = '/openlines?s=' || $2 AND read_at IS NULL`,
			user, id).Scan(&n))
		return n
	}
	due := func(want int) {
		t.Helper()
		n, err := s.Due(ctx, time.Now())
		must(err)
		if n != want {
			t.Fatalf("Due changed %d dialogues, want %d", n, want)
		}
	}
	say := func(line, text string) (string, string) {
		t.Helper()
		id, token, err := s.Visit(ctx, line, "", "")
		must(err)
		must(s.VisitorSay(ctx, line, token, text))
		return id, token
	}

	s1, _ := say(wid, "1")
	s2, _ := say(wid, "2")
	s3, _ := say(wid, "3")
	if op(s1) != a || op(s2) != b || op(s3) != "" {
		t.Fatalf("one dialogue each: %q %q %q", op(s1), op(s2), op(s3))
	}
	if unread(a, s3) != 0 || unread(b, s3) != 0 {
		t.Fatal("operators at the limit are not told of a waiting dialogue")
	}
	due(0) // nobody is free
	must(s.Reply(ctx, a, s1, "Здравствуйте")) // till the answer: a is free again
	due(1)
	if op(s3) != a || unread(a, s3) != 1 {
		t.Fatalf("the waiting dialogue goes to the first one free: %q", op(s3))
	}
	due(0)

	// till closing: an answered dialogue still counts
	_, err = s.SaveWidget(ctx, boss, Widget{ID: wid, Name: "Линия", Active: true, Operators: []string{a, b}, QueueMode: "evenly", MaxChats: 1, MaxChatsType: "close"})
	must(err)
	must(s.Reply(ctx, a, s3, "Слушаю"))
	must(s.Reply(ctx, b, s2, "Слушаю"))
	s4, _ := say(wid, "4")
	if op(s4) != "" {
		t.Fatalf("till closing: %q", op(s4))
	}
	must(s.SetStatus(ctx, b, s2, false))
	must(s.SetStatus(ctx, a, s1, false))
	due(1)
	if op(s4) != a && op(s4) != b {
		t.Fatal("closed — free")
	}

	// the client's CRM responsible: on coming back with a known card, and when the form tells who they are
	fwd, err := s.SaveWidget(ctx, boss, Widget{Name: "CRM", Active: true, Operators: []string{a, b}, QueueMode: "strict", CRMForward: true})
	must(err)
	petr, err := c.SaveContact(ctx, boss, crm.Contact{Name: "Пётр", Phone: "+79005554433", Responsible: crm.Ref{ID: b}})
	must(err)
	f1, ft := say(fwd, "Добрый день")
	if op(f1) != a {
		t.Fatalf("strict: %q", op(f1))
	}
	must(s.Contact(ctx, fwd, ft, "Пётр", "+79005554433", "", "", ""))
	if op(f1) != b || unread(b, f1) != 1 || unread(a, f1) != 0 {
		t.Fatalf("to the responsible: %q", op(f1))
	}
	_, all, _ := s.Open(ctx, boss, f1)
	if last := all[len(all)-1]; last.Body != "ol.note_forwarded" {
		t.Fatalf("service line = %+v", last)
	}
	var contact string
	must(pool.QueryRow(ctx, `SELECT contact_id::text FROM ol_sessions WHERE id = $1`, f1).Scan(&contact))
	if contact != petr {
		t.Fatal("bound to the contact")
	}
	must(s.Reply(ctx, a, f1, "Отвечу я")) // a answers and takes it; closed and back — to the responsible again
	must(s.SetStatus(ctx, a, f1, false))
	must(s.VisitorSay(ctx, fwd, ft, "Снова я"))
	if op(f1) != b {
		t.Fatalf("back: to the responsible, not the last operator: %q", op(f1))
	}

	// without the option — the operator in turn stays
	_, err = c.SaveContact(ctx, boss, crm.Contact{Name: "Анна", Email: "anna@x.ru", Responsible: crm.Ref{ID: b}})
	must(err)
	plain, _ := s.SaveWidget(ctx, boss, Widget{Name: "Без CRM", Active: true, Operators: []string{a, b}, QueueMode: "strict"})
	p1, pt := say(plain, "?")
	first := op(p1)
	must(s.Contact(ctx, plain, pt, "Анна", "", "anna@x.ru", "", ""))
	if op(p1) != first {
		t.Fatal("no forwarding without the option")
	}
}
