package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalendarAPI: meetings through REST API v1 — create, list (occurrences of a repeated meeting), change only the
// fields given, answer, the planner with the nearest free time, delete; rights by key scope and by organizer.
func TestCalendarAPI(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	user := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	boss, anna := user("Борис"), user("Анна")
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	bossKey, annaKey, readOnly := key(boss, "calendar:read", "calendar:write"), key(anna, "calendar:read", "calendar:write"), key(anna, "calendar:read")
	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	call := func(method, path, token string, body any, want int, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	// next Monday 10:00: inside the working hours
	now := time.Now()
	mon := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local)
	mon = mon.AddDate(0, 0, 7-(int(mon.Weekday())+6)%7)

	var e apiEvent
	call("POST", "/api/v1/calendar/events", readOnly, map[string]any{"title": "x", "start": mon}, http.StatusForbidden, nil)
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Планёрка", "start": mon, "repeat": "daily",
		"until": mon.AddDate(0, 0, 2).Format("2006-01-02"), "attendees": []string{anna}, "remind_before": 15}, http.StatusCreated, &e)
	if e.ID == "" || e.Owner.ID != boss || !e.End.Equal(mon.Add(time.Hour)) || len(e.Attendees) != 1 || e.Attendees[0].Status != "invited" ||
		e.Accessibility != "busy" || *e.RemindBefore != 15 {
		t.Fatalf("created = %+v", e)
	}
	id := e.ID
	var v apiEvent
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Созвон", "start": mon.AddDate(0, 0, 3), "video": true}, http.StatusCreated, &v)
	if !v.Video || v.Join != "/call?event="+v.ID {
		t.Fatalf("a video meeting = %+v", v)
	}
	// a colour and «Важная встреча» (GO24DEV-422): kept by a patch of other fields, a bad colour refused
	call("PATCH", "/api/v1/calendar/events/"+v.ID, bossKey, map[string]any{"color": "#DE2B24", "important": true}, http.StatusOK, &v)
	call("PATCH", "/api/v1/calendar/events/"+v.ID, bossKey, map[string]any{"title": "Созвон 2"}, http.StatusOK, &v)
	if v.Color != "#de2b24" || !v.Important {
		t.Fatalf("a coloured important meeting = %+v", v)
	}
	call("PATCH", "/api/v1/calendar/events/"+v.ID, bossKey, map[string]any{"color": "red;x"}, http.StatusUnprocessableEntity, nil)
	// several reminders (GO24DEV-424): "reminds" sets them all, remind_before is the earliest; [] — none
	call("PATCH", "/api/v1/calendar/events/"+v.ID, bossKey, map[string]any{"reminds": []int{1440, 15, 15}}, http.StatusOK, &v)
	if fmt.Sprint(v.Reminds) != "[15 1440]" || *v.RemindBefore != 1440 {
		t.Fatalf("reminds = %v, remind_before = %v", v.Reminds, v.RemindBefore)
	}
	var none apiEvent
	call("PATCH", "/api/v1/calendar/events/"+v.ID, bossKey, map[string]any{"reminds": []int{}}, http.StatusOK, &none)
	if none.Reminds != nil || none.RemindBefore != nil {
		t.Fatalf("no reminders = %v", none.Reminds)
	}
	ics, err := srv.Calendar.SetFeed(ctx, boss, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if feed, err := srv.Calendar.FeedICS(ctx, ics.Token, now); err != nil || !strings.Contains(feed, "PRIORITY:1") || !strings.Contains(feed, "COLOR:crimson") {
		t.Fatalf("the feed of an important meeting: %v\n%s", err, feed)
	}
	call("DELETE", "/api/v1/calendar/events/"+v.ID, bossKey, nil, http.StatusNoContent, nil)
	var list []apiEvent
	call("GET", "/api/v1/calendar/events?from="+mon.Format("2006-01-02")+"&to="+mon.AddDate(0, 0, 7).Format("2006-01-02"), annaKey, nil, http.StatusOK, &list)
	if len(list) != 3 || list[2].MyStatus != "invited" || !list[2].Start.Equal(mon.AddDate(0, 0, 2)) || !list[2].SeriesStart.Equal(mon) {
		t.Fatalf("anna's week = %+v", list)
	}
	call("GET", "/api/v1/calendar/events?from=2026-01-01&to=2028-01-01", annaKey, nil, http.StatusUnprocessableEntity, nil)
	call("PATCH", "/api/v1/calendar/events/"+id, annaKey, map[string]any{"title": "Моя"}, http.StatusForbidden, nil)
	call("PATCH", "/api/v1/calendar/events/"+id, bossKey, map[string]any{"where": "Переговорная"}, http.StatusOK, &e)
	if e.Title != "Планёрка" || e.Where != "Переговорная" || e.Repeat != "daily" || len(e.Attendees) != 1 || !e.Start.Equal(mon) {
		t.Fatalf("a patch changed what was not given: %+v", e)
	}
	// the search: anna's invitation, found by its place
	call("GET", "/api/v1/calendar/search?preset=invites", annaKey, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != id || list[0].MyStatus != "invited" {
		t.Fatalf("anna's invitations = %+v", list)
	}
	call("GET", "/api/v1/calendar/search?q="+url.QueryEscape("переговорная"), annaKey, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("found by the place = %+v", list)
	}
	call("GET", "/api/v1/calendar/search?preset=organizer", annaKey, nil, http.StatusOK, &list)
	if len(list) != 0 {
		t.Fatalf("anna organizes = %+v", list)
	}
	call("GET", "/api/v1/calendar/search?preset=x", annaKey, nil, http.StatusNotFound, nil)
	call("POST", "/api/v1/calendar/events/"+id+"/respond", annaKey, map[string]any{"status": "accepted"}, http.StatusOK, &e)
	if e.MyStatus != "accepted" {
		t.Fatalf("after the answer = %+v", e)
	}
	// the planner: anna is busy 10–11 every day; the nearest free hour from 10:00 is 11:00
	var busy struct {
		People []struct {
			ID   string `json:"id"`
			Busy []struct {
				Start time.Time `json:"start"`
				Kind  string    `json:"kind"`
				Title string    `json:"title"`
			} `json:"busy"`
		} `json:"people"`
		Free *time.Time `json:"free"`
	}
	call("GET", "/api/v1/calendar/busy?user="+anna+"&from="+mon.Format(time.RFC3339)+"&to="+mon.AddDate(0, 0, 1).Format(time.RFC3339)+"&length=60",
		bossKey, nil, http.StatusOK, &busy)
	if len(busy.People) != 1 || len(busy.People[0].Busy) != 1 || busy.People[0].Busy[0].Title != "Планёрка" || busy.Free == nil || !busy.Free.Equal(mon.Add(time.Hour)) {
		t.Fatalf("busy = %+v", busy)
	}
	// the second day alone: an hour later; the third deleted alone
	tue := url.QueryEscape(mon.AddDate(0, 0, 1).Format(time.RFC3339))
	e = apiEvent{}
	call("PATCH", "/api/v1/calendar/events/"+id+"?at="+tue+"&scope=one", bossKey, map[string]any{"start": mon.AddDate(0, 0, 1).Add(time.Hour)}, http.StatusOK, &e)
	if e.ID == id || e.Repeat != "" || !e.Start.Equal(mon.AddDate(0, 0, 1).Add(time.Hour)) || !e.End.Equal(mon.AddDate(0, 0, 1).Add(2*time.Hour)) || e.Title != "Планёрка" {
		t.Fatalf("the occurrence taken out = %+v", e)
	}
	call("DELETE", "/api/v1/calendar/events/"+id+"?at="+url.QueryEscape(mon.AddDate(0, 0, 2).Format(time.RFC3339)), bossKey, nil, http.StatusNoContent, nil)
	list = nil
	call("GET", "/api/v1/calendar/events?from="+mon.Format("2006-01-02")+"&to="+mon.AddDate(0, 0, 7).Format("2006-01-02"), annaKey, nil, http.StatusOK, &list)
	if len(list) != 2 || !list[0].Start.Equal(mon) || !list[1].Start.Equal(mon.AddDate(0, 0, 1).Add(time.Hour)) {
		t.Fatalf("anna's week after the changes = %+v", list)
	}
	call("DELETE", "/api/v1/calendar/events/"+id, annaKey, nil, http.StatusNotFound, nil)
	call("DELETE", "/api/v1/calendar/events/"+id, bossKey, nil, http.StatusNoContent, nil)
	call("GET", "/api/v1/calendar/events/"+id, annaKey, nil, http.StatusNotFound, nil)

	// one's own calendars (GO24DEV-423): a meeting in one is drawn with its colour for the organizer, for the
	// invited it is in their main calendar; nobody else puts meetings there; deleted with its meetings
	var sec apiSection
	call("POST", "/api/v1/calendar/sections", bossKey, map[string]any{"name": " "}, http.StatusUnprocessableEntity, nil)
	call("POST", "/api/v1/calendar/sections", bossKey, map[string]any{"name": "Рабочий"}, http.StatusCreated, &sec)
	if sec.ID == "" || sec.Color == "" {
		t.Fatalf("a new calendar = %+v", sec)
	}
	call("PATCH", "/api/v1/calendar/sections/"+sec.ID, bossKey, map[string]any{"color": "#E97090"}, http.StatusOK, &sec)
	call("PATCH", "/api/v1/calendar/sections/"+sec.ID, annaKey, map[string]any{"name": "Чужой"}, http.StatusNotFound, nil)
	call("PATCH", "/api/v1/calendar/sections/main", annaKey, map[string]any{"color": "#0092cc"}, http.StatusOK, nil)
	var secs []apiSection
	call("GET", "/api/v1/calendar/sections", bossKey, nil, http.StatusOK, &secs)
	if len(secs) != 2 || !secs[0].Main || secs[1].Name != "Рабочий" || secs[1].Color != "#e97090" {
		t.Fatalf("boss's calendars = %+v", secs)
	}
	call("POST", "/api/v1/calendar/events", annaKey, map[string]any{"title": "x", "start": mon, "calendar": sec.ID}, http.StatusUnprocessableEntity, nil)
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Отчёт", "start": mon, "calendar": sec.ID, "attendees": []string{anna}},
		http.StatusCreated, &e)
	if e.Calendar != sec.ID {
		t.Fatalf("a meeting in a calendar = %+v", e)
	}
	call("PATCH", "/api/v1/calendar/events/"+e.ID, bossKey, map[string]any{"title": "Отчёт 2"}, http.StatusOK, &e)
	if e.Calendar != sec.ID {
		t.Fatalf("a patch took the meeting out of its calendar: %+v", e)
	}
	var seen apiEvent
	call("GET", "/api/v1/calendar/events/"+e.ID, annaKey, nil, http.StatusOK, &seen)
	if seen.Calendar != "" {
		t.Fatalf("the invited see the organizer's calendar: %+v", seen)
	}
	for who, want := range map[string]string{boss: "#e97090", anna: "#0092cc"} {
		got, err := srv.Calendar.Range(ctx, who, mon, mon.AddDate(0, 0, 1))
		if err != nil || len(got) != 1 || got[0].Paint() != want {
			t.Fatalf("the colour for %s: %v %+v", who, err, got)
		}
	}
	call("DELETE", "/api/v1/calendar/sections/"+sec.ID, annaKey, nil, http.StatusNotFound, nil)
	call("DELETE", "/api/v1/calendar/sections/"+sec.ID, bossKey, nil, http.StatusNoContent, nil)
	call("GET", "/api/v1/calendar/events/"+e.ID, bossKey, nil, http.StatusNotFound, nil)

	// calendars of colleagues (GO24DEV-516): Anna opens Boris's calendar — the busy time only, until he shows his
	// meetings; a private one is always only busy, one she is in — in full; added to her panel, his meetings are over
	// her calendar in his colour, a guest has none of it
	day := mon.AddDate(0, 0, 1)
	var open, closed, both apiEvent
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Бюджет", "where": "Каб. 5", "start": day.Add(2 * time.Hour)}, http.StatusCreated, &open)
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Врач", "start": day.Add(5 * time.Hour), "private": true}, http.StatusCreated, &closed)
	call("POST", "/api/v1/calendar/events", bossKey, map[string]any{"title": "Вместе", "start": day.Add(6 * time.Hour), "attendees": []string{anna}}, http.StatusCreated, &both)
	span := "&from=" + day.Format("2006-01-02") + "&to=" + day.AddDate(0, 0, 1).Format("2006-01-02")
	theirs := func(want map[string]string) {
		t.Helper()
		var got []apiEvent
		call("GET", "/api/v1/calendar/events?user="+boss+span, annaKey, nil, http.StatusOK, &got)
		seen := map[string]string{}
		for _, x := range got {
			if x.Of != boss {
				t.Fatalf("a meeting of Boris's calendar is not his: %+v", x)
			}
			if x.Hidden != (x.Title == "") || x.Hidden && (x.Where != "" || x.Attendees != nil) {
				t.Fatalf("a busy time shows the meeting: %+v", x)
			}
			seen[x.ID] = x.Title
		}
		for id, title := range want {
			if v, ok := seen[id]; !ok || v != title {
				t.Fatalf("Boris's calendar for Anna: %v, want %v", seen, want)
			}
		}
	}
	theirs(map[string]string{open.ID: "", closed.ID: "", both.ID: "Вместе"})
	call("GET", "/api/v1/calendar/events/"+open.ID, annaKey, nil, http.StatusNotFound, nil)
	var share map[string]string
	call("GET", "/api/v1/calendar/share", bossKey, nil, http.StatusOK, &share)
	if share["share"] != "busy" {
		t.Fatalf("the default share: %v", share)
	}
	call("PUT", "/api/v1/calendar/share", bossKey, map[string]any{"share": "all"}, http.StatusUnprocessableEntity, nil)
	call("PUT", "/api/v1/calendar/share", readOnly, map[string]any{"share": "full"}, http.StatusForbidden, nil)
	call("PUT", "/api/v1/calendar/share", bossKey, map[string]any{"share": "full"}, http.StatusOK, &share)
	theirs(map[string]string{open.ID: "Бюджет", closed.ID: "", both.ID: "Вместе"})
	call("GET", "/api/v1/calendar/events/"+open.ID, annaKey, nil, http.StatusOK, nil)
	call("GET", "/api/v1/calendar/events/"+closed.ID, annaKey, nil, http.StatusNotFound, nil)
	call("PATCH", "/api/v1/calendar/events/"+open.ID, annaKey, map[string]any{"title": "x"}, http.StatusForbidden, nil)

	call("POST", "/api/v1/calendar/people", annaKey, map[string]any{"user": anna}, http.StatusNotFound, nil)
	var pers apiPerson
	call("POST", "/api/v1/calendar/people", annaKey, map[string]any{"user": boss}, http.StatusCreated, &pers)
	if pers.Name != "Борис" || pers.Color == "" {
		t.Fatalf("the colleague's calendar: %+v", pers)
	}
	call("POST", "/api/v1/calendar/people", annaKey, map[string]any{"user": boss, "color": "#00b38c"}, http.StatusCreated, &pers)
	var people []apiPerson
	call("GET", "/api/v1/calendar/people", annaKey, nil, http.StatusOK, &people)
	if len(people) != 1 || people[0].Color != "#00b38c" {
		t.Fatalf("Anna's colleagues: %+v", people)
	}
	mine, err := srv.Calendar.Range(ctx, anna, day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if mine, err = srv.Calendar.WithPeople(ctx, anna, mine, day, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, x := range mine {
		n[x.ID]++
		if x.ID == open.ID && (x.Of != boss || x.Paint() != "#00b38c") {
			t.Fatalf("Boris's meeting over Anna's calendar: %+v", x)
		}
	}
	if n[open.ID] != 1 || n[closed.ID] != 1 || n[both.ID] != 1 {
		t.Fatalf("Anna's calendar with Boris's: %v", n)
	}

	guest := user("Гость")
	if _, err := pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest); err != nil {
		t.Fatal(err)
	}
	guestKey := key(guest, "calendar:read", "calendar:write")
	call("GET", "/api/v1/calendar/events?user="+boss+span, guestKey, nil, http.StatusNotFound, nil)
	call("GET", "/api/v1/calendar/events/"+open.ID, guestKey, nil, http.StatusNotFound, nil)
	call("POST", "/api/v1/calendar/people", guestKey, map[string]any{"user": boss}, http.StatusNotFound, nil)
	call("POST", "/api/v1/calendar/people", annaKey, map[string]any{"user": guest}, http.StatusNotFound, nil)

	call("DELETE", "/api/v1/calendar/people/"+boss, annaKey, nil, http.StatusNoContent, nil)
	call("DELETE", "/api/v1/calendar/people/"+boss, annaKey, nil, http.StatusNotFound, nil)

	// «Настройки календаря»: the defaults, PUT changes the given ones, wrong hours are refused
	var cs calendar.Settings
	call("GET", "/api/v1/calendar/settings", annaKey, nil, http.StatusOK, &cs)
	if cs.WeekStart != 1 || cs.WorkFrom != 9 || cs.WorkTo != 18 || len(cs.DaysOff) != 2 || !cs.ShowDeclined || cs.Remind != 15 {
		t.Fatalf("settings = %+v", cs)
	}
	call("PUT", "/api/v1/calendar/settings", annaKey, map[string]any{"week_start": 7, "days_off": []int{5, 6, 5}, "remind": -1}, http.StatusOK, &cs)
	if cs.WeekStart != 7 || cs.WorkFrom != 9 || len(cs.DaysOff) != 2 || cs.DaysOff[0] != 5 || cs.Remind != -1 || !cs.ShowTasks {
		t.Fatalf("settings = %+v", cs)
	}
	call("PUT", "/api/v1/calendar/settings", annaKey, map[string]any{"work_from": 18, "work_to": 9}, http.StatusUnprocessableEntity, nil)
	call("PUT", "/api/v1/calendar/settings", annaKey, map[string]any{"remind": 7}, http.StatusUnprocessableEntity, nil)
	call("PUT", "/api/v1/calendar/settings", readOnly, map[string]any{"remind": 0}, http.StatusForbidden, nil)
}
