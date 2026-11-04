package matrixsync

import "testing"

// TestIsPoll tests isPoll checking for MSC3381 and standard Matrix poll event types.
func TestIsPoll(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{typ: pollStart, want: true},
		{typ: "m.poll.start", want: true},
		{typ: pollResponse, want: false},
		{typ: pollEnd, want: false},
		{typ: "m.room.message", want: false},
		{typ: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := isPoll(tt.typ); got != tt.want {
				t.Errorf("isPoll(%q) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

// TestPollOf tests parsing of polls from Matrix event content maps.
func TestPollOf(t *testing.T) {
	// Nil / empty content
	if got := pollOf(nil); got != nil {
		t.Errorf("pollOf(nil) = %+v, want nil", got)
	}
	if got := pollOf(map[string]any{}); got != nil {
		t.Errorf("pollOf(empty) = %+v, want nil", got)
	}

	// MSC3381 poll: disclosed, single-choice, org.matrix.msc1767.text question and answers
	mscContent := map[string]any{
		pollStart: map[string]any{
			"question": map[string]any{
				pollText: "Favorite color?",
			},
			"kind":           pollOpen,
			"max_selections": float64(1),
			"answers": []any{
				map[string]any{"id": "a1", pollText: "Red"},
				map[string]any{"id": "a2", pollText: "Blue"},
			},
		},
	}
	p1 := pollOf(mscContent)
	if p1 == nil {
		t.Fatal("pollOf(mscContent) returned nil")
	}
	if p1.Question != "Favorite color?" || p1.Hidden || p1.Multi || len(p1.Answers) != 2 {
		t.Errorf("p1 = %+v", p1)
	}
	if p1.Answers[0].ID != "a1" || p1.Answers[0].Text != "Red" || p1.Answers[1].ID != "a2" || p1.Answers[1].Text != "Blue" {
		t.Errorf("p1.Answers = %+v", p1.Answers)
	}

	// Standard m.poll: undisclosed, multi-choice, fallback body for question and answers
	stdContent := map[string]any{
		"m.poll": map[string]any{
			"question": map[string]any{
				"body": "Preferred days?",
			},
			"kind":           "m.poll.undisclosed",
			"max_selections": float64(3),
			"answers": []any{
				map[string]any{"id": "mon", "body": "Monday"},
				map[string]any{"id": "tue", "body": "Tuesday"},
			},
		},
	}
	p2 := pollOf(stdContent)
	if p2 == nil {
		t.Fatal("pollOf(stdContent) returned nil")
	}
	if p2.Question != "Preferred days?" || !p2.Hidden || !p2.Multi || len(p2.Answers) != 2 {
		t.Errorf("p2 = %+v", p2)
	}

	// Invalid polls: missing question, or no answers, or answers without ID
	badCases := []struct {
		name    string
		content map[string]any
	}{
		{
			name: "empty question",
			content: map[string]any{
				pollStart: map[string]any{
					"question": map[string]any{pollText: ""},
					"answers":  []any{map[string]any{"id": "a1", pollText: "Ans"}},
				},
			},
		},
		{
			name: "no answers",
			content: map[string]any{
				pollStart: map[string]any{
					"question": map[string]any{pollText: "Q"},
					"answers":  []any{},
				},
			},
		},
		{
			name: "answers without id",
			content: map[string]any{
				pollStart: map[string]any{
					"question": map[string]any{pollText: "Q"},
					"answers":  []any{map[string]any{pollText: "Ans"}},
				},
			},
		},
	}
	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			if got := pollOf(bc.content); got != nil {
				t.Errorf("pollOf(%s) = %+v, want nil", bc.name, got)
			}
		})
	}
}

// TestPollMethods tests Poll.has() and Poll.Shown().
func TestPollMethods(t *testing.T) {
	p := &Poll{
		Question: "Test Poll",
		Answers: []PollAnswer{
			{ID: "opt1", Text: "Option 1"},
			{ID: "opt2", Text: "Option 2"},
		},
	}

	// has
	if !p.has("opt1") || !p.has("opt2") {
		t.Error("p.has() failed on existing answer")
	}
	if p.has("opt3") {
		t.Error("p.has('opt3') should be false")
	}

	// Shown() logic:
	// Ended -> true regardless of Voted or Hidden
	pEnded := &Poll{Ended: true, Voted: false, Hidden: true}
	if !pEnded.Shown() {
		t.Error("pEnded.Shown() should be true")
	}

	// Voted and not Hidden -> true
	pVotedOpen := &Poll{Ended: false, Voted: true, Hidden: false}
	if !pVotedOpen.Shown() {
		t.Error("pVotedOpen.Shown() should be true")
	}

	// Voted and Hidden -> false
	pVotedHidden := &Poll{Ended: false, Voted: true, Hidden: true}
	if pVotedHidden.Shown() {
		t.Error("pVotedHidden.Shown() should be false")
	}

	// Not voted and not Ended -> false
	pNotVoted := &Poll{Ended: false, Voted: false, Hidden: false}
	if pNotVoted.Shown() {
		t.Error("pNotVoted.Shown() should be false")
	}
}
