package company

import (
	"testing"
	"time"
)

// TestAbsenceChart: absences become bars over their days, cut at the edges of the period; free days are single cells.
func TestAbsenceChart(t *testing.T) {
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local) // Thursday
	day := func(n int) time.Time { return from.AddDate(0, 0, n-1) }
	list := []Absence{
		{ID: "late", UserID: "a", Kind: "sick", From: day(5), To: day(6)},    // overlaps the vacation: the vacation is shown
		{ID: "vac", UserID: "a", Kind: "vacation", From: day(3), To: day(5)}, // 3–5
		{ID: "trip", UserID: "b", Kind: "trip", From: day(-3), To: day(2)},   // from September: cut on the left
		{ID: "off", UserID: "b", Kind: "dayoff", From: day(30), To: day(40)}, // into November: cut on the right
		{ID: "other", UserID: "x", Kind: "other", From: day(1), To: day(31)}, // not on the chart
	}
	weekend := func(t time.Time) bool { return t.Weekday() == time.Saturday || t.Weekday() == time.Sunday }
	rows := AbsenceChart([]Person{{ID: "a", Name: "А"}, {ID: "b", Name: "Б"}, {ID: "c", Name: "В"}}, list, from, 31, weekend, day(2).Add(10*time.Hour))
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	span := func(r ChartRow) (n int) {
		for _, c := range r.Cells {
			n += c.Span
		}
		return n
	}
	for _, r := range rows {
		if span(r) != 31 {
			t.Errorf("%s: %d days", r.Name, span(r))
		}
	}
	a := rows[0].Cells
	if !rows[0].Absent || a[2].Absence == nil || a[2].Absence.ID != "vac" || a[2].Span != 3 || a[2].CutBefore || a[2].CutAfter {
		t.Errorf("vacation %+v", a[2])
	}
	if a[3].Absence == nil || a[3].Absence.ID != "late" || a[3].Span != 1 || !a[3].Day.Equal(day(6)) { // the sick day left after the vacation
		t.Errorf("sick %+v", a[3])
	}
	if !a[1].Today || a[0].Today || a[4].Off || !a[7].Off { // 7 Oct — Wednesday, 10 Oct — Saturday
		t.Errorf("today/off %+v %+v", a[0], a[1])
	}
	b := rows[1].Cells
	if b[0].Absence == nil || b[0].Span != 2 || !b[0].CutBefore || b[0].CutAfter {
		t.Errorf("trip %+v", b[0])
	}
	if l := b[len(b)-1]; l.Absence == nil || l.Absence.ID != "off" || l.Span != 2 || !l.CutAfter {
		t.Errorf("day off %+v", l)
	}
	if rows[2].Absent || len(rows[2].Cells) != 31 {
		t.Errorf("free row %+v", rows[2])
	}
}
