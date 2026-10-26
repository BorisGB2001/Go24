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

// TestKPI: a dialogue waiting longer than the line's KPI — for the first answer and for the next ones — is a violation
// once per wait: a service line, notifications to the queue and the heads, the counters of the dialogue and the report.
func TestKPI(t *testing.T) {
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
	boss, a, head := newUser("boss"), newUser("a"), newUser("head")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	must(s.SetHeads(ctx, boss, []string{head}))
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "KPI", Active: true, Operators: []string{a}, KPIFirst: 2, KPIFurther: 5, KPIOperator: true, KPIHeads: true})
	must(err)
	if w, _ := s.Widget(ctx, wid); w.KPIFirst != 2 || w.KPIFurther != 5 || !w.KPIOperator || !w.KPIHeads {
		t.Fatalf("line = %+v", w)
	}
	due := func(want int) {
		t.Helper()
		n, err := s.Due(ctx, time.Now())
		must(err)
		if n != want {
			t.Fatalf("Due changed %d dialogues, want %d", n, want)
		}
	}
	waited := func(id string, d time.Duration) {
		_, err := pool.Exec(ctx, `UPDATE ol_sessions SET waiting_since = now() - $2::interval WHERE id = $1`, id, d.String())
		must(err)
	}
	told := func(user string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND body LIKE 'ol.kpi_notify%'`, user).Scan(&n))
		return n
	}
	late := func(id string) (bool, int) {
		var first bool
		var n int
		must(pool.QueryRow(ctx, `SELECT kpi_first_late, kpi_late FROM ol_sessions WHERE id = $1`, id).Scan(&first, &n))
		return first, n
	}

	sid, tok, err := s.Visit(ctx, wid, "", "")
	must(err)
	must(s.VisitorSay(ctx, wid, tok, "Здравствуйте"))
	due(0)
	waited(sid, 3*time.Minute) // no first answer in 2 minutes
	due(1)
	due(0) // once per wait
	if first, n := late(sid); !first || n != 1 || told(a) != 1 || told(head) != 1 || told(boss) != 0 {
		t.Fatalf("first answer late: %v %d, told a %d, head %d", first, n, told(a), told(head))
	}
	_, all, _ := s.Open(ctx, boss, sid)
	if last := all[len(all)-1]; last.Kind != "note" || last.Body != "ol.kpi_first" {
		t.Fatalf("service line = %+v", last)
	}
	if x, _ := s.Session(ctx, sid); x.KPILate != 1 {
		t.Fatalf("the dialogue's violations = %d", x.KPILate)
	}

	must(s.Reply(ctx, a, sid, "Слушаю"))
	must(s.VisitorSay(ctx, wid, tok, "Ещё вопрос"))
	waited(sid, 4*time.Minute) // within 5 minutes for the next answers
	due(0)
	waited(sid, 6*time.Minute)
	due(1)
	if _, n := late(sid); n != 2 || told(a) != 2 {
		t.Fatalf("next answer late: %d, told a %d", n, told(a))
	}
	_, all, _ = s.Open(ctx, boss, sid)
	if last := all[len(all)-1]; last.Body != "ol.kpi_further" {
		t.Fatalf("service line = %+v", last)
	}

	// only the heads are told; a line without KPI has no violations
	off, err := s.SaveWidget(ctx, boss, Widget{Name: "Без KPI", Active: true, Operators: []string{a}})
	must(err)
	oid, otok, _ := s.Visit(ctx, off, "", "")
	must(s.VisitorSay(ctx, off, otok, "?"))
	waited(oid, 24*time.Hour)
	due(0)
	heads, err := s.SaveWidget(ctx, boss, Widget{Name: "Руководителю", Active: true, Operators: []string{a}, KPIFirst: 1, KPIHeads: true})
	must(err)
	hid, htok, _ := s.Visit(ctx, heads, "", "")
	must(s.VisitorSay(ctx, heads, htok, "?"))
	waited(hid, 2*time.Minute)
	due(1)
	if told(a) != 2 || told(head) != 3 {
		t.Fatalf("heads only: told a %d, head %d", told(a), told(head))
	}

	st, err := s.Stats(ctx, StatsFilter{From: time.Now(), To: time.Now()})
	must(err)
	if st.Total.KPI != 2 || st.Total.New != 3 {
		t.Fatalf("report: %+v", st.Total)
	}
}
