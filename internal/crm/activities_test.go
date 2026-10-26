package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestActivities: deeds are planned on a card (another responsible is notified), counted for «Мои дела», shown as
// the nearest on the kanban, reminded once before the deadline, done into the timeline, and go with their card.
func TestActivities(t *testing.T) {
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
	anna, petr := newUser("anna"), newUser("petr")
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)
	deal, err := s.SaveDeal(ctx, anna, Deal{Title: "Поставка", PipelineID: pipes[0].ID})
	must(err)
	now := time.Now()

	if _, err := s.AddActivity(ctx, anna, Activity{Entity: "deal", EntityID: deal, Subject: " ", Deadline: now}); err == nil {
		t.Fatal("an activity without a subject")
	}
	if _, err := s.AddActivity(ctx, anna, Activity{Entity: "deal", EntityID: anna, Subject: "x", Deadline: now}); err == nil {
		t.Fatal("an activity on no card")
	}
	late, err := s.AddActivity(ctx, anna, Activity{Entity: "deal", EntityID: deal, Kind: "call", Subject: "Позвонить", Deadline: now.Add(-time.Hour)})
	must(err)
	_, err = s.AddActivity(ctx, anna, Activity{Entity: "deal", EntityID: deal, Kind: "meeting", Subject: "Встреча", Deadline: now.Add(48 * time.Hour),
		Responsible: Ref{ID: petr}})
	must(err)
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_activity' AND url = '/crm/deals/' || $2`, petr, deal).Scan(&notes))
	if notes != 1 {
		t.Fatalf("petr notified %d times", notes)
	}

	if n, _ := s.ActivityCounter(ctx, anna, now); n != 1 {
		t.Fatalf("anna's counter %d", n)
	}
	if n, _ := s.ActivityCounter(ctx, petr, now); n != 0 {
		t.Fatalf("petr's counter %d (his is in two days)", n)
	}
	for f, want := range map[string]int{"overdue": 1, "today": 1, "planned": 0, "all": 1} {
		if list, err := s.MyActivities(ctx, anna, f, now); err != nil || len(list) != want {
			t.Fatalf("anna %s: %d %v", f, len(list), err)
		}
	}
	if list, _ := s.MyActivities(ctx, petr, "planned", now); len(list) != 1 || list[0].CardTitle != "Поставка" || list[0].Path() != "/crm/deals/"+deal {
		t.Fatalf("petr planned: %+v", list)
	}
	next, err := s.NextActivities(ctx, "deal", []string{deal})
	must(err)
	if a := next[deal]; a == nil || a.ID != late || !a.Overdue() {
		t.Fatalf("next: %+v", next[deal])
	}

	// reminders: the overdue one now, once; the far one not yet
	if n, err := s.RemindDue(ctx, now); err != nil || n != 1 {
		t.Fatalf("reminded %d %v", n, err)
	}
	if n, _ := s.RemindDue(ctx, now); n != 0 {
		t.Fatal("reminded twice")
	}
	if n, _ := s.RemindDue(ctx, now.Add(48*time.Hour-10*time.Minute)); n != 1 {
		t.Fatal("the meeting was not reminded before its time")
	}

	a, err := s.CompleteActivity(ctx, anna, late)
	must(err)
	if a.Subject != "Позвонить" {
		t.Fatalf("completed %+v", a)
	}
	if list, _ := s.Activities(ctx, "deal", deal); len(list) != 1 || list[0].Kind != "meeting" {
		t.Fatalf("open after completing: %+v", list)
	}
	tl, err := s.Timeline(ctx, "deal", deal)
	must(err)
	if tl[0].Kind != "activity" || tl[0].Body != "call:Позвонить" {
		t.Fatalf("timeline: %+v", tl[0])
	}

	must(s.DeleteDeal(ctx, anna, deal))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_activities`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d activities outlived their card", left)
	}
}
