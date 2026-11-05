package company

import (
	"testing"
	"time"
)

// TestIdleSecsAndReport: Idle.Secs computes duration, IdleReport groups idles by person in order.
func TestIdleSecsAndReport(t *testing.T) {
	t0 := time.Date(2026, 10, 13, 10, 0, 0, 0, time.Local)
	i1 := Idle{
		ID:      1,
		UserID:  "u1",
		Started: t0,
		Ended:   t0.Add(15 * time.Minute), // 900s
		Kind:    "work",
	}
	if got := i1.Secs(); got != 900 {
		t.Errorf("Idle.Secs() = %d, want 900", got)
	}

	i2 := Idle{
		ID:      2,
		UserID:  "u1",
		Started: t0.Add(30 * time.Minute),
		Ended:   t0.Add(45 * time.Minute), // 900s
		Kind:    "personal",
	}
	i3 := Idle{
		ID:      3,
		UserID:  "u1",
		Started: t0.Add(50 * time.Minute),
		Ended:   t0.Add(60 * time.Minute), // 600s
		Kind:    "",                       // unanswered
	}

	p1 := Person{ID: "u1", Name: "Alice"}
	p2 := Person{ID: "u2", Name: "Bob"}

	// Idle for unknown user u99 should be skipped
	iUnknown := Idle{
		ID:      4,
		UserID:  "u99",
		Started: t0,
		Ended:   t0.Add(20 * time.Minute),
		Kind:    "work",
	}

	rows := IdleReport([]Person{p1, p2}, []Idle{i1, iUnknown, i2, i3})
	if len(rows) != 2 {
		t.Fatalf("IdleReport rows = %d, want 2", len(rows))
	}

	// u1
	r1 := rows[0]
	if r1.Person.ID != "u1" || len(r1.Idles) != 3 {
		t.Errorf("u1 row mismatch: %+v", r1)
	}
	if r1.Secs != 2400 || r1.Work != 900 || r1.Personal != 900 || r1.No != 600 {
		t.Errorf("u1 totals: Secs=%d Work=%d Personal=%d No=%d, want 2400/900/900/600",
			r1.Secs, r1.Work, r1.Personal, r1.No)
	}

	// u2 (no idles)
	r2 := rows[1]
	if r2.Person.ID != "u2" || len(r2.Idles) != 0 || r2.Secs != 0 || r2.Work != 0 || r2.Personal != 0 || r2.No != 0 {
		t.Errorf("u2 empty row mismatch: %+v", r2)
	}
}
