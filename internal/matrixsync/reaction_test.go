package matrixsync

import (
	"testing"

	"go24/internal/matrix"
)

// TestApplyReactions: reactions of a page land on their messages, counted per emoji, with names and «mine».
func TestApplyReactions(t *testing.T) {
	react := func(id, sender, target, key string) matrix.Event {
		return matrix.Event{EventID: id, Type: "m.reaction", Sender: sender, Content: map[string]any{
			"m.relates_to": map[string]any{"rel_type": "m.annotation", "event_id": target, "key": key}}}
	}
	// /messages order: newest first
	evs := []matrix.Event{react("$r3", "@me:x", "$m1", "👍"), react("$r2", "@bob:x", "$m1", "🔥"), react("$r1", "@bob:x", "$m1", "👍"),
		react("$r0", "@bob:x", "$gone", "👍")}
	msgs := []Message{{ID: "$m1"}, {ID: "$m2"}}
	applyReactions(evs, msgs, "@me:x", map[string]imPerson{"@bob:x": {Name: "Боб"}})
	r := msgs[0].Reactions
	if len(r) != 2 || r[0].Key != "👍" || r[0].Count != 2 || !r[0].Mine || r[0].Names != "Боб, me" || r[1].Key != "🔥" || r[1].Mine {
		t.Fatalf("reactions = %+v", r)
	}
	if len(msgs[1].Reactions) != 0 {
		t.Fatalf("a message without reactions got %+v", msgs[1].Reactions)
	}
}
