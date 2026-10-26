package calendar

import (
	"testing"
	"time"
)

// TestEventTZ: a weekly meeting at 10:00 in New York stays at 10:00 there over its change of clocks (a week before
// Europe's), so by Amsterdam's clock it is 16:00, then 15:00 for three weeks, then 16:00 again.
func TestEventTZ(t *testing.T) {
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	ny, _ := time.LoadLocation("America/New_York")
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = ams
	start := time.Date(2026, 3, 1, 10, 0, 0, 0, ny).In(ams)
	e := Event{Start: start, End: start.Add(time.Hour), Repeat: "weekly", TZ: "America/New_York"}
	var got []int
	for _, o := range e.expand(start, start.AddDate(0, 0, 35)) {
		got = append(got, o.Start.Hour())
		if h := o.Start.In(ny).Hour(); h != 10 {
			t.Fatalf("%v: %d in New York", o.Start, h)
		}
	}
	if len(got) != 5 || got[0] != 16 || got[1] != 15 || got[3] != 15 || got[4] != 16 {
		t.Fatalf("by Amsterdam's clock %v", got)
	}
	if !e.OtherTZ() || (Event{Start: start, TZ: "Europe/Berlin"}).OtherTZ() || (Event{Start: start}).OtherTZ() {
		t.Fatal("OtherTZ")
	}
	// the portal's zone: by its clock as before
	e.TZ = ""
	if o := e.expand(start, start.AddDate(0, 0, 35)); o[1].Start.Hour() != 16 {
		t.Fatalf("portal's zone %v", o[1].Start)
	}
	if !ValidTZ("") || !ValidTZ("Asia/Tokyo") || ValidTZ("Mars/Base") || ValidTZ("Local") {
		t.Fatal("ValidTZ")
	}
	if z := Zones(start, "Asia/Kathmandu"); len(z) != len(TimeZones)+1 || z[0].Name != "America/Los_Angeles" ||
		z[len(z)-1].Label != "(UTC+12:00) Asia/Kamchatka" {
		t.Fatalf("zones %v", z)
	}
}
