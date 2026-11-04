package company

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTimeControl: a gap in activity of the running day is an absence; pauses, finished days, the time before the
// control was switched on and short gaps are not; the person answers only their own absence.
func TestTimeControl(t *testing.T) {
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
	var anna, oleg string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('oleg', 'Олег', 'oleg@test') RETURNING id`).Scan(&oleg))
	s := NewStore(pool)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	after := 15 * time.Minute
	alive := func(m int, since *time.Time) *Idle {
		t.Helper()
		i, err := s.Alive(ctx, anna, at(m), after, since)
		must(err)
		return i
	}

	if alive(0, nil) != nil { // no working day — nothing
		t.Fatal("an absence without a day")
	}
	must(s.WorkAction(ctx, anna, "start", "", at(0), Place{}))
	if alive(10, nil) != nil || alive(24, nil) != nil { // gaps of 10 and 14 minutes
		t.Fatal("a short gap is an absence")
	}
	i := alive(60, nil) // 24 → 60
	if i == nil || !i.Started.Equal(at(24)) || !i.Ended.Equal(at(60)) || i.Secs() != 36*60 || i.Day.Format(time.DateOnly) != "2026-10-13" {
		t.Fatalf("absence %+v", i)
	}
	// a break is not an absence: resuming starts the count anew
	must(s.WorkAction(ctx, anna, "pause", "", at(61), Place{}))
	if alive(90, nil) != nil {
		t.Fatal("counted on a break")
	}
	must(s.WorkAction(ctx, anna, "resume", "", at(150), Place{}))
	if alive(155, nil) != nil {
		t.Fatal("the break was counted after resuming")
	}
	// switched on later: the gap before it is not counted
	since := at(200)
	if alive(230, &since) != nil {
		t.Fatal("counted before the control was on")
	}
	if alive(260, &since) == nil {
		t.Fatal("a gap after switching on was not counted")
	}
	open, err := s.OpenIdle(ctx, anna, at(260))
	must(err)
	if open == nil || !open.Started.Equal(at(230)) {
		t.Fatalf("open %+v", open)
	}
	// answers: only one's own, a known kind
	if err := s.AnswerIdle(ctx, oleg, open.ID, "work", "", at(261)); err == nil {
		t.Fatal("answered another's absence")
	}
	if err := s.AnswerIdle(ctx, anna, open.ID, "nap", "", at(261)); err == nil {
		t.Fatal("an unknown kind")
	}
	must(s.AnswerIdle(ctx, anna, open.ID, "work", " склад ", at(261)))
	must(s.AnswerIdle(ctx, anna, i.ID, "personal", "", at(261)))
	if open, err := s.OpenIdle(ctx, anna, at(262)); err != nil || open != nil {
		t.Fatalf("still open %+v %v", open, err)
	}
	// a finished day is not watched
	must(s.WorkAction(ctx, anna, "finish", "", at(300), Place{}))
	if alive(400, nil) != nil {
		t.Fatal("counted after the day")
	}
	list, err := s.Idles(ctx, []string{anna, oleg}, t0, t0.AddDate(0, 0, 1))
	must(err)
	if len(list) != 2 || list[1].Reason != "склад" || list[1].Kind != "work" || list[0].Kind != "personal" || list[0].Name != "Анна" {
		t.Fatalf("idles %+v", list)
	}
	rows := IdleReport([]Person{{ID: oleg, Name: "Олег"}, {ID: anna, Name: "Анна"}}, list)
	if len(rows) != 2 || len(rows[0].Idles) != 0 || rows[1].Secs != 36*60+30*60 || rows[1].Work != 30*60 || rows[1].Personal != 36*60 || rows[1].No != 0 {
		t.Fatalf("report %+v", rows)
	}
}
