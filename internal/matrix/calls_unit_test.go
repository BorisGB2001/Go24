package matrix

import "testing"

// TestAllowCalls: the call events of Element take the level of messages — 0 in a chat, a channel's admins in a
// channel; levels already so are no change.
func TestAllowCalls(t *testing.T) {
	pl := PowerLevels{"events_default": float64(0), "events": map[string]any{"m.room.name": float64(50)}}
	if !AllowCalls(pl) {
		t.Fatal("a room without the call levels: no change")
	}
	ev := pl["events"].(map[string]any)
	if ev[CallType] != float64(0) || ev[CallMemberType] != float64(0) || ev["m.room.name"] != float64(50) {
		t.Fatalf("the levels: %v", ev)
	}
	if AllowCalls(pl) {
		t.Fatal("the same levels again: a change")
	}
	channel := PowerLevels{"events_default": 50}
	if !AllowCalls(channel) || channel["events"].(map[string]any)[CallMemberType] != float64(50) {
		t.Fatalf("a channel: %v", channel)
	}
	made := PowerLevels{"events_default": 0, "events": map[string]any{CallType: 0, CallMemberType: 0}}
	if AllowCalls(made) {
		t.Fatal("levels go24 put (int): a change")
	}
}
