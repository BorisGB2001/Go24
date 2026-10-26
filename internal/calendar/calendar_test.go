package calendar

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalendar: a meeting with invitations — who sees it, notifications, answers, a moved meeting asks again;
// task deadlines are shown to their people.
func TestCalendar(t *testing.T) {
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
	boss, anna, stranger := user("boss"), user("anna"), user("stranger")
	s := NewStore(pool)
	day := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	month0, month1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)

	if _, err := s.Save(ctx, boss, Event{Title: "x", Start: day, End: day.Add(-time.Hour)}, nil); err == nil {
		t.Fatal("a meeting ending before it starts was saved")
	}
	id, err := s.Save(ctx, boss, Event{Title: "Планёрка", Where: "Переговорная", Start: day, End: day.Add(time.Hour)}, []string{anna, boss, ""})
	must(err)
	kinds := func(u string) (k []string) {
		rows, err := pool.Query(ctx, `SELECT kind FROM notifications WHERE user_id = $1 ORDER BY id`, u)
		must(err)
		for rows.Next() {
			var x string
			must(rows.Scan(&x))
			k = append(k, x)
		}
		return
	}
	if k := kinds(anna); len(k) != 1 || k[0] != "event" {
		t.Fatalf("anna notified %v", k)
	}
	list, err := s.Range(ctx, anna, month0, month1)
	must(err)
	if len(list) != 1 || list[0].MyStatus != "invited" || list[0].Where != "Переговорная" || len(list[0].Attendees) != 1 {
		t.Fatalf("anna's calendar = %+v", list)
	}
	if list, _ := s.Range(ctx, stranger, month0, month1); len(list) != 0 {
		t.Fatalf("a stranger sees %+v", list)
	}
	if _, err := s.Get(ctx, stranger, id); err != ErrNotFound {
		t.Fatalf("stranger get: %v", err)
	}
	if _, err := s.Save(ctx, anna, Event{ID: id, Title: "Моя", Start: day, End: day}, nil); err != ErrForbidden {
		t.Fatalf("an attendee changed the meeting: %v", err)
	}
	must(s.Respond(ctx, anna, id, "accepted"))
	if k := kinds(boss); len(k) != 1 || k[0] != "event_reply" {
		t.Fatalf("boss notified %v", k)
	}
	if err := s.Respond(ctx, stranger, id, "accepted"); err != ErrNotFound {
		t.Fatalf("a stranger answered: %v", err)
	}
	// moved: anna is asked again
	_, err = s.Save(ctx, boss, Event{ID: id, Title: "Планёрка", Start: day.Add(24 * time.Hour), End: day.Add(25 * time.Hour)}, []string{anna})
	must(err)
	e, err := s.Get(ctx, anna, id)
	must(err)
	if e.MyStatus != "invited" || len(kinds(anna)) != 2 {
		t.Fatalf("after moving: status %q, notifications %v", e.MyStatus, kinds(anna))
	}
	// company calendar: everyone; task deadlines: their people
	_, err = s.Save(ctx, boss, Event{Title: "Корпоратив", Company: true, AllDay: true, Start: day, End: day}, nil)
	must(err)
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	dl := day.Add(5 * time.Hour)
	_, err = ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Отчёт", ResponsibleID: boss, Deadline: &dl})
	must(err)
	list, err = s.Range(ctx, stranger, month0, month1)
	must(err)
	if len(list) != 1 || !list[0].Company || !list[0].AllDay {
		t.Fatalf("stranger's calendar = %+v", list)
	}
	list, err = s.Range(ctx, boss, month0, month1)
	must(err)
	var task *Event
	for i := range list {
		if list[i].Kind == "task" {
			task = &list[i]
		}
	}
	if len(list) != 3 || task == nil || task.TaskKey != "GO-1" || !list[0].AllDay {
		t.Fatalf("boss's calendar = %+v", list)
	}
	// a weekly meeting every 2 weeks until the end of November, reminded an hour and 15 minutes before
	until := time.Date(2026, 11, 30, 0, 0, 0, 0, time.Local)
	wid, err := s.Save(ctx, boss, Event{Title: "Ретро", Start: day, End: day.Add(time.Hour), Repeat: "weekly", Every: 2,
		Until: &until, Reminds: []int{15, 60, 15, -5}}, []string{anna})
	must(err)
	all, err := s.Range(ctx, anna, month0, time.Date(2027, 1, 1, 0, 0, 0, 0, time.Local))
	must(err)
	var starts []string
	for _, e := range all {
		if e.ID == wid {
			starts = append(starts, e.Start.Format("01-02"))
		}
	}
	if strings.Join(starts, " ") != "10-15 10-29 11-12 11-26" {
		t.Fatalf("occurrences = %v", starts)
	}
	if e, err := s.Get(ctx, boss, wid); err != nil || fmt.Sprint(e.Reminds) != "[15 60]" {
		t.Fatalf("reminds = %v, %v", e.Reminds, err)
	}
	for _, c := range []struct {
		h, m, want int
	}{{8, 55, 0}, {9, 5, 1}, {9, 6, 0}, {9, 40, 0}, {9, 50, 1}, {9, 51, 0}} {
		n, err := s.RemindDue(ctx, time.Date(2026, 10, 29, c.h, c.m, 0, 0, time.Local))
		must(err)
		if n != c.want {
			t.Fatalf("reminders sent at %d:%02d = %d, want %d", c.h, c.m, n, c.want)
		}
	}
	var reminded int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'event_remind' AND event_id = $1`, wid).Scan(&reminded))
	if reminded != 4 { // the organizer and Анна, twice
		t.Fatalf("remind notifications = %d", reminded)
	}
	// absences and birthdays are in everyone's calendar
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', '2026-10-19', '2026-10-23', $1)`, anna)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET birthday = '1990-10-21' WHERE id = $1`, boss)
	must(err)
	list, err = s.Range(ctx, stranger, time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local), time.Date(2026, 10, 22, 0, 0, 0, 0, time.Local))
	must(err)
	got := map[string]int{}
	for _, e := range list {
		got[e.Kind]++
	}
	if got["absence"] != 1 || got["birthday"] != 1 {
		t.Fatalf("absence and birthday in the calendar: %v", got)
	}
	must(s.Delete(ctx, boss, id))
	if err := s.Delete(ctx, anna, id); err != ErrNotFound {
		t.Fatalf("deleted twice: %v", err)
	}
}

// TestCollabCalendar: a collab's calendar shows its meetings and task deadlines only, and nobody outside the collab
// sees them — nor can a member invite someone from outside into a collab meeting.
func TestCollabCalendar(t *testing.T) {
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
	user := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, guest) VALUES ($1, $1, $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, client, outsider, otherGuest := user("owner", false), user("client", true), user("outsider", false), user("other-guest", true)
	ts := tasks.NewStore(pool)
	key, err := ts.CreateCollab(ctx, owner, "Клиент", "", []string{client})
	must(err)
	c, _ := ts.CollabByKey(ctx, owner, key)
	okey, err := ts.CreateCollab(ctx, outsider, "Другой", "", []string{otherGuest})
	must(err)
	_ = okey
	s := NewStore(pool)
	day := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	month0, month1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)

	if _, err := s.Save(ctx, owner, Event{Title: "Созвон", Start: day, End: day.Add(time.Hour), ProjectID: c.ID}, []string{client, outsider}); err == nil {
		t.Fatal("someone from outside was invited into a collab meeting")
	}
	id, err := s.Save(ctx, owner, Event{Title: "Созвон с клиентом", Start: day, End: day.Add(time.Hour), ProjectID: c.ID}, []string{client})
	must(err)
	_, err = s.Save(ctx, owner, Event{Title: "Личное", Start: day, End: day.Add(time.Hour)}, nil)
	must(err)
	_, err = s.Save(ctx, owner, Event{Title: "Общее", Start: day, End: day.Add(time.Hour), Company: true}, nil)
	must(err)
	if _, err := s.Save(ctx, outsider, Event{Title: "x", Start: day, End: day.Add(time.Hour), ProjectID: c.ID}, nil); err == nil {
		t.Fatal("an outsider put a meeting into the collab")
	}
	deadline := day.Add(48 * time.Hour)
	_, err = ts.CreateTask(ctx, owner, c, tasks.NewTask{Title: "Сдать макет", ResponsibleID: owner, Deadline: &deadline})
	must(err)

	list, err := s.ProjectRange(ctx, client, c.ID, month0, month1)
	must(err)
	if len(list) != 2 || list[0].Title != "Созвон с клиентом" || list[1].Kind != "task" || list[1].Title != "Сдать макет" {
		t.Fatalf("the collab calendar = %+v", list)
	}
	for name, u := range map[string]string{"outsider": outsider, "guest of another collab": otherGuest} {
		if _, err := s.Get(ctx, u, id); err == nil {
			t.Errorf("%s opened the collab meeting", name)
		}
		if list, err := s.ProjectRange(ctx, u, c.ID, month0, month1); err != ErrNotFound || len(list) != 0 {
			t.Errorf("%s sees the collab calendar: %+v", name, list)
		}
		all, err := s.Range(ctx, u, month0, month1)
		must(err)
		for _, e := range all {
			if e.ProjectID == c.ID || strings.Contains(e.Title, "клиент") || strings.Contains(e.Title, "макет") {
				t.Errorf("%s sees %+v in the calendar", name, e)
			}
		}
	}
}
