package openlines

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
)

// TestOperator: who took the dialogue, transfer to a colleague (service line, notification), spam (the visitor's
// messages are not taken), the tabs and search of the list, quick answers (own and the company's).
func TestOperator(t *testing.T) {
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
	say := func(token, body string) { t.Helper(); must(s.VisitorSay(ctx, wid, token, body)) }
	s1, t1, _ := s.Visit(ctx, wid, "", "")
	say(t1, "Где мой заказ?")
	s2, t2, _ := s.Visit(ctx, wid, "", "")
	say(t2, "Купите рекламу")
	must(s.Contact(ctx, wid, t1, "Иван Петров", "+7 900 123", "ivan@shop.ru", "", "Сайт"))

	ids := func(f Filter) []string {
		t.Helper()
		list, err := s.Sessions(ctx, f)
		must(err)
		out := []string{}
		for _, x := range list {
			out = append(out, x.ID)
		}
		return out
	}
	same := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if l := ids(Filter{Tab: "noanswer"}); len(l) != 2 {
		t.Fatalf("both wait: %v", l)
	}

	// the first answer: «a took the dialogue»; it is a's, and answered
	must(s.Reply(ctx, a, s1, "Сейчас проверю"))
	_, msgs, _ := s.Open(ctx, boss, s1)
	if n := msgs[len(msgs)-2]; n.Kind != "note" || n.Body != "ol.note_taken" {
		t.Fatalf("taken: %+v", msgs)
	}
	must(s.Reply(ctx, a, s1, "Ещё минуту"))
	_, msgs, _ = s.Open(ctx, boss, s1)
	if msgs[len(msgs)-2].Kind == "note" {
		t.Fatal("taken only once")
	}
	if l := ids(Filter{Tab: "mine", UserID: a}); !same(l, s1) {
		t.Fatalf("mine: %v", l)
	}
	if l := ids(Filter{Tab: "noanswer"}); !same(l, s2) {
		t.Fatalf("without an answer: %v", l)
	}
	if c, _ := s.Counts(ctx, a); c["mine"] != 1 || c["noanswer"] != 1 || c["all"] != 2 {
		t.Fatalf("counts: %v", c)
	}

	// search: name, phone, e-mail, a word of the conversation (not of service lines)
	for q, want := range map[string]int{"петров": 1, "900 1": 1, "IVAN@": 1, "рекламу": 1, "проверю": 1, "ol.note": 0, "%": 0, "Нет такого": 0} {
		if l := ids(Filter{Q: q}); len(l) != want {
			t.Fatalf("search %q: %v", q, l)
		}
	}

	// transfer to b: a service line, b is notified, a's notification is read
	_, err = pool.Exec(ctx, `INSERT INTO notifications (user_id, kind, body, url) VALUES ($1, 'openline', 'x', '/openlines?s=' || $2)`, a, s1)
	must(err)
	must(s.Transfer(ctx, a, s1, b))
	var op string
	must(pool.QueryRow(ctx, `SELECT operator_id::text FROM ol_sessions WHERE id = $1`, s1).Scan(&op))
	_, msgs, _ = s.Open(ctx, boss, s1)
	if op != b || msgs[len(msgs)-1].Body != "ol.note_transferred" {
		t.Fatalf("transferred to %s: %+v", op, msgs[len(msgs)-1])
	}
	var na, nb int
	must(pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE user_id = $1), count(*) FILTER (WHERE user_id = $2 AND body = 'ol.notify_transferred')
		FROM notifications WHERE kind = 'openline' AND read_at IS NULL AND url = '/openlines?s=' || $3`, a, b, s1).Scan(&na, &nb))
	if na != 0 || nb != 1 {
		t.Fatalf("notifications: a %d, b %d", na, nb)
	}
	var ve *i18n.Error
	if err := s.Transfer(ctx, b, s1, b); !errors.As(err, &ve) {
		t.Fatalf("to the same operator: %v", err)
	}
	if err := s.Transfer(ctx, b, s1, "00000000-0000-0000-0000-000000000000"); !errors.As(err, &ve) {
		t.Fatalf("to nobody: %v", err)
	}
	if err := s.Transfer(ctx, b, "00000000-0000-0000-0000-000000000000", a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no dialogue: %v", err)
	}

	// spam: closed, the visitor's messages are not taken (silently), shown among the closed ones; reopened — taken again
	must(s.Spam(ctx, a, s2))
	say(t2, "Купите ещё")
	_, msgs, _ = s.Open(ctx, boss, s2)
	if last := msgs[len(msgs)-1]; last.Body != "ol.note_spam" {
		t.Fatalf("spam: %+v", last)
	}
	if l := ids(Filter{}); !same(l, s1) {
		t.Fatalf("spam out of the open ones: %v", l)
	}
	closed, _ := s.Sessions(ctx, Filter{Tab: "closed"})
	if len(closed) != 1 || !closed[0].Spam {
		t.Fatalf("closed: %+v", closed)
	}
	must(s.SetStatus(ctx, a, s2, true))
	say(t2, "Извините")
	if _, msgs, _ = s.Open(ctx, boss, s2); msgs[len(msgs)-1].Body != "Извините" {
		t.Fatal("not spam any more")
	}
	if err := s.Transfer(ctx, a, s1, a); err != nil {
		t.Fatal(err)
	}
	must(s.SetStatus(ctx, a, s1, false))
	if err := s.Transfer(ctx, a, s1, b); !errors.As(err, &ve) {
		t.Fatalf("a closed dialogue: %v", err)
	}

	// quick answers: own ones for oneself, the company's — by administrators, for everyone
	own, err := s.SaveQuick(ctx, a, false, Quick{Title: "Доставка", Body: "Доставка — 1–2 дня."})
	must(err)
	if _, err := s.SaveQuick(ctx, a, false, Quick{Title: "Общий", Body: "x", Common: true}); err == nil {
		t.Fatal("only administrators make common answers")
	}
	common, err := s.SaveQuick(ctx, boss, true, Quick{Title: "Акция", Body: "Скидка 10%", Common: true})
	must(err)
	if _, err := s.SaveQuick(ctx, a, false, Quick{Title: " ", Body: "x"}); !errors.As(err, &ve) {
		t.Fatal("a title is needed")
	}
	qa, _ := s.Quick(ctx, a)
	qb, _ := s.Quick(ctx, b)
	if len(qa) != 2 || qa[0].Title != "Акция" || !qa[0].Common || len(qb) != 1 {
		t.Fatalf("quick answers: a %+v, b %+v", qa, qb)
	}
	if _, err := s.SaveQuick(ctx, b, false, Quick{ID: own, Title: "Чужой", Body: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("another's answer")
	}
	if err := s.DeleteQuick(ctx, a, false, common); !errors.Is(err, ErrNotFound) {
		t.Fatal("the company's answer is the administrators'")
	}
	must(s.DeleteQuick(ctx, a, false, own))
	must(s.DeleteQuick(ctx, boss, true, common))
	if qa, _ = s.Quick(ctx, a); len(qa) != 0 {
		t.Fatalf("deleted: %+v", qa)
	}
}
