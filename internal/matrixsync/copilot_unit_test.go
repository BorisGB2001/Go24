package matrixsync

import (
	"reflect"
	"testing"

	"go24/internal/matrix"
)

// TestCopilotTurn tests converting Matrix timeline events to conversation Turns.
func TestCopilotTurn(t *testing.T) {
	botMXID := "@copilot:example.com"
	who := map[string]imPerson{
		"@alice:example.com": {Name: "Alice"},
	}

	// 1. Non-message event ignored
	evNonMsg := matrix.Event{Type: "m.room.member"}
	if _, ok := copilotTurn(evNonMsg, "", botMXID, false, who); ok {
		t.Error("non-message event should be rejected")
	}

	// 2. Notice or empty body ignored
	evNotice := matrix.Event{
		Type:    "m.room.message",
		Content: map[string]any{"msgtype": "m.notice", "body": "system notice"},
	}
	if _, ok := copilotTurn(evNotice, "", botMXID, false, who); ok {
		t.Error("notice should be rejected")
	}

	// 3. Edit (m.replace) ignored
	evReplace := matrix.Event{
		Type: "m.room.message",
		Content: map[string]any{
			"msgtype":      "m.text",
			"body":         "edit",
			"m.relates_to": map[string]any{"rel_type": "m.replace"},
		},
	}
	if _, ok := copilotTurn(evReplace, "", botMXID, false, who); ok {
		t.Error("replace edit should be rejected")
	}

	// 4. Thread matching:
	// Event in a thread when root is empty -> rejected
	evInThread := matrix.Event{
		Type: "m.room.message",
		Content: map[string]any{
			"body":         "reply in thread",
			"m.relates_to": map[string]any{"rel_type": "m.thread", "event_id": "$root1"},
		},
	}
	if _, ok := copilotTurn(evInThread, "", botMXID, false, who); ok {
		t.Error("thread message when root is empty should be rejected")
	}

	// Event matching the thread root
	if turn, ok := copilotTurn(evInThread, "$root1", botMXID, false, who); !ok || turn.Text != "reply in thread" {
		t.Errorf("evInThread matching root: got (%+v, %v)", turn, ok)
	}

	// 5. Bot message vs User message:
	evBot := matrix.Event{
		Type:    "m.room.message",
		Sender:  botMXID,
		Content: map[string]any{"body": "Hello human"},
	}
	turnBot, ok := copilotTurn(evBot, "", botMXID, false, who)
	if !ok || !turnBot.Bot || turnBot.Text != "Hello human" {
		t.Errorf("bot turn = (%+v, %v)", turnBot, ok)
	}

	// 6. Group chat prefix with user's name
	evUserGroup := matrix.Event{
		Type:    "m.room.message",
		Sender:  "@alice:example.com",
		Content: map[string]any{"body": "What is the status?"},
	}
	turnGroup, ok := copilotTurn(evUserGroup, "", botMXID, false, who)
	if !ok || turnGroup.Bot || turnGroup.Text != "Alice: What is the status?" {
		t.Errorf("group user turn = (%+v, %v)", turnGroup, ok)
	}

	// 7. Private chat does not prefix user's name
	turnPrivate, ok := copilotTurn(evUserGroup, "", botMXID, true, who)
	if !ok || turnPrivate.Text != "What is the status?" {
		t.Errorf("private user turn = (%+v, %v)", turnPrivate, ok)
	}
}

// TestCopilotMergeTurns verifies turn merging, dropping leading bot turns, and alternating turn construction.
func TestCopilotMergeTurns(t *testing.T) {
	tests := []struct {
		name  string
		input []Turn
		want  []Turn
	}{
		{
			name:  "empty",
			input: nil,
			want:  nil,
		},
		{
			name: "drops leading bot turns",
			input: []Turn{
				{Bot: true, Text: "Initial greeting"},
				{Bot: true, Text: "Second greeting"},
				{Bot: false, Text: "User question"},
				{Bot: true, Text: "Bot answer"},
			},
			want: []Turn{
				{Bot: false, Text: "User question"},
				{Bot: true, Text: "Bot answer"},
			},
		},
		{
			name: "merges consecutive turns of the same speaker",
			input: []Turn{
				{Bot: false, Text: "Part 1"},
				{Bot: false, Text: "Part 2"},
				{Bot: true, Text: "Answer A"},
				{Bot: true, Text: "Answer B"},
			},
			want: []Turn{
				{Bot: false, Text: "Part 1\n\nPart 2"},
				{Bot: true, Text: "Answer A\n\nAnswer B"},
			},
		},
		{
			name: "only bot turns",
			input: []Turn{
				{Bot: true, Text: "Greeting 1"},
				{Bot: true, Text: "Greeting 2"},
			},
			want: nil,
		},
		{
			name: "alternating turns preserved",
			input: []Turn{
				{Bot: false, Text: "Q1"},
				{Bot: true, Text: "A1"},
				{Bot: false, Text: "Q2"},
				{Bot: true, Text: "A2"},
			},
			want: []Turn{
				{Bot: false, Text: "Q1"},
				{Bot: true, Text: "A1"},
				{Bot: false, Text: "Q2"},
				{Bot: true, Text: "A2"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeTurns(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("mergeTurns() =\n%+v\nwant:\n%+v", got, tt.want)
			}
		})
	}
}
