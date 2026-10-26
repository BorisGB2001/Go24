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

func TestInHours(t *testing.T) {
	mon := func(hm string) time.Time {
		x, _ := time.ParseInLocation("2006-01-02 15:04", "2026-10-26 "+hm, time.Local) // a Monday
		return x
	}
	w := Widget{HoursOn: true, HoursDays: []int32{1}, HoursFrom: "09:00", HoursTo: "18:00"}
	if !w.InHours(mon("09:00")) || !w.InHours(mon("17:59")) || w.InHours(mon("18:00")) || w.InHours(mon("08:59")) || w.InHours(mon("10:00").AddDate(0, 0, 1)) {
		t.Fatal("09:00–18:00 on Mondays")
	}
	night := Widget{HoursOn: true, HoursDays: []int32{1}, HoursFrom: "22:00", HoursTo: "06:00"}
	if !night.InHours(mon("23:00")) || !night.InHours(mon("05:00")) || night.InHours(mon("12:00")) {
		t.Fatal("overnight hours")
	}
	if !(Widget{}).InHours(mon("03:00")) {
		t.Fatal("without a schedule the line always works")
	}
	for _, bad := range []Widget{{HoursFrom: "9:00", HoursTo: "18:00"}, {HoursFrom: "10:00", HoursTo: "10:00"}, {HoursOn: true, HoursDays: []int32{}},
		{QueueMode: "random"}, {AnswerWait: 2000}, {HoursDays: []int32{7}}, {Autoclose: -1}, {KPIFirst: 2000}, {KPIFurther: -1}} {
		var ve *i18n.Error
		if err := cleanLine(&bad); !errors.As(err, &ve) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

// TestQueue: strictly in turn and evenly the dialogue goes to one operator, passes on without an answer in time;
// the line's automatic messages (greeting, «busy», out of hours, closing) and closing a dialogue the visitor left.
func TestQueue(t *testing.T) {
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
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Линия", Active: true, Operators: []string{a, b, c}, QueueMode: "strict",
		WelcomeText: "Здравствуйте!", NoanswerText: "Все заняты", NoanswerAfter: 5, CloseText: "Спасибо!", Autoclose: 1})
	must(err)
	w, _ := s.Widget(ctx, wid)
	if w.AnswerWait != 3 || w.QueueMode != "strict" || w.WelcomeText != "Здравствуйте!" {
		t.Fatalf("line = %+v", w)
	}
	visitor := func() (string, string) {
		id, token, err := s.Visit(ctx, wid, "", "")
		must(err)
		return id, token
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
	ago := func(id, col string, d time.Duration) {
		_, err := pool.Exec(ctx, `UPDATE ol_sessions SET `+col+` = now() - $2::interval WHERE id = $1`, id, d.String())
		must(err)
	}
	due := func(want int) {
		t.Helper()
		n, err := s.Due(ctx, time.Now())
		must(err)
		if n != want {
			t.Fatalf("Due changed %d dialogues, want %d", n, want)
		}
	}

	s1, t1 := visitor()
	must(s.VisitorSay(ctx, wid, t1, "Здравствуйте, вопрос"))
	s2, t2 := visitor()
	must(s.VisitorSay(ctx, wid, t2, "Добрый день"))
	if op(s1) != a || op(s2) != b || unread(a, s1) != 1 || unread(b, s1) != 0 || unread(c, s1) != 0 {
		t.Fatalf("strictly in turn: %s %s", op(s1), op(s2))
	}
	msgs, _ := s.VisitorMessages(ctx, wid, t1, 0)
	if len(msgs) != 2 || msgs[1].Body != "Здравствуйте!" || msgs[1].Operator != "Линия" {
		t.Fatalf("the greeting from the line: %+v", msgs)
	}
	must(s.VisitorSay(ctx, wid, t1, "Алло?"))
	if msgs, _ := s.VisitorMessages(ctx, wid, t1, 0); len(msgs) != 3 {
		t.Fatal("the greeting is only for the first message")
	}
	due(0)

	ago(s1, "assigned_at", 4*time.Minute) // a did not answer in 3 minutes: the next in turn after b — c
	due(1)
	if op(s1) != c || unread(a, s1) != 0 || unread(c, s1) != 1 {
		t.Fatalf("passed on to %s", op(s1))
	}
	_, all, _ := s.Open(ctx, boss, s1)
	if last := all[len(all)-1]; last.Kind != "note" || last.Body != "ol.note_passed" {
		t.Fatalf("service line = %+v", last)
	}
	if msgs, _ := s.VisitorMessages(ctx, wid, t1, 0); len(msgs) != 3 {
		t.Fatal("the visitor does not see service lines")
	}

	ago(s1, "waiting_since", 6*time.Minute)
	due(1)
	due(0) // once per wait
	if msgs, _ := s.VisitorMessages(ctx, wid, t1, 0); msgs[len(msgs)-1].Body != "Все заняты" {
		t.Fatalf("no «busy» message: %+v", msgs)
	}

	must(s.Reply(ctx, b, s1, "Слушаю вас")) // b answers first and takes it from c
	if op(s1) != b {
		t.Fatalf("the first to answer takes the dialogue: %s", op(s1))
	}
	ago(s1, "assigned_at", time.Hour)
	due(0) // answered: stays with b
	must(s.Reply(ctx, c, s1, "И я тут"))
	if op(s1) != b {
		t.Fatal("an answered dialogue stays with its operator")
	}

	ago(s1, "last_at", 2*time.Hour) // the visitor left: closed after an hour, with the closing message
	due(1)
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM ol_sessions WHERE id = $1`, s1).Scan(&status))
	if msgs, _ := s.VisitorMessages(ctx, wid, t1, 0); status != "closed" || msgs[len(msgs)-1].Body != "Спасибо!" {
		t.Fatalf("auto close: %s %+v", status, msgs)
	}
	must(s.VisitorSay(ctx, wid, t1, "Ещё вопрос")) // back: to the same operator, greeted again
	if msgs, _ := s.VisitorMessages(ctx, wid, t1, 0); op(s1) != b || msgs[len(msgs)-1].Body != "Здравствуйте!" {
		t.Fatalf("came back: %s", op(s1))
	}
	must(s.SetStatus(ctx, b, s1, false))
	_, all, _ = s.Open(ctx, boss, s1)
	if all[len(all)-1].Body != "ol.note_closed" || all[len(all)-2].Body != "Спасибо!" {
		t.Fatalf("closed by the operator: %+v", all[len(all)-2:])
	}

	// the only operator of the line keeps the dialogue
	solo, _ := s.SaveWidget(ctx, boss, Widget{Name: "Один", Active: true, Operators: []string{a}, QueueMode: "evenly"})
	ss, st, _ := s.Visit(ctx, solo, "", "")
	must(s.VisitorSay(ctx, solo, st, "?"))
	ago(ss, "assigned_at", 10*time.Minute)
	due(1)
	if op(ss) != a {
		t.Fatal("the only operator")
	}
	due(0) // the clock started anew

	// evenly: to whoever has the fewest open dialogues
	even, _ := s.SaveWidget(ctx, boss, Widget{Name: "Ровно", Active: true, Operators: []string{a, c}, QueueMode: "evenly"})
	ev, et, _ := s.Visit(ctx, even, "", "")
	must(s.VisitorSay(ctx, even, et, "1"))
	if op(ev) != c { // a has the solo dialogue
		t.Fatalf("evenly: %s", op(ev))
	}

	// out of working hours: the offline message instead of the greeting, once
	tomorrow := int32((time.Now().Weekday() + 1) % 7)
	night, err := s.SaveWidget(ctx, boss, Widget{Name: "Ночь", Active: true, HoursOn: true, HoursDays: []int32{tomorrow}, HoursFrom: "00:00", HoursTo: "23:59",
		OfflineText: "Мы закрыты", WelcomeText: "Привет"})
	must(err)
	ns, nt, _ := s.Visit(ctx, night, "", "")
	must(s.VisitorSay(ctx, night, nt, "Есть кто?"))
	must(s.VisitorSay(ctx, night, nt, "Ау"))
	msgs, _ = s.VisitorMessages(ctx, night, nt, 0)
	if len(msgs) != 3 || msgs[1].Body != "Мы закрыты" {
		t.Fatalf("out of hours: %+v", msgs)
	}
	ago(ns, "waiting_since", time.Hour)
	due(0)
}
