package company

import (
	"testing"
	"time"
)

// TestHonorAndHRMethods: Honor Coming, localDay, and HR Can permissions.
func TestHonorAndHRMethods(t *testing.T) {
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)

	// Honor Coming
	hPast := Honor{From: day.AddDate(0, 0, -2)}
	if hPast.Coming(day) {
		t.Errorf("past honor should not be Coming()")
	}
	hToday := Honor{From: day}
	if hToday.Coming(day) {
		t.Errorf("current day honor should not be Coming()")
	}
	hFuture := Honor{From: day.AddDate(0, 0, 1)}
	if !hFuture.Coming(day) {
		t.Errorf("future honor should be Coming()")
	}

	// localDay normalizes to local midnight
	dt := time.Date(2026, 10, 15, 17, 45, 30, 123456, time.UTC)
	normalized := localDay(dt)
	if normalized.Hour() != 0 || normalized.Minute() != 0 || normalized.Second() != 0 || normalized.Nanosecond() != 0 {
		t.Errorf("localDay() did not zero out time components: %v", normalized)
	}
	if normalized.Location() != time.Local {
		t.Errorf("localDay() location = %v, want Local", normalized.Location())
	}

	// HR Can
	hrOff := HR{On: false, Rights: []string{"absences", "invite"}}
	if hrOff.Can("absences") {
		t.Errorf("HR with On=false should return Can()=false")
	}

	hrOn := HR{On: true, Rights: []string{"absences", "honor"}}
	if !hrOn.Can("absences") || !hrOn.Can("honor") {
		t.Errorf("HR with On=true should have granted rights")
	}
	if hrOn.Can("learning") {
		t.Errorf("HR should not have ungranted rights")
	}
}
