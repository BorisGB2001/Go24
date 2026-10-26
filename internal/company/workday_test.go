package company

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

func TestWorkdayMath(t *testing.T) {
	start := time.Date(2026, 10, 13, 9, 15, 0, 0, time.Local)
	end := start.Add(8 * time.Hour)
	d := Workday{Started: start, Ended: &end, PauseSecs: 3600}
	if d.Worked(end.Add(time.Hour)) != 7*3600 || d.State() != "done" || !d.Late("09:00") || d.Late("09:30") {
		t.Fatalf("worked %d, state %s", d.Worked(end), d.State())
	}
	paused := start.Add(2 * time.Hour)
	d = Workday{Started: start, PausedAt: &paused}
	if d.Worked(start.Add(5*time.Hour)) != 2*3600 || d.State() != "paused" {
		t.Fatalf("on a pause: %d %s", d.Worked(start.Add(5*time.Hour)), d.State())
	}
}

// TestWorkday: start, a break, finish with a report, continue the day; actions out of turn are refused.
func TestWorkday(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@test') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	t0 := time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	var ve *i18n.Error
	if err := s.WorkAction(ctx, anna, "pause", "", at(0)); err == nil {
		t.Fatal("a break before the day started")
	}
	must(s.WorkAction(ctx, anna, "start", "", at(0)))
	must(s.WorkAction(ctx, anna, "pause", "", at(60)))
	must(s.WorkAction(ctx, anna, "resume", "", at(90)))
	must(s.WorkAction(ctx, anna, "finish", " сделала отчёт ", at(480)))
	d, err := s.Today(ctx, anna, at(500))
	must(err)
	if d == nil || d.State() != "done" || d.PauseSecs != 1800 || d.Worked(at(600)) != 450*60 || d.Report != "сделала отчёт" {
		t.Fatalf("day = %+v", d)
	}
	if err := s.WorkAction(ctx, anna, "finish", "", at(490)); !errors.As(err, &ve) {
		t.Fatalf("finished twice: %v", err)
	}
	must(s.WorkAction(ctx, anna, "reopen", "", at(540))) // the hour between counts as a break
	d, _ = s.Today(ctx, anna, at(541))
	if d.State() != "running" || d.PauseSecs != 1800+3600 {
		t.Fatalf("reopened = %+v", d)
	}
	days, err := s.Workdays(ctx, anna, t0.AddDate(0, 0, -t0.Day()+1), t0.AddDate(0, 1, 0))
	must(err)
	team, err := s.TeamDay(ctx, []string{anna}, t0)
	must(err)
	if len(days) != 1 || len(team) != 1 {
		t.Fatalf("month %d, team %d", len(days), len(team))
	}
}

// TestInvites: the form is checked, someone already in go24 is not invited, the list says who has joined.
func TestEmployeeInvites(t *testing.T) {
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
	var boss string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@test') RETURNING id`).Scan(&boss); err != nil {
		t.Fatal(err)
	}
	s := NewStore(pool)
	for _, bad := range [][2]string{{"not-mail", "Анна"}, {"ann@test", " "}, {"BOSS@test", "Босс"}} {
		if _, _, err := s.CheckInvite(ctx, bad[0], bad[1]); err == nil {
			t.Fatalf("%v must be refused", bad)
		}
	}
	email, name, err := s.CheckInvite(ctx, " Ann@Test ", " Анна ")
	if err != nil || email != "ann@test" || name != "Анна" {
		t.Fatalf("checked = %q %q %v", email, name, err)
	}
	if err := s.SaveInvite(ctx, boss, email, name, "https://sso/x", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, _ := s.Invites(ctx)
	if len(list) != 1 || list[0].Joined || list[0].InvitedBy != "Босс" {
		t.Fatalf("invites = %+v", list)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@test')`); err != nil {
		t.Fatal(err)
	}
	if list, _ = s.Invites(ctx); !list[0].Joined {
		t.Fatal("ann signed in but is not shown as joined")
	}
}
