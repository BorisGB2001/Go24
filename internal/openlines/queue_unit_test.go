package openlines

import (
	"testing"
)

func TestContains(t *testing.T) {
	list := []string{"alice", "bob", "carol"}
	if !contains(list, "alice") || !contains(list, "bob") || !contains(list, "carol") {
		t.Errorf("contains(%v) failed for existing element", list)
	}
	if contains(list, "dave") {
		t.Errorf("contains(%v, 'dave') must be false", list)
	}
	if contains(list, "ALICE") {
		t.Errorf("contains(%v, 'ALICE') must be false (case sensitive)", list)
	}
	if contains(nil, "alice") {
		t.Error("contains(nil) must be false")
	}
	if contains([]string{}, "alice") {
		t.Error("contains([]) must be false")
	}
}
