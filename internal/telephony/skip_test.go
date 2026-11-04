package telephony

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestOffList: GO24_OFF has the inner numbers of the unavailable employees between commas, not a second holder of
// a number; nobody — a lone comma, which no number matches.
func TestOffList(t *testing.T) {
	lines := []Line{{UserID: "1", Number: "101"}, {UserID: "2", Number: "102"}, {UserID: "3", Number: "102", Dup: true}, {UserID: "4", Number: "104"}}
	if got := OffList(lines, map[string]string{"2": SkipDND, "3": SkipAbsent, "4": SkipWorkday}); got != ",102,104," {
		t.Errorf("off list %q", got)
	}
	if got := OffList(lines, nil); got != "," {
		t.Errorf("nobody off: %q", got)
	}
}

// TestUnavailable: «Не беспокоить» and the absent ones are passed by by default, the working day when asked (a day
// going since yesterday counts, a break or a finished day does not); the reason is the first of them; employees
// without an inner number are not there; the rule turned off passes nobody by.
func TestUnavailable(t *testing.T) {
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
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.Local)
	today := now.Format(time.DateOnly)
	ids := map[string]string{}
	for _, u := range []struct{ name, phone, presence string }{
		{"ann", "101", ""}, {"bob", "102", "dnd"}, {"eve", "103", ""}, {"dan", "104", "dnd"}, {"kim", "105", ""},
		{"lea", "106", ""}, {"max", "107", ""}, {"nik", "", "dnd"},
	} {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, inner_phone, presence) VALUES ($1, $1, $2, $3) RETURNING id`,
			u.name, u.phone, u.presence).Scan(&id))
		ids[u.name] = id
	}
	// eve and dan are on vacation today, kim's vacation was yesterday
	for _, a := range []struct{ who, from, to string }{{"eve", today, today}, {"dan", "2026-10-20", "2026-11-02"}, {"kim", "2026-10-27", "2026-10-28"}} {
		_, err := pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2, $3, $1)`,
			ids[a.who], a.from, a.to)
		must(err)
	}
	// ann works today, lea since yesterday's night shift, kim is on a break, max finished
	for _, d := range []struct {
		who, day       string
		ended, paused bool
	}{{"ann", today, false, false}, {"lea", "2026-10-28", false, false}, {"kim", today, false, true}, {"max", today, true, false}} {
		_, err := pool.Exec(ctx, `INSERT INTO workdays (user_id, day, started_at, ended_at, paused_at) VALUES ($1, $2, $3,
			CASE WHEN $4 THEN $3::timestamptz + interval '1 hour' END, CASE WHEN $5 THEN $3::timestamptz + interval '1 hour' END)`,
			ids[d.who], d.day, now.Add(-2*time.Hour), d.ended, d.paused)
		must(err)
	}

	s := NewStore(pool, "secret")
	check := func(want map[string]string) {
		t.Helper()
		got, err := s.Unavailable(ctx, now)
		must(err)
		if len(got) != len(want) {
			t.Errorf("unavailable %v, want %v", got, want)
		}
		for name, why := range want {
			if got[ids[name]] != why {
				t.Errorf("%s: %q, want %q", name, got[ids[name]], why)
			}
		}
	}
	check(map[string]string{"bob": SkipDND, "dan": SkipDND, "eve": SkipAbsent})

	must(s.SetSkipping(ctx, ids["ann"], Skip{Absent: true, Workday: true}))
	check(map[string]string{"bob": SkipWorkday, "dan": SkipAbsent, "eve": SkipAbsent, "kim": SkipWorkday, "max": SkipWorkday})

	must(s.SetSkipping(ctx, ids["ann"], Skip{}))
	if sk, err := s.Skipping(ctx); err != nil || sk != (Skip{}) {
		t.Fatalf("the rule is not off: %+v %v", sk, err)
	}
	check(map[string]string{})
}
