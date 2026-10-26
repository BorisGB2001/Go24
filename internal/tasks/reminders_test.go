package tasks

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

// freshDB gives a store on a recreated schema of GO24_TEST_DATABASE_URL (skips the test without it).
func freshDB(t *testing.T) (*pgxpool.Pool, *Store) {
	t.Helper()
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, NewStore(pool)
}

// TestReminders: a reminder at a time and one before the deadline, to oneself and to the responsible; each is sent
// once, a muted task still reminds, a closed task does not.
func TestReminders(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	creator, resp, stranger := newUser("creator"), newUser("resp"), newUser("stranger")
	p, err := s.CreateProject(ctx, creator, "rm", "Напоминания", "")
	must(err)
	proj, err := s.ProjectByKey(ctx, creator, p)
	must(err)
	now := time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local)
	deadline := now.Add(3 * time.Hour)
	key, err := s.CreateTask(ctx, creator, proj, NewTask{Title: "Отчёт", ResponsibleID: resp, Deadline: &deadline})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)

	var ve *i18n.Error
	at := now.Add(30 * time.Minute)
	past := now.Add(-time.Minute)
	if err := s.AddReminder(ctx, creator, id, "self", &past, 0, now); !errors.As(err, &ve) {
		t.Fatalf("a reminder in the past must fail, got %v", err)
	}
	if err := s.AddReminder(ctx, creator, id, "boss", &at, 0, now); !errors.As(err, &ve) {
		t.Fatalf("an unknown recipient must fail, got %v", err)
	}
	if err := s.AddReminder(ctx, stranger, id, "self", &at, 0, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger must not set reminders, got %v", err)
	}
	must(s.AddReminder(ctx, creator, id, "self", &at, 0, now))
	must(s.AddReminder(ctx, creator, id, "responsible", nil, 60, now))
	list, err := s.Reminders(ctx, creator, id)
	must(err)
	if len(list) != 2 || list[0].At == nil || list[1].Due == nil || !list[1].Due.Equal(deadline.Add(-time.Hour)) {
		t.Fatalf("reminders = %+v", list)
	}
	if other, _ := s.Reminders(ctx, resp, id); len(other) != 0 {
		t.Fatal("reminders are the person's own")
	}

	must(s.SetMark(ctx, creator, id, "mute", true))
	reminded := func(user string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'task_remind' AND user_id = $1`, user).Scan(&n))
		return n
	}
	if n, err := s.RemindDue(ctx, now.Add(10*time.Minute)); err != nil || n != 0 {
		t.Fatalf("too early: %d %v", n, err)
	}
	if n, err := s.RemindDue(ctx, now.Add(31*time.Minute)); err != nil || n != 1 || reminded(creator) != 1 {
		t.Fatalf("at the time: %d %v, creator got %d", n, err, reminded(creator))
	}
	// the deadline moves: the reminder before it follows
	later := deadline.Add(time.Hour)
	must(s.SetDeadline(ctx, creator, id, &later))
	if n, _ := s.RemindDue(ctx, deadline.Add(-time.Hour)); n != 0 {
		t.Fatal("the reminder must follow the deadline")
	}
	if n, _ := s.RemindDue(ctx, later.Add(-time.Hour)); n != 1 || reminded(resp) != 1 {
		t.Fatalf("before the deadline: %d, responsible got %d", n, reminded(resp))
	}
	if n, _ := s.RemindDue(ctx, later); n != 0 {
		t.Fatal("reminded twice")
	}

	// a closed task reminds no more
	must(s.AddReminder(ctx, resp, id, "self", &later, 0, now))
	must(s.ApplyAction(ctx, resp, id, ActionStart))
	must(s.ApplyAction(ctx, resp, id, ActionComplete))
	if n, _ := s.RemindDue(ctx, later.Add(time.Minute)); n != 0 {
		t.Fatal("a closed task must not remind")
	}
	list, _ = s.Reminders(ctx, resp, id)
	must(s.DeleteReminder(ctx, resp, id, list[0].ID))
	if err := s.DeleteReminder(ctx, creator, id, list[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
