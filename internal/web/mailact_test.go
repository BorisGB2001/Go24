package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMailPresets(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	wed := time.Date(2026, 10, 14, 10, 20, 0, 0, loc) // a Wednesday
	sat := time.Date(2026, 10, 17, 9, 0, 0, 0, loc)
	at := func(d, h int) time.Time { return time.Date(2026, 10, d, h, 0, 0, 0, loc) }
	for _, c := range []struct {
		v    string
		now  time.Time
		want time.Time
		ok   bool
	}{
		{"later", wed, at(14, 13), true},
		{"tomorrow", wed, at(15, 8), true},
		{"weekend", wed, at(17, 8), true},
		{"weekend", sat, at(24, 8), true}, // Saturday after 8:00 — the next one
		{"nextweek", wed, at(19, 8), true},
		{"nextweek", time.Date(2026, 10, 19, 7, 0, 0, 0, loc), at(26, 8), true}, // on Monday — the next Monday
		{"2026-10-20T09:30", wed, time.Date(2026, 10, 20, 9, 30, 0, 0, loc), true},
		{"2026-10-20T09:30:00+02:00", wed, time.Date(2026, 10, 20, 9, 30, 0, 0, loc), true},
		{"2026-10-01T09:30", wed, time.Time{}, false},
		{"soon", wed, time.Time{}, false},
	} {
		got, ok := snoozeTime(c.v, c.now)
		if ok != c.ok || (ok && !got.Equal(c.want)) {
			t.Errorf("snoozeTime(%q, %s) = %s %v, want %s %v", c.v, c.now.Weekday(), got, ok, c.want, c.ok)
		}
	}
	for _, c := range []struct{ v, want string }{
		{"today", "2026-10-14"}, {"tomorrow", "2026-10-15"}, {"week", "2026-10-16"}, {"nextweek", "2026-10-19"}, {"none", ""}, {"", ""},
		{"2026-11-01", "2026-11-01"},
	} {
		if got, ok := flagDue(c.v, wed); !ok || got != c.want {
			t.Errorf("flagDue(%q) = %q %v, want %q", c.v, got, ok, c.want)
		}
	}
	if d, _ := flagDue("week", sat); d != "2026-10-17" {
		t.Errorf("this week on Saturday = %s", d)
	}
	if _, ok := flagDue("31.12.2026", wed); ok {
		t.Error("a bad date taken")
	}
	if a, err := actArg("remind", "tomorrow", wed); err != nil || a != time.Date(2026, 10, 15, 8, 0, 0, 0, wed.Location()).Format(time.RFC3339) {
		t.Errorf("remind tomorrow = %q %v", a, err)
	}
	if _, err := actArg("remind", "2000-01-01T08:00", wed); err == nil {
		t.Error("a reminder in the past taken")
	}
	if got := letterIDs([]string{"a,b", " c ", "", ",d,"}); len(got) != 4 || got[0] != "a" || got[2] != "c" || got[3] != "d" {
		t.Errorf("letterIDs = %q", got)
	}
}

func TestMailCols(t *testing.T) {
	for _, c := range []struct {
		cookie string
		want   mailCols
	}{
		{"", mailCols{250, 380, 320}},
		{"300.500.400", mailCols{300, 500, 400}},
		{"10.5000.x", mailCols{160, 900, 320}},
		{"<script>", mailCols{250, 380, 320}},
	} {
		r := httptest.NewRequest("GET", "/mail", nil)
		if c.cookie != "" {
			r.AddCookie(&http.Cookie{Name: "mail_cols", Value: c.cookie})
		}
		if got := mailColsOf(r); got != c.want {
			t.Errorf("mail_cols=%q: %+v, want %+v", c.cookie, got, c.want)
		}
	}
}

func TestMailGroup(t *testing.T) {
	now := time.Date(2026, 10, 14, 15, 0, 0, 0, time.Local) // a Wednesday
	for _, c := range []struct {
		t    time.Time
		want string
	}{
		{now.Add(-time.Hour), "today"}, {now.AddDate(0, 0, -1), "yesterday"}, {now.AddDate(0, 0, -2), "week"},
		{now.AddDate(0, 0, -5), "lastweek"}, {now.AddDate(0, 0, -12), "month"}, {now.AddDate(0, 0, -20), "lastmonth"},
		{now.AddDate(0, -3, 0), "older"},
	} {
		if got := mailGroup(c.t, false, now); got != c.want {
			t.Errorf("mailGroup(%s) = %s, want %s", c.t.Format("02.01"), got, c.want)
		}
	}
	if mailGroup(now.AddDate(-1, 0, 0), true, now) != "pinned" {
		t.Error("a pinned letter is not in «Закреплено»")
	}
}
