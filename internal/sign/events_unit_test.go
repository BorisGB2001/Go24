package sign

import (
	"testing"
	"time"
)

func TestEventKinds(t *testing.T) {
	kinds := map[string]string{
		"EvCreated":   EvCreated,
		"EvSent":      EvSent,
		"EvReminded":  EvReminded,
		"EvViewed":    EvViewed,
		"EvCode":      EvCode,
		"EvSigned":    EvSigned,
		"EvRefused":   EvRefused,
		"EvApproved":  EvApproved,
		"EvRejected":  EvRejected,
		"EvCancelled": EvCancelled,
		"EvDone":      EvDone,
	}
	expected := map[string]string{
		"EvCreated":   "created",
		"EvSent":      "sent",
		"EvReminded":  "reminded",
		"EvViewed":    "viewed",
		"EvCode":      "code",
		"EvSigned":    "signed",
		"EvRefused":   "refused",
		"EvApproved":  "approved",
		"EvRejected":  "rejected",
		"EvCancelled": "cancelled",
		"EvDone":      "done",
	}
	for name, val := range kinds {
		if val != expected[name] {
			t.Errorf("%s = %q, want %q", name, val, expected[name])
		}
	}
}

func TestEventStruct(t *testing.T) {
	now := time.Now()
	ev := Event{
		Kind:    EvSigned,
		PartyID: "p-1",
		Party:   "Alice",
		ActorID: "u-1",
		Actor:   "Bob",
		OK:      true,
		IP:      "192.168.1.1",
		Note:    "signed via portal",
		At:      now,
	}
	if ev.Kind != EvSigned || ev.PartyID != "p-1" || ev.Party != "Alice" ||
		ev.ActorID != "u-1" || ev.Actor != "Bob" || !ev.OK ||
		ev.IP != "192.168.1.1" || ev.Note != "signed via portal" || !ev.At.Equal(now) {
		t.Errorf("Event struct fields mismatch: %+v", ev)
	}
}
