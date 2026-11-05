package company

import (
	"slices"
	"testing"
	"time"
)

// TestLearningPercentAndOverdue: percent caps at 100 and overdue detects expired deadlines.
func TestLearningPercentAndOverdue(t *testing.T) {
	today := time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)
	ptr := func(t time.Time) *time.Time { return &t }

	// percent
	if percent(0, 0) != 0 || percent(5, 0) != 0 {
		t.Errorf("percent with zero total should be 0")
	}
	if percent(5, 10) != 50 {
		t.Errorf("percent(5, 10) = %d, want 50", percent(5, 10))
	}
	if percent(12, 10) != 100 { // capped at 100
		t.Errorf("percent(12, 10) = %d, want 100", percent(12, 10))
	}

	// overdue
	if overdue(nil, false, today) {
		t.Errorf("nil due date should not be overdue")
	}
	if overdue(ptr(today.AddDate(0, 0, -1)), true, today) {
		t.Errorf("finished course should not be overdue even past deadline")
	}
	if !overdue(ptr(today.AddDate(0, 0, -1)), false, today) {
		t.Errorf("unfinished course past deadline should be overdue")
	}
	if overdue(ptr(today.AddDate(0, 0, 1)), false, today) {
		t.Errorf("course with future deadline should not be overdue")
	}

	// Course and Learner methods
	c := Course{
		Lessons: 4,
		Done:    4,
		Due:     ptr(today.AddDate(0, 0, -2)),
	}
	if c.Percent() != 100 || !c.Finished() || c.Overdue(today) {
		t.Errorf("Course methods mismatch: pct=%d finished=%v overdue=%v", c.Percent(), c.Finished(), c.Overdue(today))
	}

	l := Learner{
		Total: 5,
		Done:  3,
		Due:   ptr(today.AddDate(0, 0, -1)),
	}
	if l.Percent() != 60 || l.Finished() || !l.Overdue(today) {
		t.Errorf("Learner methods mismatch: pct=%d finished=%v overdue=%v", l.Percent(), l.Finished(), l.Overdue(today))
	}

	les := Lesson{Kind: "test"}
	if !les.Test() {
		t.Errorf("Lesson with kind 'test' should have Test()=true")
	}
	lesText := Lesson{Kind: "text"}
	if lesText.Test() {
		t.Errorf("Lesson with kind 'text' should have Test()=false")
	}
}

// TestLearningHelpers: likeEscape and splitTo helpers.
func TestLearningHelpers(t *testing.T) {
	// likeEscape
	raw := `foo\bar%baz_qux`
	escaped := likeEscape(raw)
	want := `foo\\bar\%baz\_qux`
	if escaped != want {
		t.Errorf("likeEscape(%q) = %q, want %q", raw, escaped, want)
	}

	// splitTo
	to := []string{"u1", "dept:d1", "u2", "dept:d2"}
	users, depts := splitTo(to)
	if !slices.Equal(users, []string{"u1", "u2"}) || !slices.Equal(depts, []string{"d1", "d2"}) {
		t.Errorf("splitTo() = (%v, %v), want ([u1 u2], [d1 d2])", users, depts)
	}
}

// TestLearnTestsOptionsAndAttempts: OptionsOf, OptionLines, and Attempt helpers.
func TestLearnTestsOptionsAndAttempts(t *testing.T) {
	// Options round trip
	lines := "*Paris\nLondon\n*Berlin\n"
	opts := OptionsOf(lines)
	if len(opts) != 3 {
		t.Fatalf("OptionsOf len = %d, want 3", len(opts))
	}
	if !opts[0].Right || opts[0].Text != "Paris" {
		t.Errorf("opt 0 mismatch: %+v", opts[0])
	}
	if opts[1].Right || opts[1].Text != "London" {
		t.Errorf("opt 1 mismatch: %+v", opts[1])
	}
	if !opts[2].Right || opts[2].Text != "Berlin" {
		t.Errorf("opt 2 mismatch: %+v", opts[2])
	}

	q := Question{Options: opts}
	if got := q.OptionLines(); got != lines {
		t.Errorf("OptionLines() = %q, want %q", got, lines)
	}

	// Attempt methods
	startTime := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	att := Attempt{
		Score:   8,
		Max:     10,
		Started: startTime,
		Answers: map[string]any{
			"q1": []any{float64(0), float64(2)},
			"q2": "Typed Answer",
		},
	}
	if att.Pct() != 80 {
		t.Errorf("Attempt.Pct() = %d, want 80", att.Pct())
	}
	if att.Deadline(0) != (time.Time{}) {
		t.Errorf("Attempt.Deadline(0) should be zero time")
	}
	if want := startTime.Add(30 * time.Minute); att.Deadline(30) != want {
		t.Errorf("Attempt.Deadline(30) = %v, want %v", att.Deadline(30), want)
	}
	if !att.Chosen("q1", 0) || !att.Chosen("q1", 2) || att.Chosen("q1", 1) {
		t.Errorf("Attempt.Chosen() mismatch for q1")
	}
	if att.Typed("q2") != "Typed Answer" || att.Typed("q1") != "" {
		t.Errorf("Attempt.Typed() mismatch")
	}

	// numbers helper
	ints := []int{3, 1, 1, 2, 5, -1}
	parsedInts := numbers(ints, 4) // max n = 4, so 0..3 allowed, deduplicated and sorted
	if !slices.Equal(parsedInts, []int{1, 2, 3}) {
		t.Errorf("numbers([]int) = %v, want [1 2 3]", parsedInts)
	}

	floats := []any{float64(2), float64(0), float64(2), "invalid", float64(4)}
	parsedFloats := numbers(floats, 4) // 0..3 allowed
	if !slices.Equal(parsedFloats, []int{0, 2}) {
		t.Errorf("numbers([]any) = %v, want [0 2]", parsedFloats)
	}
}
