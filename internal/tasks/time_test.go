package tasks

import "testing"

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int{
		"1:30": 5400, "0:45": 2700, "90m": 5400, "1.5": 5400, "1,5": 5400, "2h": 7200, "1h30m": 5400,
		"1h 30m": 5400, "2ч": 7200, "1ч 30мин": 5400, "45мин": 2700, "3": 10800,
	} {
		if got, err := ParseDuration(in); err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "-1", "0", "1:xx"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) must fail", bad)
		}
	}
}
