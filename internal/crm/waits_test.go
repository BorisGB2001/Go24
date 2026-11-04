package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestWaitUntil: «Ждать n дней/недель» after now or before a date; a past or too long wait is an error.
func TestWaitUntil(t *testing.T) {
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	if got, err := WaitUntil(3, "day", nil, now); err != nil || !got.Equal(now.AddDate(0, 0, 3)) {
		t.Fatalf("3 days after now: %v %v", got, err)
	}
	if got, err := WaitUntil(2, "week", nil, now); err != nil || !got.Equal(now.AddDate(0, 0, 14)) {
		t.Fatalf("2 weeks after now: %v %v", got, err)
	}
	close := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	if got, err := WaitUntil(1, "week", &close, now); err != nil || !got.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("a week before the close: %v %v", got, err)
	}
	if _, err := WaitUntil(2, "week", &close, now); err == nil {
		t.Fatal("2 weeks before a date 13 days away must fail")
	}
	for _, n := range []int{0, -1, MaxWait + 1} {
		if _, err := WaitUntil(n, "day", nil, now); err == nil {
			t.Fatalf("%d days must fail", n)
		}
	}
}

// TestCRMWaits: a card waits once at a time — a new wait ends the open one; «Завершить» ends it; the worker ends the
// waits whose day has come and tells the card's responsible; the timeline tells each; a deleted card takes its wait.
func TestCRMWaits(t *testing.T) {
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
	var anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	s := NewStore(pool)
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Responsible: Ref{ID: boris}})
	must(err)
	kinds := func() (out []string) {
		ev, err := s.Timeline(ctx, "lead", lead)
		must(err)
		for _, e := range ev {
			if e.Kind == "wait" || e.Kind == "wait_done" || e.Kind == "wait_over" {
				out = append(out, e.Kind+":"+e.WaitNote())
			}
		}
		return out
	}

	if _, err := s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: time.Now().Add(-time.Minute)}); err == nil {
		t.Fatal("a wait in the past must fail")
	}
	day := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	_, err = s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: day, Description: "ответ клиента"})
	must(err)
	w, err := s.WaitOf(ctx, "lead", lead)
	must(err)
	if w == nil || !w.Until.Equal(day) || w.Description != "ответ клиента" || w.By.Name != "Анна" {
		t.Fatalf("open wait: %+v", w)
	}
	ev, err := s.Timeline(ctx, "lead", lead)
	must(err)
	if ev[0].Kind != "wait" || !ev[0].WaitAt().Equal(day) {
		t.Fatalf("wait line: %+v", ev[0])
	}

	// a new wait ends the open one
	_, err = s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: day.Add(time.Hour), Description: "договор"})
	must(err)
	var open int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_waits WHERE done_at IS NULL`).Scan(&open))
	if open != 1 {
		t.Fatalf("open waits: %d", open)
	}
	tiles, err := s.WaitsOf(ctx, "lead", []string{lead})
	must(err)
	if tiles[lead] == nil || tiles[lead].Description != "договор" {
		t.Fatalf("tiles: %+v", tiles)
	}

	// the worker: not yet, then the day has come
	n, err := s.EndWaitsDue(ctx, time.Now())
	must(err)
	if n != 0 {
		t.Fatalf("ended early: %d", n)
	}
	n, err = s.EndWaitsDue(ctx, day.Add(2*time.Hour))
	must(err)
	if n != 1 {
		t.Fatalf("ended: %d", n)
	}
	if w, err := s.WaitOf(ctx, "lead", lead); err != nil || w != nil {
		t.Fatalf("still waits: %+v %v", w, err)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_wait_over' AND body = 'Заявка'`, boris).Scan(&told))
	if told != 1 {
		t.Fatalf("the responsible is told %d times", told)
	}

	// «Завершить» by hand; with no wait — nothing
	_, err = s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: day})
	must(err)
	must(s.EndWait(ctx, boris, "lead", lead))
	must(s.EndWait(ctx, boris, "lead", lead))
	got := kinds()
	want := []string{"wait_done:", "wait:", "wait_over:договор", "wait:договор", "wait_done:ответ клиента", "wait:ответ клиента"}
	if len(got) != len(want) {
		t.Fatalf("timeline: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("timeline: %v, want %v", got, want)
		}
	}

	// a deleted card takes its wait
	_, err = s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: day})
	must(err)
	_, err = pool.Exec(ctx, `DELETE FROM crm_leads WHERE id = $1`, lead)
	must(err)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_waits`).Scan(&open))
	if open != 0 {
		t.Fatalf("waits of a deleted card: %d", open)
	}
	if _, err := s.AddWait(ctx, anna, Wait{Entity: "lead", EntityID: lead, Until: day}); err != ErrNotFound {
		t.Fatalf("a wait of no card: %v", err)
	}
}
