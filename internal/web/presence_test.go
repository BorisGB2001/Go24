package web

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go24/internal/company"
	"go24/internal/i18n"
)

func TestPresenceText(t *testing.T) {
	tr := func(k string, a ...any) string { return k + fmt.Sprint(a...) }
	now := time.Date(2026, 10, 21, 15, 0, 0, 0, time.Local)
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	cases := []struct {
		status string
		seen   *time.Time
		state  string
		text   string
	}{
		{"", at(time.Minute), "online", "presence.online"},
		{"away", at(2 * time.Minute), "away", "presence.away"},
		{"dnd", at(0), "dnd", "presence.dnd"},
		{"dnd", at(10 * time.Minute), "offline", "presence.seen_min10"},
		{"", at(5 * time.Minute), "offline", "presence.seen_min5"},
		{"", at(3 * time.Hour), "offline", "presence.seen_today12:00"},
		{"", at(20 * time.Hour), "offline", "presence.seen_yesterday19:00"},
		{"", at(5 * 24 * time.Hour), "offline", "presence.seen_day16.10"},
		{"", at(400 * 24 * time.Hour), "offline", "presence.seen_day16.09.2025"},
		{"", nil, "offline", "presence.offline"},
	}
	for _, c := range cases {
		state := presenceState(c.status, c.seen, now)
		if state != c.state {
			t.Errorf("%q %v: state %s, want %s", c.status, c.seen, state, c.state)
		}
		if got := presenceText(tr, state, c.seen, now); got != c.text {
			t.Errorf("%q %v: %s, want %s", c.status, c.seen, got, c.text)
		}
	}
}

// TestPresenceCard: someone else's card asks for their presence; one's own card sets the status.
func TestPresenceCard(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	card := func(me bool, status string) string {
		var b strings.Builder
		if err := pages["ru-RU"]["profile"].ExecuteTemplate(&b, "pcard", map[string]any{
			"P": company.Profile{ID: "u1", Name: "Анна"}, "Me": me, "Presence": status}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	other := card(false, "")
	if !strings.Contains(other, `data-presence="u1" data-presence-text`) || strings.Contains(other, `action="/presence"`) {
		t.Errorf("someone else's card:\n%s", other)
	}
	mine := card(true, "dnd")
	for _, want := range []string{`action="/presence"`, `value="away"`, `value="dnd" class="ghost on"`, "Не беспокоить", "Отошёл"} {
		if !strings.Contains(mine, want) {
			t.Errorf("own card lacks %q:\n%s", want, mine)
		}
	}
	if strings.Contains(mine, "data-presence=") {
		t.Errorf("own card asks for own presence")
	}
}
