package crm

import (
	"reflect"
	"testing"
)

func TestWatched_Unit(t *testing.T) {
	if !watched("deal") {
		t.Error("watched(\"deal\") = false, want true")
	}
	if !watched("lead") {
		t.Error("watched(\"lead\") = false, want true")
	}
	if watched("contact") || watched("company") || watched("invoice") || watched("") {
		t.Error("watched should be false for non-deal, non-lead entities")
	}
}

func TestObserverSQL_Unit(t *testing.T) {
	sql := observerSQL("deal", "d", "$3")
	want := `d.id IN (SELECT item_id FROM crm_deal_observers WHERE user_id::text = $3)`
	if sql != want {
		t.Errorf("observerSQL() = %q, want %q", sql, want)
	}
}

func TestObserverIDs_Unit(t *testing.T) {
	refs := []Ref{
		{ID: "u1", Name: "User 1"},
		{ID: "u2", Name: "User 2"},
		{ID: "u3", Name: "User 3"},
	}
	ids := ObserverIDs(refs)
	want := []string{"u1", "u2", "u3"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("ObserverIDs() = %v, want %v", ids, want)
	}

	if gotEmpty := ObserverIDs(nil); len(gotEmpty) != 0 {
		t.Errorf("ObserverIDs(nil) = %v, want empty", gotEmpty)
	}
}
