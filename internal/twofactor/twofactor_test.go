package twofactor

import (
	"testing"
	"time"
)

func TestJudge(t *testing.T) {
	since := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := Policy{Mode: "admins", Days: 7, Since: since}
	old, fresh := since.AddDate(-1, 0, 0), since.AddDate(0, 0, 3)
	cases := []struct {
		name        string
		p           Policy
		admin, mfa  bool
		joined, now time.Time
		want        Verdict
		due         time.Time
	}{
		{"not required", Policy{}, true, false, old, since, Pass, time.Time{}},
		{"admins only: an employee passes", p, false, false, old, since.AddDate(1, 0, 0), Pass, time.Time{}},
		{"the factor used", p, true, true, old, since.AddDate(1, 0, 0), Pass, time.Time{}},
		{"the period runs", p, true, false, old, since.AddDate(0, 0, 6), Remind, since.AddDate(0, 0, 7)},
		{"the period is over", p, true, false, old, since.AddDate(0, 0, 7), Deny, since.AddDate(0, 0, 7)},
		{"joined later: from joining", p, true, false, fresh, since.AddDate(0, 0, 8), Remind, fresh.AddDate(0, 0, 7)},
		{"for all", Policy{Mode: "all", Since: since}, false, false, old, since, Deny, since},
	}
	for _, c := range cases {
		v, due := c.p.Judge(c.admin, c.mfa, c.joined, c.now)
		if v != c.want || !due.Equal(c.due) {
			t.Errorf("%s: %v %v, want %v %v", c.name, v, due, c.want, c.due)
		}
	}
	if !MFA([]string{"pwd", "mfa"}) || MFA([]string{"pwd"}) || MFA(nil) {
		t.Error("MFA")
	}
}
