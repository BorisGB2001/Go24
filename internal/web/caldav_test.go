package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalDAVHTTP: the calendars over CalDAV — only with an API key that may «calendar:read»; the top is the person
// and their calendar home with the main calendar and their own ones; a meeting a program puts (a series with a
// changed occurrence) is kept with its name and UID, read back as one resource by multiget and query, changed under If-Match only,
// moved into another calendar by its UID and deleted; someone else's meeting is not changed, deleting it declines it.
func TestCalDAVHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	cal := calendar.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: cal, Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	work, err := cal.SaveSection(ctx, anna, calendar.Section{Name: "Работа"})
	must(err)
	at := time.Date(2030, 3, 4, 9, 0, 0, 0, time.UTC)
	theirs, err := cal.Save(ctx, ivan, calendar.Event{Title: "Планёрка Ивана", Start: at.Add(2 * time.Hour), End: at.Add(3 * time.Hour)}, []string{anna})
	must(err)
	key := func(who string, scopes ...string) string {
		k, err := srv.Keys.Create(ctx, who, "caldav", scopes, 0)
		must(err)
		return k
	}
	readKey, writeKey, mailKey := key(anna, "calendar:read"), key(anna, "calendar:read", "calendar:write"), key(anna, "mail:read")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(token, method, p string, hdr map[string]string, body string, want int) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+p, strings.NewReader(body))
		if token != "" {
			req.SetBasicAuth("anyone", token)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, p, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	has := func(what, text string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s has no %q: %.2500s", what, w, text)
			}
		}
	}
	depth0, depth1 := map[string]string{"Depth": "0"}, map[string]string{"Depth": "1"}

	if _, h := do("", "PROPFIND", "/.well-known/caldav", nil, "", 301); h.Get("Location") != "/caldav/" {
		t.Errorf("well-known leads to %q", h.Get("Location"))
	}
	do("", "PROPFIND", "/caldav/", depth0, "", 401)
	do(mailKey, "PROPFIND", "/caldav/", depth0, "", 401)
	_, h := do(readKey, "OPTIONS", "/caldav/", nil, "", 200)
	has("DAV header", h.Get("DAV"), "calendar-access")
	principal, _ := do(readKey, "PROPFIND", "/caldav/", depth0, `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
		<d:prop><d:current-user-principal/><c:calendar-home-set/><c:calendar-user-address-set/><d:unknown/></d:prop></d:propfind>`, 207)
	has("the principal", principal, "<d:current-user-principal><d:href>/caldav/</d:href>", "<c:calendar-home-set><d:href>/caldav/</d:href>",
		"mailto:anna@go24.test", "<d:unknown/></d:prop><d:status>HTTP/1.1 404")
	home, _ := do(readKey, "PROPFIND", "/caldav/", depth1, `<d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/"><d:prop><d:resourcetype/><d:displayname/><cs:getctag/></d:prop></d:propfind>`, 207)
	has("the home", home, "<d:href>/caldav/main/</d:href>", "<d:href>/caldav/"+work+"/</d:href>", "Работа", "<c:calendar/>", "<cs:getctag>")
	do(readKey, "PROPFIND", "/caldav/nope/", depth0, "", 404)

	// a weekly series from the phone, its second occurrence moved by an hour
	series := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple//iOS//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:abc@phone\r\nDTSTAMP:20300101T000000Z\r\nDTSTART:20300304T090000Z\r\nDTEND:20300304T100000Z\r\n" +
		"SUMMARY:Йога\r\nRRULE:FREQ=WEEKLY\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:abc@phone\r\nDTSTAMP:20300101T000000Z\r\nRECURRENCE-ID:20300311T090000Z\r\nDTSTART:20300311T100000Z\r\nDTEND:20300311T110000Z\r\n" +
		"SUMMARY:Йога позже\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	do(readKey, "PUT", "/caldav/main/abc.ics", nil, series, 403)
	do(writeKey, "PUT", "/caldav/main/abc.ics", map[string]string{"If-None-Match": "*"}, series, 201)
	var id, repeat string
	var reminds []int
	must(pool.QueryRow(ctx, `SELECT id::text, repeat, reminds FROM events WHERE owner_id = $1 AND dav_name = 'abc.ics' AND import_uid = 'abc@phone'`, anna).Scan(&id, &repeat, &reminds))
	if repeat != "weekly" || len(reminds) != 1 || reminds[0] != 15 {
		t.Errorf("the series = %s %v", repeat, reminds)
	}
	var child string
	must(pool.QueryRow(ctx, `SELECT id::text FROM events WHERE series_id = $1 AND series_at = '2030-03-11T09:00:00Z' AND title = 'Йога позже' AND starts_at = '2030-03-11T10:00:00Z'`, id).Scan(&child))
	do(writeKey, "PUT", "/caldav/main/abc.ics", map[string]string{"If-None-Match": "*"}, series, 412)

	list, _ := do(readKey, "PROPFIND", "/caldav/main/", depth1, "", 207)
	has("the main calendar", list, "/caldav/main/abc.ics", "/caldav/main/"+theirs+".ics", "<d:getetag>")
	if strings.Contains(list, child) { // the changed occurrence is in the series' resource
		t.Errorf("the changed occurrence is a resource of its own: %s", list)
	}
	got, _ := do(readKey, "REPORT", "/caldav/main/", depth1, `<c:calendar-multiget xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
		<d:prop><d:getetag/><c:calendar-data/></d:prop><d:href>/caldav/main/abc.ics</d:href><d:href>/caldav/main/gone.ics</d:href></c:calendar-multiget>`, 207)
	has("multiget", got, "UID:abc@phone", "RRULE:FREQ=WEEKLY", "RECURRENCE-ID:20300311T090000Z", "SUMMARY:Йога позже", "TRIGGER:-PT15M", "<d:href>/caldav/main/gone.ics</d:href><d:status>HTTP/1.1 404")
	query := func(from, to string) string {
		out, _ := do(readKey, "REPORT", "/caldav/main/", depth1, `<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/></d:prop>
			<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT"><c:time-range start="`+from+`" end="`+to+`"/></c:comp-filter></c:comp-filter></c:filter></c:calendar-query>`, 207)
		return out
	}
	has("query", query("20300401T000000Z", "20300501T000000Z"), "abc.ics")
	if q := query("20290101T000000Z", "20290201T000000Z"); strings.Contains(q, "abc.ics") || strings.Contains(q, theirs) {
		t.Errorf("a query of 2029: %.1500s", q)
	}
	do(readKey, "REPORT", "/caldav/main/", depth1, `<d:sync-collection xmlns:d="DAV:"><d:sync-token/></d:sync-collection>`, 403)

	// changed under If-Match only
	_, h = do(readKey, "GET", "/caldav/main/abc.ics", nil, "", 200)
	etag := h.Get("ETag")
	renamed := strings.Replace(series, "SUMMARY:Йога\r\n", "SUMMARY:Йога утром\r\n", 1)
	do(writeKey, "PUT", "/caldav/main/abc.ics", map[string]string{"If-Match": `"stale"`}, renamed, 412)
	do(writeKey, "PUT", "/caldav/main/abc.ics", map[string]string{"If-Match": etag}, renamed, 204)
	var title string
	var children int
	must(pool.QueryRow(ctx, `SELECT title, (SELECT count(*) FROM events WHERE series_id = $1) FROM events WHERE id = $1`, id).Scan(&title, &children))
	if title != "Йога утром" || children != 1 {
		t.Errorf("changed: %q, %d changed occurrences", title, children)
	}

	// someone else's meeting: not changed; deleted — declined
	do(writeKey, "PUT", "/caldav/main/"+theirs+".ics", nil, strings.Replace(series, "abc@phone", theirs+"@go24", -1), 403)
	do(writeKey, "DELETE", "/caldav/main/"+theirs+".ics", nil, "", 204)
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_attendees WHERE event_id = $1 AND user_id = $2`, theirs, anna).Scan(&status))
	if status != "declined" {
		t.Errorf("anna's answer = %s", status)
	}
	do(readKey, "GET", "/caldav/main/"+theirs+".ics", nil, "", 404)

	// put into «Работа» by its UID: the meeting moves there
	do(writeKey, "PUT", "/caldav/"+work+"/abc.ics", nil, renamed, 204)
	var in string
	must(pool.QueryRow(ctx, `SELECT coalesce(calendar_id::text, '') FROM events WHERE id = $1`, id).Scan(&in))
	if in != work {
		t.Errorf("the meeting is in %q", in)
	}
	do(writeKey, "DELETE", "/caldav/main/abc.ics", nil, "", 404)
	do(writeKey, "DELETE", "/caldav/"+work+"/abc.ics", nil, "", 204)
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1 OR series_id = $1`, id).Scan(&left))
	if left != 0 {
		t.Errorf("%d meetings left of the deleted series", left)
	}
}
