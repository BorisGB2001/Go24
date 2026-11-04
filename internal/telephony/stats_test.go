package telephony

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestStatsReports: «Пропущенные звонки» — a missed call is recalled by a later conversation with its number (called
// back from another spelling of it, or the client got through), the numbers nobody talked to are listed once with
// their calls counted; «Нагрузка по времени суток» — the incoming calls by weekday and hour; the period before is
// counted for the comparison.
func TestStatsReports(t *testing.T) {
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
	var ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ann', 'ann') RETURNING id`).Scan(&ann))
	s := NewStore(pool, "secret")
	must(s.SetNumber(ctx, ann, "101"))
	const line = "+74951234567"
	t0 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC) // a Monday
	for _, c := range []CDR{
		{UniqueID: "p.1", From: "+79001110009", To: line, By: "101", Line: line, Dial: "ANSWER", Start: t0.AddDate(0, 0, -1)},
		{UniqueID: "m.1", From: "+79001110001", To: line, By: "101", Line: line, Dial: "NOANSWER", Start: t0},
		{UniqueID: "o.1", From: "101", To: "89001110001", Line: line, Dial: "ANSWER", Start: t0.Add(30 * time.Minute)},
		{UniqueID: "m.2", From: "+79001110002", To: line, By: "101", Line: line, Dial: "NOANSWER", Start: t0.Add(time.Hour)},
		{UniqueID: "m.3", From: "+79001110002", To: line, By: "101", Line: line, Dial: "BUSY", Start: t0.Add(2 * time.Hour)},
		{UniqueID: "m.4", From: "+79001110003", To: line, By: "101", Line: line, Dial: "NOANSWER", Start: t0.Add(3 * time.Hour)},
		{UniqueID: "a.4", From: "+79001110003", To: line, By: "101", Line: line, Dial: "ANSWER", Start: t0.Add(4 * time.Hour)},
	} {
		c.End = c.Start.Add(time.Minute)
		if c.Dial == "ANSWER" {
			c.Answer = c.Start.Add(5 * time.Second)
		}
		_, err := s.SaveCall(ctx, c)
		must(err)
	}
	st, err := s.Stats(ctx, StatFilter{From: t0.Add(-10 * time.Hour), To: t0.Add(14 * time.Hour)}, time.UTC)
	must(err)
	m := st.Missed
	if m.Missed != 4 || m.Recalled != 2 || m.NotRecalled() != 2 || m.RecalledPct() != 50 || m.AvgRecall() != 2700 {
		t.Fatalf("missed calls: %+v", m)
	}
	if len(m.Waiting) != 1 || m.Waiting[0].Number != "+79001110002" || m.Waiting[0].Times != 2 ||
		!m.Waiting[0].Last.Equal(t0.Add(2*time.Hour)) || m.Waiting[0].UserID != ann {
		t.Fatalf("not called back: %+v", m.Waiting)
	}
	if h := st.Hours[0]; h[10] != (StatHour{In: 1, Missed: 1}) || h[14] != (StatHour{In: 1}) || h[9] != (StatHour{}) || st.Hours[6][10] != (StatHour{}) {
		t.Fatalf("by hour: %+v", h)
	}
	if st.Total.In != 5 || st.Prev.In != 1 || st.Prev.Missed != 0 {
		t.Fatalf("the period before: %+v / %+v", st.Total, st.Prev)
	}
	// only one employee's calls: the conversation recalling is anybody's
	if st, _ := s.Stats(ctx, StatFilter{From: t0, To: t0.Add(time.Hour), UserID: ann}, time.UTC); st.Missed.Recalled != 1 {
		t.Fatalf("ann's missed calls: %+v", st.Missed)
	}
}
