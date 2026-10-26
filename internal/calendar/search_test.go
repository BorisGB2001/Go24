package calendar

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCalendarSearch: the search of the calendar — by text, the presets «Приглашения», «Я организатор», «Отклонённые»,
// the counter of invitations; someone else's private meeting is not found by its text, a repeated one is found once.
func TestCalendarSearch(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := user("boss"), user("anna")
	s := NewStore(pool)
	now := time.Now().Truncate(time.Hour)
	save := func(owner string, e Event, invite ...string) string {
		t.Helper()
		id, err := s.Save(ctx, owner, e, invite)
		must(err)
		return id
	}
	soon := now.AddDate(0, 0, 2)
	plan := save(boss, Event{Title: "Планёрка отдела", Start: soon, End: soon.Add(time.Hour)}, anna)
	past := save(boss, Event{Title: "Старая планёрка", Start: now.AddDate(0, 0, -5), End: now.AddDate(0, 0, -5).Add(time.Hour)}, anna)
	review := save(boss, Event{Title: "Обзор", Where: "Переговорная 100%", Start: soon.Add(3 * time.Hour), End: soon.Add(4 * time.Hour)}, anna)
	save(boss, Event{Title: "Секретная планёрка", Private: true, Start: soon, End: soon.Add(time.Hour)})
	weekly := save(anna, Event{Title: "Еженедельная планёрка", Repeat: "weekly", Start: now.AddDate(0, 0, -20), End: now.AddDate(0, 0, -20).Add(time.Hour)})

	titles := func(viewer, text, preset string) (out []string) {
		t.Helper()
		list, err := s.Search(ctx, viewer, text, preset)
		must(err)
		for _, e := range list {
			out = append(out, e.Title)
		}
		return
	}
	eq := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	if got := titles(anna, "планёрка", ""); !eq(got, "Старая планёрка", "Планёрка отдела", "Еженедельная планёрка") && !eq(got, "Старая планёрка", "Еженедельная планёрка", "Планёрка отдела") {
		t.Fatalf("anna finds %v", got)
	}
	if got := titles(boss, "планёрка", ""); !eq(got, "Старая планёрка", "Планёрка отдела", "Секретная планёрка") && !eq(got, "Старая планёрка", "Секретная планёрка", "Планёрка отдела") {
		t.Fatalf("boss finds %v", got)
	}
	if got := titles(anna, "100%", ""); !eq(got, "Обзор") {
		t.Fatalf("by the place: %v", got)
	}
	if got := titles(anna, "_", ""); len(got) != 0 {
		t.Fatalf("_ is not a wildcard: %v", got)
	}
	if got := titles(anna, "", ""); got != nil {
		t.Fatalf("nothing asked, found %v", got)
	}
	list, err := s.Search(ctx, anna, "еженедельная", "")
	must(err)
	if len(list) != 1 || list[0].Start.Before(now) || list[0].SeriesStart.After(now) {
		t.Fatalf("a repeated meeting is found at its next occurrence: %+v", list)
	}
	_ = weekly

	if got := titles(anna, "", PresetInvites); !eq(got, "Планёрка отдела", "Обзор") {
		t.Fatalf("anna's invitations %v (a past one is not)", got)
	}
	if n, err := s.Invitations(ctx, anna); err != nil || n != 2 {
		t.Fatalf("anna's counter = %d, %v", n, err)
	}
	must(s.Respond(ctx, anna, review, "declined"))
	must(s.Respond(ctx, anna, past, "declined"))
	if got := titles(anna, "", PresetInvites); !eq(got, "Планёрка отдела") {
		t.Fatalf("after declining: %v", got)
	}
	if got := titles(anna, "", PresetDeclined); !eq(got, "Старая планёрка", "Обзор") {
		t.Fatalf("anna declined %v", got)
	}
	if got := titles(anna, "обзор", PresetDeclined); !eq(got, "Обзор") {
		t.Fatalf("a preset with text: %v", got)
	}
	if got := titles(anna, "", PresetOrganizer); !eq(got, "Еженедельная планёрка") {
		t.Fatalf("anna organizes %v", got)
	}
	if got := titles(boss, "", PresetInvites); len(got) != 0 {
		t.Fatalf("the organizer is not invited to their own meetings: %v", got)
	}
	if n, _ := s.Invitations(ctx, anna); n != 1 {
		t.Fatalf("counter after the answers = %d", n)
	}
	if _, err := s.Search(ctx, anna, "", "nonsense"); err == nil {
		t.Fatal("an unknown preset")
	}
	_ = plan
}
