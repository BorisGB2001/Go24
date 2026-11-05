package company

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestActingOf: actingOf validates the acting user id and deduplicates known powers.
func TestActingOf(t *testing.T) {
	cases := []struct {
		name       string
		userID     string
		actingID   string
		rights     []string
		wantActing any
		wantRights []string
	}{
		{
			name:       "empty acting id",
			userID:     "u1",
			actingID:   "",
			rights:     []string{"view", "work"},
			wantActing: nil,
			wantRights: []string{},
		},
		{
			name:       "acting id is same as user id",
			userID:     "u1",
			actingID:   "u1",
			rights:     []string{"view", "manage"},
			wantActing: nil,
			wantRights: []string{},
		},
		{
			name:       "valid acting id and known rights with duplicates and invalid ones",
			userID:     "u1",
			actingID:   "u2",
			rights:     []string{"view", "unknown", "work", "view", "manage", "admin"},
			wantActing: "u2",
			wantRights: []string{"view", "work", "manage"},
		},
		{
			name:       "all acting rights",
			userID:     "u1",
			actingID:   "u2",
			rights:     []string{"notify", "manage", "control", "work", "view"},
			wantActing: "u2",
			wantRights: []string{"notify", "manage", "control", "work", "view"},
		},
		{
			name:       "empty rights slice",
			userID:     "u1",
			actingID:   "u2",
			rights:     []string{},
			wantActing: "u2",
			wantRights: []string{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotActing, gotRights := actingOf(tc.userID, tc.actingID, tc.rights)
			if gotActing != tc.wantActing {
				t.Errorf("actingOf acting = %v, want %v", gotActing, tc.wantActing)
			}
			if !slices.Equal(gotRights, tc.wantRights) {
				t.Errorf("actingOf rights = %v, want %v", gotRights, tc.wantRights)
			}
		})
	}
}

// TestAbsenceChartLayouts: AbsenceChart computes cell spans, handles edge cuts, free days, and overlaps.
func TestAbsenceChartLayouts(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local) // 2026-10-01
	day := func(n int) time.Time { return from.AddDate(0, 0, n-1) }
	now := day(3).Add(12 * time.Hour) // today is 2026-10-03
	weekend := func(t time.Time) bool {
		return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday
	}

	p1 := Person{ID: "u1", Name: "Alice"}
	p2 := Person{ID: "u2", Name: "Bob"}
	p3 := Person{ID: "u3", Name: "Charlie"}

	list := []Absence{
		// u1: overlapping absences
		// vac starts day 3, ends day 5 (3 days)
		{ID: "a1", UserID: "u1", Kind: "vacation", From: day(3), To: day(5)},
		// sick starts day 4, ends day 7 (overlaps vac, but starts later: vac takes precedence days 3-5, sick takes days 6-7)
		{ID: "a2", UserID: "u1", Kind: "sick", From: day(4), To: day(7)},

		// u2: cuts on both ends
		// trip starts in September (day -2), ends day 2
		{ID: "a3", UserID: "u2", Kind: "trip", From: day(-2), To: day(2)},
		// off starts day 8, ends day 15 (beyond 10 days)
		{ID: "a4", UserID: "u2", Kind: "dayoff", From: day(8), To: day(15)},

		// u3: outside the period (both before and after)
		{ID: "a5", UserID: "u3", Kind: "other", From: day(-10), To: day(-1)},
		{ID: "a6", UserID: "u3", Kind: "other", From: day(11), To: day(20)},
	}

	days := 10 // period: day 1 to day 10
	rows := AbsenceChart([]Person{p1, p2, p3}, list, from, days, weekend, now)
	if len(rows) != 3 {
		t.Fatalf("rows count = %d, want 3", len(rows))
	}

	// Verify u1
	r1 := rows[0]
	if !r1.Absent {
		t.Errorf("u1 should be marked absent")
	}
	// Day 1: free (Span 1)
	// Day 2: free (Span 1)
	// Day 3..5: vac (Span 3, CutBefore false, CutAfter false)
	// Day 6..7: sick (Span 2, CutBefore true because sick started day 4, CutAfter false)
	// Day 8: free (Span 1)
	// Day 9: free (Span 1)
	// Day 10: free (Span 1)
	if len(r1.Cells) != 7 {
		t.Fatalf("u1 cells len = %d, want 7", len(r1.Cells))
	}
	if r1.Cells[0].Span != 1 || r1.Cells[0].Absence != nil {
		t.Errorf("u1 cell 0: expected free day, got %+v", r1.Cells[0])
	}
	if r1.Cells[1].Span != 1 || r1.Cells[1].Absence != nil {
		t.Errorf("u1 cell 1: expected free day, got %+v", r1.Cells[1])
	}
	// Vac
	cVac := r1.Cells[2]
	if cVac.Span != 3 || cVac.Absence == nil || cVac.Absence.ID != "a1" || cVac.CutBefore || cVac.CutAfter {
		t.Errorf("u1 cell 2: expected vac span 3 uncut, got %+v", cVac)
	}
	// Sick (continuation after vac)
	cSick := r1.Cells[3]
	if cSick.Span != 2 || cSick.Absence == nil || cSick.Absence.ID != "a2" || !cSick.CutBefore || cSick.CutAfter {
		t.Errorf("u1 cell 3: expected sick span 2 CutBefore, got %+v", cSick)
	}

	// Verify u2
	r2 := rows[1]
	if !r2.Absent {
		t.Errorf("u2 should be marked absent")
	}
	// a3 (day -2 to day 2): cut on the left, span from day 1 to day 2 = 2 days
	// day 3..7: free days (5 cells of span 1)
	// a4 (day 8 to day 15): starts day 8, ends at last (day 10) = 3 days, cut on the right
	if len(r2.Cells) != 7 {
		t.Fatalf("u2 cells len = %d, want 7", len(r2.Cells))
	}
	cTrip := r2.Cells[0]
	if cTrip.Span != 2 || cTrip.Absence == nil || cTrip.Absence.ID != "a3" || !cTrip.CutBefore || cTrip.CutAfter {
		t.Errorf("u2 cell 0: expected trip span 2 CutBefore, got %+v", cTrip)
	}
	cOff := r2.Cells[6]
	if cOff.Span != 3 || cOff.Absence == nil || cOff.Absence.ID != "a4" || cOff.CutBefore || !cOff.CutAfter {
		t.Errorf("u2 cell 6: expected off span 3 CutAfter, got %+v", cOff)
	}

	// Verify u3
	r3 := rows[2]
	if r3.Absent {
		t.Errorf("u3 should NOT be marked absent (absences are strictly outside period)")
	}
	if len(r3.Cells) != 10 {
		t.Fatalf("u3 cells len = %d, want 10", len(r3.Cells))
	}
	for i, c := range r3.Cells {
		if c.Span != 1 || c.Absence != nil {
			t.Errorf("u3 cell %d should be free, got %+v", i, c)
		}
		// Day 3 should have Today=true
		if i == 2 && !c.Today {
			t.Errorf("u3 cell 2 (day 3) should be Today")
		} else if i != 2 && c.Today {
			t.Errorf("u3 cell %d should not be Today", i)
		}
	}
}

// TestAbsenceChartEdgeCases: Absence spanning the entire window, empty inputs, single-day absences.
func TestAbsenceChartEdgeCases(t *testing.T) {
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	p := Person{ID: "u1", Name: "Eve"}

	// Full span: started before, ends after
	list := []Absence{
		{ID: "full", UserID: "u1", Kind: "maternity", From: from.AddDate(0, 0, -5), To: from.AddDate(0, 0, 15)},
	}
	rows := AbsenceChart([]Person{p}, list, from, 7, func(time.Time) bool { return false }, from)
	if len(rows) != 1 || len(rows[0].Cells) != 1 {
		t.Fatalf("expected 1 row and 1 cell, got %d rows", len(rows))
	}
	cell := rows[0].Cells[0]
	if cell.Span != 7 || !cell.CutBefore || !cell.CutAfter {
		t.Errorf("full span cell: span=%d cutBefore=%v cutAfter=%v, want 7, true, true", cell.Span, cell.CutBefore, cell.CutAfter)
	}

	// Empty people list
	emptyRows := AbsenceChart(nil, list, from, 7, func(time.Time) bool { return false }, from)
	if len(emptyRows) != 0 {
		t.Errorf("empty people should return empty rows, got %d", len(emptyRows))
	}

	// Single day absence on first day of period
	singleList := []Absence{
		{ID: "s1", UserID: "u1", Kind: "dayoff", From: from, To: from},
	}
	rowsSingle := AbsenceChart([]Person{p}, singleList, from, 3, func(time.Time) bool { return false }, from)
	if len(rowsSingle[0].Cells) != 3 {
		t.Fatalf("expected 3 cells (1 absence + 2 free), got %d", len(rowsSingle[0].Cells))
	}
	if rowsSingle[0].Cells[0].Span != 1 || rowsSingle[0].Cells[0].Absence == nil {
		t.Errorf("cell 0 should be single day absence")
	}
	if rowsSingle[0].Cells[0].CutBefore || rowsSingle[0].Cells[0].CutAfter {
		t.Errorf("single day absence inside period should not be cut")
	}
}

// TestAbsenceKinds: defaultKinds, kindOf, AbsenceKindName, AbsenceKindColor, and newKindKey.
func TestAbsenceKinds(t *testing.T) {
	def := defaultKinds()
	if len(def) != len(AbsenceKinds) {
		t.Fatalf("defaultKinds len = %d, want %d", len(def), len(AbsenceKinds))
	}
	for i, k := range def {
		if k.Key != AbsenceKinds[i] || !k.Builtin || k.Color == "" {
			t.Errorf("defaultKind[%d] = %+v malformed", i, k)
		}
	}

	// kindOf builtin
	kVac, ok := kindOf("vacation")
	if !ok || kVac.Key != "vacation" || kVac.Color != "green" {
		t.Errorf("kindOf(vacation) = %+v, %v", kVac, ok)
	}

	// kindOf unknown
	kUnknown, ok := kindOf("nonexistent_kind")
	if ok || kUnknown.Color != "gray" {
		t.Errorf("kindOf(nonexistent) = %+v, %v, want gray, false", kUnknown, ok)
	}

	// AbsenceKindColor
	if got := AbsenceKindColor("sick"); got != "red" {
		t.Errorf("AbsenceKindColor(sick) = %q, want red", got)
	}
	if got := AbsenceKindColor("unknown"); got != "gray" {
		t.Errorf("AbsenceKindColor(unknown) = %q, want gray", got)
	}

	// AbsenceKindName
	tr := func(msg string, args ...any) string {
		return strings.ToUpper(msg)
	}
	if got := AbsenceKindName("trip", tr); got != "ABSENCE.KIND.TRIP" {
		t.Errorf("AbsenceKindName(trip) = %q, want ABSENCE.KIND.TRIP", got)
	}
	if got := AbsenceKindName("unknown", tr); got != "ABSENCE.KIND.OTHER" {
		t.Errorf("AbsenceKindName(unknown) = %q, want ABSENCE.KIND.OTHER", got)
	}

	// IsAbsenceKind
	if !IsAbsenceKind("vacation") {
		t.Errorf("IsAbsenceKind(vacation) should be true")
	}
	if IsAbsenceKind("random_unknown_kind") {
		t.Errorf("IsAbsenceKind(random_unknown_kind) should be false")
	}

	// newKindKey
	k1 := newKindKey()
	k2 := newKindKey()
	if !strings.HasPrefix(k1, "c") || len(k1) != 9 {
		t.Errorf("newKindKey format invalid: %q", k1)
	}
	if k1 == k2 {
		t.Errorf("newKindKey produced duplicate values: %q", k1)
	}
}
