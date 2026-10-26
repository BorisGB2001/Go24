package web

import (
	"testing"
	"time"

	"go24/internal/i18n"
)

// TestBirthdayFormat: a birthday reads «1 августа» / «1 August» — the day without a leading zero, the month in words of
// the language; the year only when the employee shows it.
func TestBirthdayFormat(t *testing.T) {
	b, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	d := time.Date(1990, 8, 1, 0, 0, 0, 0, time.Local)
	for _, c := range []struct {
		lang string
		year bool
		want string
	}{{"ru-RU", false, "1 августа"}, {"ru-RU", true, "1 августа 1990"}, {"en-US", false, "1 August"}, {"en-US", true, "1 August 1990"}} {
		if got := langFuncs(b, c.lang)["bday"].(func(time.Time, bool) string)(d, c.year); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.lang, c.year, got, c.want)
		}
	}
}
