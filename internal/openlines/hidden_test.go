package openlines

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestHidden: a hidden message — the operators see it, the visitor does not, it does not take the dialogue, the
// dialogue's operator is told; «Перехватить диалог» — the dialogue is the interceptor's, the former operator is told.
func TestHidden(t *testing.T) {
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
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a, b}})
	must(err)
	sid, tok, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok, "Где мой заказ?"))
	notes := func(user, body string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND body = $2`, user, body).Scan(&n))
		return n
	}

	// a hidden message before anyone answered: the dialogue is still nobody's and waits
	must(s.Hide(ctx, a, sid, "Клиент постоянный", nil))
	x, msgs, err := s.Open(ctx, a, sid)
	must(err)
	if x.OperatorID != "" || !x.Waiting || x.Last != "Где мой заказ?" {
		t.Fatalf("hidden message took the dialogue: operator %q, waiting %v, last %q", x.OperatorID, x.Waiting, x.Last)
	}
	if m := msgs[len(msgs)-1]; m.Kind != "hidden" || m.AuthorID != a || !m.Internal() || m.Auto() {
		t.Fatalf("hidden message: %+v", m)
	}
	must(s.Reply(ctx, a, sid, "Сейчас посмотрю"))
	_, err = pool.Exec(ctx, `UPDATE notifications SET read_at = now() WHERE user_id = $1`, a)
	must(err)
	// the head's hint: the operator is told, the visitor sees nothing
	must(s.Hide(ctx, boss, sid, "Предложи скидку", nil))
	must(s.Hide(ctx, a, sid, "Хорошо", nil))
	if n := notes(a, "ol.notify_hidden"); n != 1 {
		t.Fatalf("operator's notifications of the hint: %d", n)
	}
	if n := notes(boss, "ol.notify_hidden"); n != 0 {
		t.Fatalf("the head was told of the operator's own hidden message: %d", n)
	}
	vis, err := s.VisitorMessages(ctx, wid, tok, 0)
	must(err)
	for _, m := range vis {
		if m.Kind == "hidden" || m.Body == "Предложи скидку" || m.Body == "Клиент постоянный" {
			t.Fatalf("the visitor sees a hidden message: %+v", m)
		}
	}
	if len(vis) != 2 {
		t.Fatalf("visitor's messages: %+v", vis)
	}
	if err := s.Hide(ctx, a, sid, " ", nil); err == nil {
		t.Fatal("an empty hidden message was taken")
	}

	// b intercepts a's dialogue
	must(s.Intercept(ctx, b, sid))
	x, msgs, err = s.Open(ctx, b, sid)
	must(err)
	if x.OperatorID != b {
		t.Fatalf("intercepted dialogue's operator: %q", x.OperatorID)
	}
	if m := msgs[len(msgs)-1]; m.Kind != "note" || m.Body != "ol.note_intercepted" {
		t.Fatalf("interception's service line: %+v", m)
	}
	if n := notes(a, "ol.notify_intercepted"); n != 1 {
		t.Fatalf("former operator's notifications of the interception: %d", n)
	}
	if err := s.Intercept(ctx, b, sid); err == nil {
		t.Fatal("own dialogue intercepted")
	}
	// an unassigned dialogue intercepted is taken
	sid2, tok2, _ := s.Visit(ctx, wid, "", "")
	must(s.VisitorSay(ctx, wid, tok2, "Здравствуйте"))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET operator_id = NULL WHERE id = $1`, sid2)
	must(err)
	must(s.Intercept(ctx, boss, sid2))
	x, msgs, err = s.Open(ctx, boss, sid2)
	must(err)
	if x.OperatorID != boss || msgs[len(msgs)-1].Body != "ol.note_taken" {
		t.Fatalf("unassigned dialogue intercepted: operator %q, %+v", x.OperatorID, msgs[len(msgs)-1])
	}
}
