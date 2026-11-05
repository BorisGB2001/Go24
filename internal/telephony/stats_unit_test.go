package telephony

import (
	"testing"
)

func TestStatRowCalculations(t *testing.T) {
	// 1. AvgScore
	zeroScored := StatRow{Scored: 0, Scores: 0}
	if got := zeroScored.AvgScore(); got != -1 {
		t.Errorf("zeroScored.AvgScore() = %d, want -1", got)
	}

	scoredRow := StatRow{Scored: 3, Scores: 251} // 251/3 = 83.67 -> 84 rounded
	if got := scoredRow.AvgScore(); got != 84 {
		t.Errorf("scoredRow.AvgScore() = %d, want 84", got)
	}

	scoredRow2 := StatRow{Scored: 3, Scores: 250} // 250/3 = 83.33 -> 83 rounded
	if got := scoredRow2.AvgScore(); got != 83 {
		t.Errorf("scoredRow2.AvgScore() = %d, want 83", got)
	}

	// 2. AvgRating
	zeroRated := StatRow{Rated: 0, Ratings: 0}
	if got := zeroRated.AvgRating(); got != "" {
		t.Errorf("zeroRated.AvgRating() = %q, want \"\"", got)
	}

	ratedRow := StatRow{Rated: 2, Ratings: 9}
	if got := ratedRow.AvgRating(); got != "4.5" {
		t.Errorf("ratedRow.AvgRating() = %q, want \"4.5\"", got)
	}

	ratedRowWhole := StatRow{Rated: 1, Ratings: 5}
	if got := ratedRowWhole.AvgRating(); got != "5.0" {
		t.Errorf("ratedRowWhole.AvgRating() = %q, want \"5.0\"", got)
	}

	// 3. Answered
	ansRow := StatRow{In: 10, Missed: 3}
	if got := ansRow.Answered(); got != 7 {
		t.Errorf("ansRow.Answered() = %d, want 7", got)
	}

	// 4. MissedPct
	zeroInRow := StatRow{In: 0, Missed: 0}
	if got := zeroInRow.MissedPct(); got != 0 {
		t.Errorf("zeroInRow.MissedPct() = %d, want 0", got)
	}

	missedRow := StatRow{In: 3, Missed: 1} // 33.33% -> 33%
	if got := missedRow.MissedPct(); got != 33 {
		t.Errorf("missedRow.MissedPct() = %d, want 33", got)
	}

	missedRow2 := StatRow{In: 3, Missed: 2} // 66.67% -> 67%
	if got := missedRow2.MissedPct(); got != 67 {
		t.Errorf("missedRow2.MissedPct() = %d, want 67", got)
	}

	// 5. AvgTalk
	zeroTalkRow := StatRow{In: 1, Missed: 1, OutOK: 0, Seconds: 100} // Answered=0, OutOK=0
	if got := zeroTalkRow.AvgTalk(); got != 0 {
		t.Errorf("zeroTalkRow.AvgTalk() = %d, want 0", got)
	}

	talkRow := StatRow{In: 3, Missed: 1, OutOK: 2, Seconds: 242} // n = 2 + 2 = 4 calls. (242+2)/4 = 61s
	if got := talkRow.AvgTalk(); got != 61 {
		t.Errorf("talkRow.AvgTalk() = %d, want 61", got)
	}

	// 6. AvgWait
	zeroWaitRow := StatRow{In: 2, Missed: 2, WaitSeconds: 50} // Answered = 0
	if got := zeroWaitRow.AvgWait(); got != 0 {
		t.Errorf("zeroWaitRow.AvgWait() = %d, want 0", got)
	}

	waitRow := StatRow{In: 4, Missed: 2, WaitSeconds: 9} // Answered = 2. (9+1)/2 = 5s
	if got := waitRow.AvgWait(); got != 5 {
		t.Errorf("waitRow.AvgWait() = %d, want 5", got)
	}
}

func TestStatDayAnswered(t *testing.T) {
	d := StatDay{In: 15, Missed: 4}
	if got := d.Answered(); got != 11 {
		t.Errorf("StatDay.Answered() = %d, want 11", got)
	}
}
