package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRestPortalCalendar: calendar.section.* and calendar.event.* by numbers — a meeting with people, repetition and
// reminders gives them, the answers of the invited, an occurrence changed, the planner, a project's calendar.
func TestRestPortalCalendar(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, ivan string
	var annaNum, ivanNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна Петрова', 'anna@go24.test')
		RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id, num`).Scan(&ivan, &ivanNum))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CAL", "Запуск", "")
	must(err)
	var projNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM projects WHERE key = 'CAL'`).Scan(&projNum))

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		I18n: bundle, BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path string, v url.Values, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", path, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hookOf := func(id string, n int64) string {
		token, err := srv.Keys.Create(ctx, id, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	hook, ivanHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum)
	form := func(hook, method string, v url.Values) any { t.Helper(); return call(hook+method, v, 200)["result"] }
	str := func(v any) string { return portalStr(v) }
	obj := func(v any) map[string]any { return v.(map[string]any) }

	// sections: the main calendar, one's own added, renamed; a project's
	secs := form(hook, "calendar.section.get", url.Values{"type": {"user"}, "ownerId": {num(annaNum)}}).([]any)
	if len(secs) != 1 || obj(secs[0])["NAME"] != "Мой календарь" || obj(secs[0])["CAL_TYPE"] != "user" {
		t.Fatalf("calendar.section.get: %v", secs)
	}
	main := str(obj(secs[0])["ID"])
	work := str(form(hook, "calendar.section.add", url.Values{"type": {"user"}, "ownerId": {num(annaNum)}, "name": {"Рабочий"},
		"color": {"#9dcf00"}}))
	if form(hook, "calendar.section.update", url.Values{"id": {work}, "name": {"Работа"}}) != work {
		t.Error("calendar.section.update")
	}
	secs = form(hook, "calendar.section.get", nil).([]any)
	if len(secs) != 2 || str(obj(secs[1])["ID"]) != work || obj(secs[1])["NAME"] != "Работа" || obj(secs[1])["COLOR"] != "#9dcf00" {
		t.Fatalf("calendar.section.get: %v", secs)
	}
	if again := form(hook, "calendar.section.get", nil).([]any); str(obj(again[0])["ID"]) != main {
		t.Error("the number of a section changed")
	}
	if s := form(ivanHook, "calendar.section.get", url.Values{"type": {"user"}, "ownerId": {num(annaNum)}}).([]any); len(s) != 1 ||
		obj(obj(s[0])["PERM"])["add"] != false {
		t.Errorf("someone else's calendar: %v", s)
	}

	// a weekly meeting with Ivan in «Работа»: Mondays and Wednesdays, 4 times, a reminder a day before
	day := time.Now().AddDate(0, 0, 7)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, 1)
	}
	d := day.Format("2006-01-02")
	id := str(form(hook, "calendar.event.add", url.Values{"type": {"user"}, "ownerId": {num(annaNum)}, "section": {work},
		"name": {"Планёрка"}, "from": {d + " 10:00"}, "to": {d + " 11:00"}, "location": {"Переговорная"}, "importance": {"high"},
		"attendees[]": {num(annaNum), num(ivanNum)}, "is_meeting": {"Y"}, "rrule[FREQ]": {"WEEKLY"}, "rrule[COUNT]": {"4"},
		"rrule[BYDAY][]": {"MO", "WE"}, "remind[0][type]": {"day"}, "remind[0][count]": {"1"}}))
	e := obj(form(hook, "calendar.event.getbyid", url.Values{"id": {id}}))
	if e["NAME"] != "Планёрка" || e["SECT_ID"] != work || e["DATE_FROM"] != day.Format("02.01.2006")+" 10:00:00" ||
		e["IMPORTANCE"] != "high" || e["IS_MEETING"] != true || e["MEETING_STATUS"] != "H" || e["MEETING_HOST"] != num(annaNum) ||
		obj(e["RRULE"])["FREQ"] != "WEEKLY" || obj(obj(e["RRULE"])["BYDAY"])["WE"] != "WE" ||
		obj(e["REMIND"].([]any)[0])["count"] != float64(1440) || e["DT_LENGTH"] != float64(3600) {
		t.Fatalf("calendar.event.getbyid: %v", e)
	}
	if codes := e["ATTENDEES_CODES"].([]any); len(codes) != 2 || codes[1] != "U"+num(ivanNum) {
		t.Errorf("ATTENDEES_CODES: %v", codes)
	}
	// four occurrences in Anna's calendar, of the section «Работа» only; in Ivan's — in his main calendar
	list := form(hook, "calendar.event.get", url.Values{"from": {d}, "to": {day.AddDate(0, 0, 14).Format("2006-01-02")},
		"section[]": {work}}).([]any)
	if len(list) != 4 || str(obj(list[1])["ID"]) != id {
		t.Fatalf("calendar.event.get: %v", list)
	}
	if l := form(hook, "calendar.event.get", url.Values{"from": {d}, "to": {day.AddDate(0, 0, 14).Format("2006-01-02")},
		"section[]": {main}}).([]any); len(l) != 0 {
		t.Errorf("the main section: %v", l)
	}
	ivanMain := str(obj(form(ivanHook, "calendar.section.get", nil).([]any)[0])["ID"])
	l := form(ivanHook, "calendar.event.get", url.Values{"from": {d}, "to": {day.AddDate(0, 0, 1).Format("2006-01-02")}}).([]any)
	if len(l) != 1 || obj(l[0])["SECT_ID"] != ivanMain || obj(l[0])["MEETING_STATUS"] != "Q" || obj(l[0])["OWNER_ID"] != num(ivanNum) {
		t.Fatalf("Ivan's calendar: %v", l)
	}

	// Ivan answers; he may not change it
	if form(ivanHook, "calendar.meeting.status.set", url.Values{"eventId": {id}, "status": {"Y"}}) != true ||
		form(ivanHook, "calendar.meeting.status.get", url.Values{"eventId": {id}}) != "Y" ||
		form(hook, "calendar.meeting.status.get", url.Values{"eventId": {id}}) != "H" {
		t.Error("calendar.meeting.status")
	}
	call(ivanHook+"calendar.event.update", url.Values{"id": {id}, "name": {"Моя"}}, 403)

	// the second occurrence moved to 12:00 alone; then the series renamed
	wed := day.AddDate(0, 0, 2)
	one := str(form(hook, "calendar.event.update", url.Values{"id": {id}, "recursion_mode": {"this"},
		"current_date_from": {wed.Format("02.01.2006")}, "from": {wed.Format("2006-01-02") + " 12:00"}, "to": {wed.Format("2006-01-02") + " 13:00"}}))
	if one == id || one == "" {
		t.Fatalf("an occurrence changed: %s", one)
	}
	if str(form(hook, "calendar.event.update", url.Values{"id": {id}, "name": {"Планёрка отдела"}})) != id {
		t.Error("calendar.event.update")
	}
	list = form(hook, "calendar.event.get", url.Values{"from": {d}, "to": {day.AddDate(0, 0, 14).Format("2006-01-02")}}).([]any)
	moved := false
	for _, x := range list {
		if str(obj(x)["ID"]) == one && obj(x)["DATE_FROM"] == wed.Format("02.01.2006")+" 12:00:00" {
			moved = true
		}
	}
	if len(list) != 4 || !moved || obj(list[0])["NAME"] != "Планёрка отдела" {
		t.Fatalf("after the changes: %v", list)
	}

	// the planner: Ivan is busy on Monday 10–11
	busy := obj(form(hook, "calendar.accessibility.get", url.Values{"users[]": {num(ivanNum)}, "from": {d}, "to": {d}}))
	if b := busy[num(ivanNum)].([]any); len(b) != 1 || str(obj(b[0])["ID"]) != id || obj(b[0])["DATE_FROM"] != day.Format("02.01.2006")+" 10:00:00" {
		t.Fatalf("calendar.accessibility.get: %v", busy)
	}
	if n := form(ivanHook, "calendar.event.get.nearest", url.Values{"days": {"30"}}).([]any); len(n) == 0 || str(obj(n[0])["ID"]) != id {
		t.Errorf("calendar.event.get.nearest: %v", n)
	}

	// a whole-day meeting in the project's calendar
	pid := str(form(hook, "calendar.event.add", url.Values{"type": {"group"}, "ownerId": {num(projNum)}, "name": {"Релиз"},
		"from": {d}, "to": {day.AddDate(0, 0, 1).Format("2006-01-02")}, "skip_time": {"Y"}}))
	pl := form(hook, "calendar.event.get", url.Values{"type": {"group"}, "ownerId": {num(projNum)}, "from": {d},
		"to": {day.AddDate(0, 0, 3).Format("2006-01-02")}}).([]any)
	if len(pl) != 1 || str(obj(pl[0])["ID"]) != pid || obj(pl[0])["DT_SKIP_TIME"] != "Y" || obj(pl[0])["CAL_TYPE"] != "group" ||
		obj(pl[0])["DATE_TO"] != day.AddDate(0, 0, 1).Format("02.01.2006") || obj(pl[0])["DT_LENGTH"] != float64(2*86400) {
		t.Fatalf("the project's calendar: %v", pl)
	}
	call(ivanHook+"calendar.event.get", url.Values{"type": {"group"}, "ownerId": {num(projNum)}}, 403)

	// Ivan deletes it from his calendar — declines; Anna deletes it; her calendar is deleted with its meetings
	if form(ivanHook, "calendar.event.delete", url.Values{"id": {id}}) != true ||
		form(ivanHook, "calendar.meeting.status.get", url.Values{"eventId": {id}}) != "N" {
		t.Error("an invited person deletes")
	}
	if form(hook, "calendar.event.delete", url.Values{"id": {pid}}) != true {
		t.Error("calendar.event.delete")
	}
	call(hook+"calendar.event.getbyid", url.Values{"id": {pid}}, 400)
	call(hook+"calendar.section.delete", url.Values{"id": {main}}, 403)
	if form(hook, "calendar.section.delete", url.Values{"id": {work}}) != true {
		t.Error("calendar.section.delete")
	}
	call(hook+"calendar.event.getbyid", url.Values{"id": {id}}, 400)
}
