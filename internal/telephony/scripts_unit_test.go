package telephony

import (
	"testing"
)

func TestParseScore(t *testing.T) {
	tests := []struct {
		name      string
		reply     string
		wantScore int
		wantText  string
		wantOK    bool
	}{
		{
			name:      "score and text",
			reply:     "85\nGood call, greeted client promptly.",
			wantScore: 85,
			wantText:  "Good call, greeted client promptly.",
			wantOK:    true,
		},
		{
			name:      "score in sentence on first line",
			reply:     "Score: 90 / 100\nDone:\n- Greeting\n- Offered product",
			wantScore: 90,
			wantText:  "Done:\n- Greeting\n- Offered product",
			wantOK:    true,
		},
		{
			name:      "zero score",
			reply:     "0\nClient hung up immediately.",
			wantScore: 0,
			wantText:  "Client hung up immediately.",
			wantOK:    true,
		},
		{
			name:      "maximum score 100",
			reply:     "100\nPerfect adherence to script.",
			wantScore: 100,
			wantText:  "Perfect adherence to script.",
			wantOK:    true,
		},
		{
			name:      "single line with score only",
			reply:     "75",
			wantScore: 75,
			wantText:  "",
			wantOK:    true,
		},
		{
			name:      "score greater than 100",
			reply:     "105\nInvalid score",
			wantScore: 0,
			wantText:  "",
			wantOK:    false,
		},
		{
			name:      "no digits on first line",
			reply:     "No score provided\nDetailed review...",
			wantScore: 0,
			wantText:  "",
			wantOK:    false,
		},
		{
			name:      "empty reply",
			reply:     "",
			wantScore: 0,
			wantText:  "",
			wantOK:    false,
		},
		{
			name:      "whitespace only reply",
			reply:     "   \n  \t ",
			wantScore: 0,
			wantText:  "",
			wantOK:    false,
		},
		{
			name:      "high first match > 100",
			reply:     "250 points out of 300\nExplanation",
			wantScore: 0,
			wantText:  "",
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			score, text, ok := ParseScore(tc.reply)
			if ok != tc.wantOK {
				t.Fatalf("ParseScore(%q) ok = %v, want %v", tc.reply, ok, tc.wantOK)
			}
			if score != tc.wantScore || text != tc.wantText {
				t.Errorf("ParseScore(%q) = (%d, %q), want (%d, %q)",
					tc.reply, score, text, tc.wantScore, tc.wantText)
			}
		})
	}
}
