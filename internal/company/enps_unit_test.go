package company

import (
	"testing"
	"time"
)

// TestEnpsStatusAndRate: Enps status lifecycle and response rate calculation.
func TestEnpsStatusAndRate(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)
	ptr := func(t time.Time) *time.Time { return &t }

	// Closed explicitly
	eClosed := Enps{
		From:   day.AddDate(0, 0, -5),
		To:     day.AddDate(0, 0, 5),
		Closed: ptr(day.AddDate(0, 0, -1)),
	}
	if got := eClosed.Status(day); got != "closed" {
		t.Errorf("explicitly closed survey status = %q, want closed", got)
	}

	// Closed by date (past To)
	eExpired := Enps{
		From: day.AddDate(0, 0, -10),
		To:   day.AddDate(0, 0, -1),
	}
	if got := eExpired.Status(day); got != "closed" {
		t.Errorf("past To survey status = %q, want closed", got)
	}

	// Coming (before From)
	eComing := Enps{
		From: day.AddDate(0, 0, 1),
		To:   day.AddDate(0, 0, 10),
	}
	if got := eComing.Status(day); got != "coming" {
		t.Errorf("future From survey status = %q, want coming", got)
	}

	// Open
	eOpen := Enps{
		From: day.AddDate(0, 0, -2),
		To:   day.AddDate(0, 0, 5),
	}
	if got := eOpen.Status(day); got != "open" {
		t.Errorf("active survey status = %q, want open", got)
	}

	// Rate
	if eZero := (Enps{Audience: 0, Answers: 0}); eZero.Rate() != 0 {
		t.Errorf("empty audience Rate() = %d, want 0", eZero.Rate())
	}
	if ePartial := (Enps{Audience: 50, Answers: 35}); ePartial.Rate() != 70 {
		t.Errorf("35/50 audience Rate() = %d, want 70", ePartial.Rate())
	}
}

// TestEnpsGroupMetrics: EnpsGroup add, count and Share formulas.
func TestEnpsGroupMetrics(t *testing.T) {
	var g EnpsGroup

	// Add scores:
	// Promoters: 9, 10 (2)
	// Passives: 7, 8 (2)
	// Detractors: 0, 4, 6 (3)
	scores := []int{10, 9, 8, 7, 6, 4, 0}
	for _, s := range scores {
		g.add(s)
	}
	if g.Answers != 7 || g.Promoters != 2 || g.Passives != 2 || g.Detractors != 3 {
		t.Errorf("after add: Answers=%d Promoters=%d Passives=%d Detractors=%d",
			g.Answers, g.Promoters, g.Passives, g.Detractors)
	}

	// count: (2*100 - 3*100) / 7 = -100 / 7 = -14
	g.count()
	if g.Index != -14 {
		t.Errorf("eNPS Index = %d, want -14", g.Index)
	}

	// Share
	if got := g.Share(g.Promoters); got != 2*100/7 { // 28%
		t.Errorf("Share(Promoters) = %d, want 28", got)
	}

	// Empty group count
	var gEmpty EnpsGroup
	gEmpty.count()
	if gEmpty.Index != 0 || gEmpty.Share(5) != 0 {
		t.Errorf("empty group count/share mismatch")
	}
}
