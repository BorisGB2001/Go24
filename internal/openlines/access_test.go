package openlines

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestAccess: an operator sees the unassigned queue of their lines and their own dialogues, not other lines' or
// colleagues'; a department head — also their people's; the lines' heads and administrators — every dialogue.
func TestAccess(t *testing.T) {
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
	la, err := s.SaveWidget(ctx, boss, Widget{Name: "A", Active: true, Operators: []string{a, b}})
	must(err)
	lb, err := s.SaveWidget(ctx, boss, Widget{Name: "B", Active: true, Operators: []string{c}})
	must(err)
	visit := func(wid string) string {
		t.Helper()
		id, token, err := s.Visit(ctx, wid, "", "")
		must(err)
		must(s.VisitorSay(ctx, wid, token, "?"))
		return id
	}
	qa, qa2, qb := visit(la), visit(la), visit(lb)
	must(s.Reply(ctx, b, qa2, "!")) // b answers: theirs
	sees := func(x Who) []string {
		t.Helper()
		list, err := s.Sessions(ctx, Filter{Who: x})
		must(err)
		var ids []string
		for _, l := range list {
			ids = append(ids, l.ID)
		}
		slices.Sort(ids)
		return ids
	}
	want := func(ids ...string) []string { slices.Sort(ids); return ids }
	if got := sees(Who{UserID: a}); !slices.Equal(got, want(qa)) {
		t.Fatalf("a sees %v", got)
	}
	if got := sees(Who{UserID: b}); !slices.Equal(got, want(qa, qa2)) {
		t.Fatalf("b sees %v", got)
	}
	if got := sees(Who{UserID: c}); !slices.Equal(got, want(qb)) {
		t.Fatalf("c sees %v", got)
	}
	if got := sees(Who{UserID: c, Team: []string{b}}); !slices.Equal(got, want(qa2, qb)) {
		t.Fatalf("b's head sees %v", got)
	}
	if got := sees(Who{UserID: boss, All: true}); !slices.Equal(got, want(qa, qa2, qb)) {
		t.Fatalf("all: %v", got)
	}
	if got := sees(Who{}); len(got) != 0 {
		t.Fatalf("nobody sees %v", got)
	}
	if err := s.See(ctx, Who{UserID: a}, qa2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a sees b's dialogue: %v", err)
	}
	must(s.See(ctx, Who{UserID: a}, qa))
	// transferred to a: now a's, not b's
	must(s.Transfer(ctx, b, qa2, a))
	must(s.See(ctx, Who{UserID: a}, qa2))
	if err := s.See(ctx, Who{UserID: b}, qa2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("b still sees the transferred dialogue: %v", err)
	}
	if n, err := s.Counts(ctx, Who{UserID: c}); err != nil || n["all"] != 1 {
		t.Fatalf("c's counts %+v %v", n, err)
	}

	// the client's other dialogues: the same e-mail on another line, the same phone written otherwise; not a stranger's
	visitor := func(wid, name, phone, email string) string {
		t.Helper()
		id, token, err := s.Visit(ctx, wid, "", "")
		must(err)
		must(s.VisitorSay(ctx, wid, token, "Здравствуйте"))
		must(s.Contact(ctx, wid, token, name, phone, email, "", ""))
		return id
	}
	v1 := visitor(la, "Иван", "+7 (900) 123-45-67", "ivan@shop.ru")
	v2 := visitor(lb, "Иван", "", "IVAN@shop.ru")
	v3 := visitor(la, "Ваня", "8 900 1234567", "")
	visitor(la, "Пётр", "+7 900 765-43-21", "petr@shop.ru")
	past, open, err := s.ClientDialogues(ctx, Who{UserID: a}, v1)
	must(err)
	var got []string
	for _, p := range past {
		got = append(got, p.ID)
	}
	slices.Sort(got)
	if !slices.Equal(got, want(v2, v3)) || !open[v3] || open[v2] {
		t.Fatalf("v1's other dialogues %v, open %v", got, open)
	}
	if x, err := s.Session(ctx, v1); err != nil || x.Name != "Иван" {
		t.Fatalf("session %+v %v", x, err)
	}
	if _, err := s.Session(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no session: %v", err)
	}
	must(s.TaskMade(ctx, a, v1, "GO-7", "Перезвонить"))
	msgs, err := s.Messages(ctx, v1, 0)
	must(err)
	if last := msgs[len(msgs)-1]; last.Kind != "note" || last.Body != "ol.note_task" {
		t.Fatalf("the task's note %+v", last)
	}

	// passed to the line B: its queue (c) has it, a does not; the client writes on in their widget of A
	td, ttok, err := s.Visit(ctx, la, "", "")
	must(err)
	must(s.VisitorSay(ctx, la, ttok, "нужен склад"))
	must(s.Reply(ctx, a, td, "передам"))
	if err := s.TransferLine(ctx, a, td, la); err == nil {
		t.Fatal("passed to the line it is in")
	}
	must(s.TransferLine(ctx, a, td, lb))
	x, err := s.Session(ctx, td)
	must(err)
	if x.OperatorID != "" || x.QueueID != lb || x.Widget != "B" || x.WidgetID != la {
		t.Fatalf("passed to B: %+v", x)
	}
	must(s.See(ctx, Who{UserID: c}, td))
	if err := s.See(ctx, Who{UserID: a}, td); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a still sees the dialogue passed on: %v", err)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND url = '/openlines?s=' || $2`, c, td).Scan(&told))
	if told != 1 {
		t.Fatalf("c told %d times", told)
	}
	must(s.VisitorSay(ctx, la, ttok, "ау"))
	must(s.See(ctx, Who{UserID: c}, td))
	// closed, the client comes back: to their own line again
	must(s.SetStatus(ctx, c, td, false))
	must(s.VisitorSay(ctx, la, ttok, "снова я"))
	if x, _ := s.Session(ctx, td); x.QueueID != "" || x.Widget != "A" {
		t.Fatalf("back: %+v", x)
	}
	must(s.See(ctx, Who{UserID: a}, td))

	// the lines' heads
	must(s.SetHeads(ctx, boss, []string{a, c}))
	if ok, _ := s.IsHead(ctx, a); !ok {
		t.Fatal("a is a head")
	}
	must(s.SetHeads(ctx, boss, []string{c}))
	heads, err := s.Heads(ctx)
	must(err)
	if !slices.Equal(heads, []string{c}) {
		t.Fatalf("heads = %v", heads)
	}
	if ok, _ := s.IsHead(ctx, a); ok {
		t.Fatal("a is no head any more")
	}
}
