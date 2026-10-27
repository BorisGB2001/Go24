package bp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestEventEnd: «Событие календаря» puts a meeting into each person's calendar — from a date and time for an hour or
// till the end field, from a date for the whole day; «Завершить бизнес-процесс» ends the process with its branches.
func TestEventEnd(t *testing.T) {
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
	ann, ivan, olga := newUser("ann"), newUser("ivan"), newUser("olga")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	fields := []Field{{Key: "day", Label: "День", Kind: "date"}, {Key: "from", Label: "С", Kind: "datetime"},
		{Key: "to", Label: "По", Kind: "datetime"}, {Key: "topic", Label: "Тема", Kind: "string"}}

	var ve *i18n.Error
	for name, st := range map[string]Step{
		"no people":      {Kind: "event", Text: "x", Start: "day"},
		"no title":       {Kind: "event", Users: []string{ivan}, Start: "day"},
		"a string start": {Kind: "event", Text: "x", Users: []string{ivan}, Start: "topic"},
		"an unknown end": {Kind: "event", Text: "x", Users: []string{ivan}, Start: "day", End: "nope"},
	} {
		if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: fields, Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Встреча", Active: true, Fields: fields, Steps: []Step{
		{Kind: "event", Text: "Встреча: {topic}", Note: "О {topic}", Users: []string{"starter", ivan, ivan}, Start: "from"},
		{Kind: "event", Text: "Разбор", Users: []string{ivan}, Start: "from", End: "to"},
		{Kind: "event", Text: "Весь день", Users: []string{olga}, Start: "day"},
		{Kind: "parallel", Branches: []int{6}},
		{Kind: "approve", To: olga, Text: "Утвердите"},
		{Kind: "end", Text: "Готово"},
	}})
	must(err)
	// no calendar: the step fails
	id, err := s.Start(ctx, ann, tpl, "", map[string]string{"day": "2026-11-02", "from": "2026-11-03T10:00", "to": "2026-11-03T12:30", "topic": "план"})
	must(err)
	if x, _ := s.Instance(ctx, ann, id); x.Status != "failed" {
		t.Fatalf("no calendar: %s", x.Status)
	}
	s.Calendar = calendar.NewStore(pool)
	id, err = s.Start(ctx, ann, tpl, "", map[string]string{"day": "2026-11-02", "from": "2026-11-03T10:00", "to": "2026-11-03T12:30", "topic": "план"})
	must(err)
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Status != "done" {
		t.Fatalf("the end step does not end the process: %s", x.Status)
	}
	if mine, _ := s.MyApprovals(ctx, olga); len(mine) != 0 {
		t.Fatalf("an approval of the ended process is asked: %+v", mine)
	}
	type ev struct {
		owner, title, desc string
		from, to           time.Time
		day                bool
	}
	var got []ev
	rows, err := pool.Query(ctx, `SELECT owner_id::text, title, description, starts_at, ends_at, all_day FROM events ORDER BY created_at, owner_id`)
	must(err)
	for rows.Next() {
		var e ev
		must(rows.Scan(&e.owner, &e.title, &e.desc, &e.from, &e.to, &e.day))
		got = append(got, e)
	}
	must(rows.Err())
	at := func(v string) time.Time { t, _ := time.ParseInLocation("2006-01-02T15:04", v, time.Local); return t }
	if len(got) != 4 {
		t.Fatalf("events: %+v", got)
	}
	count := map[string]int{}
	for _, e := range got {
		count[e.owner+" "+e.title]++
		switch e.title {
		case "Встреча: план":
			if !e.from.Equal(at("2026-11-03T10:00")) || !e.to.Equal(at("2026-11-03T11:00")) || e.day || e.desc != "О план" {
				t.Fatalf("an hour's meeting: %+v", e)
			}
		case "Разбор":
			if !e.to.Equal(at("2026-11-03T12:30")) || e.owner != ivan {
				t.Fatalf("till the end field: %+v", e)
			}
		case "Весь день":
			if !e.day || e.owner != olga || !e.from.Equal(at("2026-11-02T00:00")) {
				t.Fatalf("a whole day: %+v", e)
			}
		}
	}
	if count[ann+" Встреча: план"] != 1 || count[ivan+" Встреча: план"] != 1 {
		t.Fatalf("each person once: %v", count)
	}
	lines, err := s.Log(ctx, id)
	must(err)
	var ended bool
	for _, l := range lines {
		ended = ended || l.Kind == "ended" && l.Text == "Готово"
	}
	if !ended {
		t.Fatalf("no «ended» in the history: %+v", lines)
	}
}
