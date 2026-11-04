package web

import (
	"testing"

	"go24/internal/telephony"
)

// TestPhoneHeat: «Нагрузка по времени суток» shades the hours against the busiest one, Monday first, Sunday last;
// no incoming calls — no chart.
func TestPhoneHeat(t *testing.T) {
	if phoneHeat([7][24]telephony.StatHour{}) != nil {
		t.Fatal("a chart without calls")
	}
	var h [7][24]telephony.StatHour
	h[0][9] = telephony.StatHour{In: 8, Missed: 2}
	h[0][10] = telephony.StatHour{In: 1}
	h[6][23] = telephony.StatHour{In: 4}
	days := phoneHeat(h)
	if len(days) != 7 || days[0].Day != "weekday.1" || days[6].Day != "weekday.0" {
		t.Fatalf("the days: %+v", days)
	}
	if c := days[0].Hours[9]; c.Level != 4 || c.Missed != 2 || c.Hour != 9 {
		t.Fatalf("the busiest hour: %+v", c)
	}
	if days[0].Hours[10].Level != 1 || days[6].Hours[23].Level != 2 || days[3].Hours[12].Level != 0 {
		t.Fatalf("the shades: %+v %+v", days[0].Hours[10], days[6].Hours[23])
	}
}

// TestPhoneDeltas: the change against the period before — grown, fallen, unchanged or nothing to compare with.
func TestPhoneDeltas(t *testing.T) {
	d := phoneDeltas(telephony.StatRow{In: 12, Missed: 1, Out: 5}, telephony.StatRow{In: 10, Missed: 4})
	if d["In"] != (phoneDelta{Text: "+20%", Up: true}) || d["Missed"] != (phoneDelta{Text: "−75%"}) || d["Out"] != (phoneDelta{}) {
		t.Fatalf("the changes: %+v", d)
	}
	if d := phoneDeltas(telephony.StatRow{In: 1001}, telephony.StatRow{In: 1000}); d["In"].Text != "" {
		t.Fatalf("less than a percent: %+v", d["In"])
	}
}
