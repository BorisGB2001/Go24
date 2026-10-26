package calendar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Subscription to one's calendar from Outlook, Google Calendar or a phone, as the calendar sync: a secret
// link to an iCalendar feed of the person's meetings (repeated ones with their rules and exceptions) and, if they
// wish, the deadlines of their tasks.

// Feed is the person's subscription ("" token — none yet).
type Feed struct {
	Token string
	Tasks bool
}

// FeedOf is the person's subscription.
func (s *Store) FeedOf(ctx context.Context, userID string) (Feed, error) {
	var f Feed
	err := s.db.QueryRow(ctx, `SELECT token, tasks FROM calendar_feeds WHERE user_id = $1`, userID).Scan(&f.Token, &f.Tasks)
	if errors.Is(err, pgx.ErrNoRows) {
		return Feed{Tasks: true}, nil
	}
	return f, err
}

// SetFeed issues a new link (renew, or when there is none) and keeps whether the deadlines of tasks go into it.
func (s *Store) SetFeed(ctx context.Context, userID string, renew, withTasks bool) (Feed, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return Feed{}, err
	}
	var f Feed
	err := s.db.QueryRow(ctx, `INSERT INTO calendar_feeds (user_id, token, tasks) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET tasks = EXCLUDED.tasks,
			token = CASE WHEN $4 THEN EXCLUDED.token ELSE calendar_feeds.token END
		RETURNING token, tasks`, userID, hex.EncodeToString(b), withTasks, renew).Scan(&f.Token, &f.Tasks)
	return f, err
}

// FeedICS is the iCalendar of the feed with this token: the meetings its person sees from a month ago on, and the
// deadlines of their open tasks; ErrNotFound for an unknown token or a person who left.
func (s *Store) FeedICS(ctx context.Context, token string, now time.Time) (string, error) {
	var userID, name string
	var withTasks bool
	err := s.db.QueryRow(ctx, `SELECT f.user_id::text, f.tasks, u.name FROM calendar_feeds f JOIN users u ON u.id = f.user_id
		WHERE f.token = $1 AND u.active`, token).Scan(&userID, &withTasks, &name)
	if errors.Is(err, pgx.ErrNoRows) || token == "" {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	from := now.AddDate(0, -1, 0)
	rows, err := s.db.Query(ctx, selectSQL+` WHERE ((e.repeat = '' AND e.ends_at >= $3) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $3::date)))
		AND `+visible+` ORDER BY e.starts_at`, userID, false, from)
	if err != nil {
		return "", err
	}
	list, err := s.scan(ctx, userID, rows)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	line := func(l string) { foldLine(&b, l) }
	line("BEGIN:VCALENDAR")
	line("VERSION:2.0")
	line("PRODID:-//go24//calendar//RU")
	line("CALSCALE:GREGORIAN")
	line("METHOD:PUBLISH")
	line("X-WR-CALNAME:" + escapeText("go24 · "+name))
	line("REFRESH-INTERVAL;VALUE=DURATION:PT30M")
	line("X-PUBLISHED-TTL:PT30M")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, e := range list {
		if e.MyStatus == "declined" {
			continue
		}
		writeEvent(line, e, stamp)
	}
	if withTasks {
		trows, err := s.db.Query(ctx, `SELECT p.key || '-' || t.number, t.title, t.deadline FROM tasks t JOIN projects p ON p.id = t.project_id
			WHERE t.status <> 'completed' AND t.deadline >= $2
			  AND (t.responsible_id = $1 OR t.creator_id = $1 OR EXISTS (SELECT 1 FROM task_participants tp WHERE tp.task_id = t.id AND tp.user_id = $1))
			  AND (p.kind <> 'collab' OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1))`, userID, from)
		if err != nil {
			return "", err
		}
		for trows.Next() {
			var key, title string
			var at time.Time
			if err := trows.Scan(&key, &title, &at); err != nil {
				trows.Close()
				return "", err
			}
			line("BEGIN:VEVENT")
			line("UID:task-" + key + "@go24")
			line("DTSTAMP:" + stamp)
			writeTimes(line, at, at, false)
			line("SUMMARY:" + escapeText("☑ "+key+" "+title))
			line("TRANSP:TRANSPARENT")
			line("END:VEVENT")
		}
		trows.Close()
		if err := trows.Err(); err != nil {
			return "", err
		}
	}
	line("END:VCALENDAR")
	return b.String(), nil
}

// writeEvent writes the VEVENT of a meeting as the viewer sees it (a private one of someone else — without a title).
func writeEvent(line func(string), e Event, stamp string) {
	line("BEGIN:VEVENT")
	if e.UID != "" {
		line("UID:" + e.UID)
	} else {
		line("UID:" + e.ID + "@go24")
	}
	line("DTSTAMP:" + stamp)
	writeTimes(line, e.Start, e.End, e.AllDay)
	title := e.Title
	if e.Hidden {
		title = "—"
	}
	line("SUMMARY:" + escapeText(title))
	if e.Description != "" {
		line("DESCRIPTION:" + escapeText(e.Description))
	}
	where := e.Where
	if e.RoomName != "" {
		where = strings.TrimSpace(e.RoomName + " " + where)
	}
	if where != "" {
		line("LOCATION:" + escapeText(where))
	}
	if e.Private {
		line("CLASS:PRIVATE")
	}
	if e.Important && !e.Hidden { // «Важная встреча»: high importance in Outlook and others
		line("PRIORITY:1")
		line("X-MICROSOFT-CDO-IMPORTANCE:2")
	}
	if e.Color != "" && !e.Hidden { // its colour (RFC 7986: a CSS colour name)
		line("COLOR:" + colorName(e.Color))
	}
	switch {
	case e.MyStatus == "tentative" || e.MyStatus == "invited" || e.Accessibility == "tentative":
		line("STATUS:TENTATIVE")
	default:
		line("STATUS:CONFIRMED")
	}
	if e.Accessibility == "free" {
		line("TRANSP:TRANSPARENT")
	}
	if e.Repeat != "" {
		rule := "RRULE:FREQ=" + strings.ToUpper(e.Repeat) + fmt.Sprintf(";INTERVAL=%d", max(e.Every, 1))
		if e.Until != nil {
			u := time.Date(e.Until.Year(), e.Until.Month(), e.Until.Day(), 23, 59, 59, 0, time.Local)
			rule += ";UNTIL=" + u.UTC().Format("20060102T150405Z")
		}
		line(rule)
		for _, x := range e.Skip {
			if e.AllDay {
				line("EXDATE;VALUE=DATE:" + x.In(time.Local).Format("20060102"))
			} else {
				line("EXDATE:" + x.UTC().Format("20060102T150405Z"))
			}
		}
	}
	for _, m := range e.Reminds {
		line("BEGIN:VALARM")
		line("ACTION:DISPLAY")
		line("DESCRIPTION:" + escapeText(title))
		line(fmt.Sprintf("TRIGGER:-PT%dM", m))
		line("END:VALARM")
	}
	line("END:VEVENT")
}

// writeTimes: DTSTART and DTEND in UTC, or whole days (DTEND is the day after the last one).
func writeTimes(line func(string), start, end time.Time, allDay bool) {
	if allDay {
		s, e := start.In(time.Local), end.In(time.Local)
		line("DTSTART;VALUE=DATE:" + s.Format("20060102"))
		line("DTEND;VALUE=DATE:" + time.Date(e.Year(), e.Month(), e.Day()+1, 0, 0, 0, 0, time.Local).Format("20060102"))
		return
	}
	line("DTSTART:" + start.UTC().Format("20060102T150405Z"))
	line("DTEND:" + end.UTC().Format("20060102T150405Z"))
}

// escapeText escapes a TEXT value of iCalendar (RFC 5545 3.3.11).
// cssColors: the CSS colour names a meeting's colour is told by in the feed, the nearest one taken.
var cssColors = map[string][3]int{"olivedrab": {107, 142, 35}, "yellowgreen": {154, 205, 50}, "steelblue": {70, 130, 180},
	"dodgerblue": {30, 144, 255}, "darkturquoise": {0, 206, 209}, "lightseagreen": {32, 178, 170}, "orange": {255, 165, 0},
	"darkorange": {255, 140, 0}, "mediumseagreen": {60, 179, 113}, "crimson": {220, 20, 60}, "red": {255, 0, 0},
	"orchid": {218, 112, 214}, "mediumpurple": {147, 112, 219}, "slategray": {112, 128, 144}, "gray": {128, 128, 128},
	"chocolate": {210, 105, 30}, "sienna": {160, 82, 45}, "palevioletred": {219, 112, 147}, "hotpink": {255, 105, 180},
	"royalblue": {65, 105, 225}, "navy": {0, 0, 128}, "green": {0, 128, 0}, "gold": {255, 215, 0}, "black": {0, 0, 0}}

// paletteNames: the names of the palette's colours (Colors), as one sees them; the nearest is not always that.
var paletteNames = map[string]string{"#86b100": "olivedrab", "#0092cc": "dodgerblue", "#00afc7": "darkturquoise",
	"#e89b06": "orange", "#00b38c": "mediumseagreen", "#de2b24": "crimson", "#bd7ac9": "orchid", "#838fa0": "slategray",
	"#c3612c": "chocolate", "#e97090": "palevioletred"}

// colorName is the CSS colour name nearest to a #rrggbb.
func colorName(hex string) string {
	if name, ok := paletteNames[hex]; ok {
		return name
	}
	var r, g, b int
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	best, dist := "", -1
	for name, c := range cssColors {
		d := (c[0]-r)*(c[0]-r) + (c[1]-g)*(c[1]-g) + (c[2]-b)*(c[2]-b)
		if dist < 0 || d < dist || d == dist && name < best {
			best, dist = name, d
		}
	}
	return best
}

func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(s)
}

// foldLine writes a content line folded at 75 octets, without splitting a UTF-8 character.
func foldLine(b *strings.Builder, l string) {
	for len(l) > 75 {
		cut := 75
		for cut > 0 && l[cut]&0xC0 == 0x80 {
			cut--
		}
		b.WriteString(l[:cut] + "\r\n ")
		l = l[cut:]
	}
	b.WriteString(l + "\r\n")
}
