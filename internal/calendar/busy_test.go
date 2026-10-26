package calendar

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/worktime"
)

// TestFirstFree: the nearest time all are free — within working hours, past the busy slots, on the next working day.
func TestFirstFree(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.Local) } // 23.10 — Friday
	sched := worktime.Schedule{Start: "09:00", End: "18:00", Weekend: []int{0, 6}}
	people := []Person{
		{Slots: []Slot{{Start: at(23, 9, 0), End: at(23, 10, 30)}, {Start: at(23, 11, 0), End: at(23, 17, 0)}}},
		{Slots: []Slot{{Start: at(23, 10, 15), End: at(23, 10, 45)}}},
	}
	cases := []struct {
		from   time.Time
		length time.Duration
		want   time.Time
	}{
		{at(23, 8, 0), 15 * time.Minute, at(23, 10, 45)},  // between the slots, after the second person's
		{at(23, 8, 0), time.Hour, at(23, 17, 0)},          // the gap is too short: after the long one
		{at(23, 17, 10), time.Hour, at(26, 9, 0)},         // past the end of the day, over the weekend
		{at(23, 17, 7), 30 * time.Minute, at(23, 17, 15)}, // on the quarter-hour grid
	}
	for _, c := range cases {
		got, ok := FirstFree(people, c.from, c.length, sched, 14)
		if !ok || !got.Equal(c.want) {
			t.Errorf("from %v for %v = %v %v, want %v", c.from, c.length, got, ok, c.want)
		}
	}
	if _, ok := FirstFree(people, at(23, 8, 0), 10*time.Hour, sched, 14); ok {
		t.Error("a meeting longer than the working day found a place")
	}
}

// TestBusy: the planner shows a person's meetings (organized, invited, not declined, not «free»), repetitions and
// absences; a private meeting keeps its title from those outside it; the meeting being changed is left out.
func TestBusy(t *testing.T) {
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
	boss, anna, petr, guest := user("boss", false), user("anna", false), user("petr", false), user("guest", true)
	s := NewStore(pool)
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)
	h := func(x int) time.Time { return day.Add(time.Duration(x) * time.Hour) }
	save := func(owner string, e Event, invite ...string) string {
		id, err := s.Save(ctx, owner, e, invite)
		must(err)
		return id
	}
	plan := save(anna, Event{Title: "Планёрка", Start: h(9), End: h(10), Repeat: "daily", Every: 1})
	secret := save(anna, Event{Title: "Собеседование", Start: h(11), End: h(12), Private: true}, petr)
	save(anna, Event{Title: "Обед", Start: h(13), End: h(14), Accessibility: "free"})
	declined := save(boss, Event{Title: "Отклонённая", Start: h(15), End: h(16)}, anna)
	must(s.Respond(ctx, anna, declined, "declined"))
	save(boss, Event{Title: "Без ответа", Start: h(16), End: h(17)}, petr)
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2, $2, $1)`, petr, day.AddDate(0, 0, 1))
	must(err)

	people, err := s.Busy(ctx, boss, []string{anna, petr, anna, guest}, day, day.AddDate(0, 0, 2), "")
	must(err)
	if len(people) != 2 || people[0].ID != anna || people[1].ID != petr {
		t.Fatalf("people = %+v (a guest outside the collabs is unknown, duplicates once)", people)
	}
	var got []string
	for _, sl := range people[0].Slots {
		got = append(got, sl.Start.Format("02 15")+" "+sl.Kind+" "+sl.Title)
	}
	want := []string{"21 09 busy ", "21 11 busy ", "22 09 busy "} // boss sees neither the private title nor anna's own meetings
	if len(got) != len(want) {
		t.Fatalf("anna busy = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("anna busy = %q, want %q", got, want)
		}
	}
	var pk []string
	for _, sl := range people[1].Slots {
		pk = append(pk, sl.Start.Format("02 15")+" "+sl.Kind+" "+sl.Title)
	}
	if len(pk) != 3 || pk[0] != "21 11 tentative " || pk[1] != "21 16 tentative Без ответа" || pk[2] != "22 00 absent " {
		t.Fatalf("petr busy = %q", pk)
	}
	// the invited sees the private meeting
	people, err = s.Busy(ctx, petr, []string{anna}, day, day.AddDate(0, 0, 1), plan)
	must(err)
	if len(people[0].Slots) != 1 || people[0].Slots[0].Title != "Собеседование" || people[0].Slots[0].EventID != secret {
		t.Fatalf("petr sees anna = %+v (the meeting being changed left out)", people[0].Slots)
	}
	// a private meeting in a calendar: details only for its people
	list, err := s.Range(ctx, boss, day, day.AddDate(0, 0, 1))
	must(err)
	for _, e := range list {
		if e.ID == secret {
			t.Fatalf("boss sees anna's private meeting in his calendar: %+v", e)
		}
	}
	e, err := s.Get(ctx, petr, secret)
	must(err)
	if e.Hidden || e.Title != "Собеседование" {
		t.Fatalf("the invited: %+v", e)
	}
	_, err = pool.Exec(ctx, `UPDATE events SET company = true WHERE id = $1`, secret)
	must(err)
	e, err = s.Get(ctx, boss, secret)
	must(err)
	if !e.Hidden || e.Title != "" || e.Description != "" || e.Attendees != nil {
		t.Fatalf("a private company meeting shown to others: %+v", e)
	}
	if _, err := s.Save(ctx, anna, Event{Title: "x", Start: h(9), End: h(10), Accessibility: "away"}, nil); err == nil {
		t.Fatal("an unknown accessibility was saved")
	}
}

// TestOccurrences: an occurrence of a repeated meeting changed or deleted alone, with the following, or the whole
// series; a changed one remembers the series and goes away with it.
func TestOccurrences(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('boss', 'boss') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'anna') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	d := func(n, h, m int) time.Time { return time.Date(2026, 10, 19+n, h, m, 0, 0, time.Local) } // 19.10 — Monday
	until := d(4, 0, 0)
	id, err := s.Save(ctx, boss, Event{Title: "Планёрка", Start: d(0, 10, 0), End: d(0, 11, 0), Repeat: "daily", Every: 1, Until: &until}, []string{anna})
	must(err)
	week := func() (out []string) {
		t.Helper()
		list, err := s.Range(ctx, anna, d(0, 0, 0), d(7, 0, 0))
		must(err)
		for _, e := range list {
			out = append(out, e.Start.Format("02 15:04 ")+e.Title)
		}
		return
	}
	same := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("week = %q, want %q", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("week = %q, want %q", got, want)
			}
		}
	}
	same(week(), "19 10:00 Планёрка", "20 10:00 Планёрка", "21 10:00 Планёрка", "22 10:00 Планёрка", "23 10:00 Планёрка")

	if _, err := s.SaveOccurrence(ctx, boss, id, d(1, 10, 30), ScopeOne, Event{Title: "x", Start: d(1, 12, 0), End: d(1, 13, 0)}, nil); err != ErrNotFound {
		t.Fatalf("no occurrence at 10:30: %v", err)
	}
	if _, err := s.SaveOccurrence(ctx, anna, id, d(1, 10, 0), ScopeOne, Event{Title: "x", Start: d(1, 12, 0), End: d(1, 13, 0)}, nil); err != ErrForbidden {
		t.Fatalf("an invited person changed the series: %v", err)
	}
	// Tuesday alone: moved to 12:00 and renamed
	one, err := s.SaveOccurrence(ctx, boss, id, d(1, 10, 0), ScopeOne, Event{Title: "Планёрка с клиентом", Start: d(1, 12, 0), End: d(1, 13, 0)}, []string{anna})
	must(err)
	e, err := s.Get(ctx, anna, one)
	must(err)
	if e.SeriesID != id || e.SeriesAt == nil || !e.SeriesAt.Equal(d(1, 10, 0)) || e.Repeat != "" || len(e.Attendees) != 1 {
		t.Fatalf("the occurrence taken out = %+v", e)
	}
	// Wednesday deleted alone
	must(s.DeleteOccurrence(ctx, boss, id, d(2, 10, 0), ScopeOne))
	same(week(), "19 10:00 Планёрка", "20 12:00 Планёрка с клиентом", "22 10:00 Планёрка", "23 10:00 Планёрка")
	// from Thursday on: a new name
	next, err := s.SaveOccurrence(ctx, boss, id, d(3, 10, 0), ScopeFollowing,
		Event{Title: "Стендап", Start: d(3, 9, 30), End: d(3, 10, 0), Repeat: "daily", Every: 1, Until: &until}, []string{anna})
	must(err)
	same(week(), "19 10:00 Планёрка", "20 12:00 Планёрка с клиентом", "22 09:30 Стендап", "23 09:30 Стендап")
	// the whole first series later by half an hour, from its first occurrence
	wed := d(2, 0, 0) // the series now ends on Wednesday
	_, err = s.SaveOccurrence(ctx, boss, id, d(0, 10, 0), ScopeAll, Event{Title: "Планёрка", Start: d(0, 10, 30), End: d(0, 11, 30), Repeat: "daily", Every: 1, Until: &wed}, []string{anna})
	must(err)
	if e, _ := s.Get(ctx, boss, id); !e.Start.Equal(d(0, 10, 30)) {
		t.Fatalf("the series = %+v", e)
	}
	// the following of the new series from Friday: deleted
	must(s.DeleteOccurrence(ctx, boss, next, d(4, 9, 30), ScopeFollowing))
	same(week(), "19 10:30 Планёрка", "20 12:00 Планёрка с клиентом", "22 09:30 Стендап")
	// the whole first series: its changed occurrence goes too
	must(s.DeleteOccurrence(ctx, boss, id, d(0, 10, 30), ScopeAll))
	same(week(), "22 09:30 Стендап")
	must(s.DeleteOccurrence(ctx, boss, next, d(3, 9, 30), ScopeAll))

	// weekly on Mon, Wed, Fri, six meetings (GO24DEV-682): kept with its days and count, the end from the count
	weeks := func(from, n int) (out []string) {
		t.Helper()
		list, err := s.Range(ctx, anna, d(from, 0, 0), d(from+7*n, 0, 0))
		must(err)
		for _, e := range list {
			out = append(out, e.Start.Format("02.01 15:04"))
		}
		return
	}
	yoga, err := s.Save(ctx, boss, Event{Title: "Йога", Start: d(0, 18, 0), End: d(0, 19, 0), Repeat: "weekly", Days: []int{3, 5}, Count: 6}, []string{anna})
	must(err)
	e, err = s.Get(ctx, anna, yoga)
	must(err)
	if len(e.Days) != 3 || e.Count != 6 || e.Until == nil || e.Until.Format("02.01") != "30.10" {
		t.Fatalf("yoga = %+v", e)
	}
	same(weeks(0, 3), "19.10 18:00", "21.10 18:00", "23.10 18:00", "26.10 18:00", "28.10 18:00", "30.10 18:00")
	// from Wednesday of the second week on at 19:00: the new series ends where the old one did
	later, err := s.SaveOccurrence(ctx, boss, yoga, d(9, 18, 0), ScopeFollowing,
		Event{Title: "Йога", Start: d(9, 19, 0), End: d(9, 20, 0), Repeat: "weekly", Days: e.Days, Count: 6}, []string{anna})
	must(err)
	same(weeks(0, 3), "19.10 18:00", "21.10 18:00", "23.10 18:00", "26.10 18:00", "28.10 19:00", "30.10 19:00")
	if o, _ := s.Get(ctx, boss, yoga); o.Count != 0 || o.Until.Format("02.01") != "27.10" {
		t.Fatalf("the series before = %+v", o)
	}
	// the whole first series a day later: its days move with it
	tue := d(8, 0, 0)
	_, err = s.SaveOccurrence(ctx, boss, yoga, d(0, 18, 0), ScopeAll,
		Event{Title: "Йога", Start: d(1, 18, 0), End: d(1, 19, 0), Repeat: "weekly", Days: []int{1, 3, 5}, Until: &tue}, []string{anna})
	must(err)
	if o, _ := s.Get(ctx, boss, yoga); len(o.Days) != 3 || o.Days[0] != 2 || o.Days[2] != 6 {
		t.Fatalf("the series moved = %+v", o.Days)
	}
	must(s.DeleteOccurrence(ctx, boss, later, d(9, 19, 0), ScopeAll))
	same(weeks(0, 1), "20.10 18:00", "22.10 18:00", "24.10 18:00")
}

// TestRooms: a meeting room is never booked twice at the same time — single meetings and every occurrence of a
// repeated one; an occurrence taken out of its series keeps the room; a deleted room leaves its meetings.
func TestRooms(t *testing.T) {
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
	var boss, anna, guest string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('boss', 'boss') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'anna') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, guest) VALUES ('guest', 'guest', true) RETURNING id`).Scan(&guest))
	s := NewStore(pool)
	if _, err := s.SaveRoom(ctx, Room{Name: "  "}); err == nil {
		t.Fatal("a room without a name")
	}
	room, err := s.SaveRoom(ctx, Room{Name: "Большая", Capacity: 12, Location: "3 этаж"})
	must(err)
	d := func(n, h, m int) time.Time { return time.Date(2026, 10, 19+n, h, m, 0, 0, time.Local) }
	busy := func(err error) bool { return err != nil && strings.Contains(err.Error(), "room_busy") }
	_, err = s.Save(ctx, boss, Event{Title: "Совет", Start: d(0, 10, 0), End: d(0, 11, 0), RoomID: room}, nil)
	must(err)
	if _, err := s.Save(ctx, anna, Event{Title: "Встреча", Start: d(0, 10, 30), End: d(0, 11, 30), RoomID: room}, nil); !busy(err) {
		t.Fatalf("booked twice: %v", err)
	}
	_, err = s.Save(ctx, anna, Event{Title: "Встреча", Start: d(0, 11, 0), End: d(0, 12, 0), RoomID: room}, nil)
	must(err)
	until := d(3, 0, 0)
	series, err := s.Save(ctx, boss, Event{Title: "Стендап", Start: d(1, 9, 0), End: d(1, 9, 30), Repeat: "daily", Every: 1, Until: &until, RoomID: room}, nil)
	must(err)
	if _, err := s.Save(ctx, anna, Event{Title: "Звонок", Start: d(2, 9, 15), End: d(2, 9, 45), RoomID: room}, nil); !busy(err) {
		t.Fatalf("an occurrence of the series not counted: %v", err)
	}
	if _, err := s.Save(ctx, anna, Event{Title: "Утро", Start: d(4, 9, 0), End: d(4, 9, 30), RoomID: room}, nil); err != nil {
		t.Fatalf("after the series ended: %v", err)
	}
	// Wednesday's occurrence alone a quarter later: it does not clash with itself
	one, err := s.SaveOccurrence(ctx, boss, series, d(2, 9, 0), ScopeOne, Event{Title: "Стендап", Start: d(2, 9, 15), End: d(2, 9, 45), RoomID: room}, nil)
	must(err)
	if e, _ := s.Get(ctx, boss, one); e.RoomName != "Большая" {
		t.Fatalf("the occurrence = %+v", e)
	}
	// a series is checked against the meetings to come
	if _, err := s.Save(ctx, anna, Event{Title: "Йога", Start: d(-5, 9, 20), End: d(-5, 9, 40), Repeat: "weekly", Every: 1, RoomID: room}, nil); !busy(err) {
		t.Fatalf("a weekly series over Wednesday's occurrence: %v", err)
	}
	_, slots, err := s.RoomBusy(ctx, anna, room, d(0, 0, 0), d(1, 0, 0))
	must(err)
	if len(slots) != 2 || slots[0].Title != "" && slots[0].Title != "Совет" {
		t.Fatalf("the room's Monday = %+v", slots)
	}
	if _, err := s.Save(ctx, guest, Event{Title: "Гость", Start: d(5, 9, 0), End: d(5, 10, 0), RoomID: room}, nil); err == nil {
		t.Fatal("a guest booked a room of the company")
	}
	must(s.DeleteRoom(ctx, room))
	if e, err := s.Get(ctx, boss, one); err != nil || e.RoomID != "" || e.Title != "Стендап" {
		t.Fatalf("after the room was deleted: %+v %v", e, err)
	}
}

// TestFeed: the subscription link — the person's meetings with the repetition rule, exceptions and reminders, a
// private meeting of someone else without its title, the deadlines of tasks by choice; a new link stops the old one.
func TestFeed(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('boss', 'Борис') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	now := time.Now()
	st := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local).AddDate(0, 0, 1)
	until := st.AddDate(0, 0, 9)
	series, err := s.Save(ctx, boss, Event{Title: "Планёрка; утро", Start: st, End: st.Add(time.Hour), Repeat: "daily", Every: 2,
		Until: &until, Reminds: []int{10, 1440}}, []string{anna})
	must(err)
	must(s.DeleteOccurrence(ctx, boss, series, st.AddDate(0, 0, 2), ScopeOne))
	_, err = s.Save(ctx, boss, Event{Title: "Тайное", Start: st, End: st.Add(time.Hour), Private: true, Company: true}, nil)
	must(err)
	_, err = s.Save(ctx, boss, Event{Title: "Чужое", Start: st, End: st.Add(time.Hour)}, nil)
	must(err)

	if _, err := s.FeedICS(ctx, "nope", now); err != ErrNotFound {
		t.Fatalf("an unknown token: %v", err)
	}
	f, err := s.SetFeed(ctx, anna, false, false)
	must(err)
	ics, err := s.FeedICS(ctx, f.Token, now)
	must(err)
	for _, want := range []string{"BEGIN:VCALENDAR\r\n", "X-WR-CALNAME:go24 · Анна", `SUMMARY:Планёрка\; утро`, "RRULE:FREQ=DAILY;INTERVAL=2;UNTIL=",
		"EXDATE:" + st.AddDate(0, 0, 2).UTC().Format("20060102T150405Z"), "TRIGGER:-PT10M", "TRIGGER:-PT1440M", "STATUS:TENTATIVE", "SUMMARY:—\r\nCLASS:PRIVATE", "END:VCALENDAR\r\n"} {
		if !strings.Contains(ics, want) {
			t.Errorf("the feed lacks %q:\n%s", want, ics)
		}
	}
	if strings.Contains(ics, "Чужое") || strings.Contains(ics, "Тайное") {
		t.Errorf("the feed shows what anna does not see:\n%s", ics)
	}
	again, err := s.SetFeed(ctx, anna, false, true)
	must(err)
	if again.Token != f.Token || !again.Tasks {
		t.Fatalf("saving the choice changed the link: %+v", again)
	}
	renewed, err := s.SetFeed(ctx, anna, true, true)
	must(err)
	if renewed.Token == f.Token {
		t.Fatal("the link was not renewed")
	}
	if _, err := s.FeedICS(ctx, f.Token, now); err != ErrNotFound {
		t.Fatalf("the old link still works: %v", err)
	}
}
