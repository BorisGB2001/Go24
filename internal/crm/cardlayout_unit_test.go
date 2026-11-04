package crm

import (
	"reflect"
	"slices"
	"testing"
)

func TestCardScope_Unit(t *testing.T) {
	if got := CardScope("deal", "type-1"); got != "type:type-1" {
		t.Errorf("CardScope deal with type = %q, want \"type:type-1\"", got)
	}
	if got := CardScope("lead", ""); got != "lead" {
		t.Errorf("CardScope lead = %q, want \"lead\"", got)
	}
}

func TestDefaultCardLayout_Unit(t *testing.T) {
	layout := DefaultCardLayout("lead", []string{"f1", "f2"})
	if len(layout.Sections) != 2 {
		t.Fatalf("DefaultCardLayout sections len = %d, want 2", len(layout.Sections))
	}
	if layout.Sections[0].Code != "main" || layout.Sections[1].Code != "more" {
		t.Errorf("section codes mismatch: %+v", layout.Sections)
	}
	if !reflect.DeepEqual(layout.Sections[1].Fields, []string{"f:f1", "f:f2"}) {
		t.Errorf("custom fields mismatch: %v", layout.Sections[1].Fields)
	}
	for _, f := range CardFields["lead"] {
		if !slices.Contains(layout.Sections[0].Fields, f) {
			t.Errorf("main section missing lead card field %q", f)
		}
	}
}

func TestCardLayoutClean_EdgeCases_Unit(t *testing.T) {
	// Layout with empty sections and unknown fields
	l := CardLayout{
		Sections: []CardSection{
			{Name: "  Пустая секция  ", Fields: []string{"unknown_field", "f:unknown"}},
		},
		Hidden: []string{"deal_type", "invalid_hidden"},
	}
	cleaned := l.clean("deal", []string{"known_1"})

	// Verify section name trimmed
	if cleaned.Sections[0].Name != "Пустая секция" {
		t.Errorf("section name = %q, want trimmed", cleaned.Sections[0].Name)
	}
	// Verify unknown_field dropped
	for _, s := range cleaned.Sections {
		if slices.Contains(s.Fields, "unknown_field") || slices.Contains(s.Fields, "f:unknown") {
			t.Errorf("cleaned layout kept unknown fields: %+v", s.Fields)
		}
	}
	// Verify known custom field "f:known_1" placed in last section
	lastSec := cleaned.Sections[len(cleaned.Sections)-1]
	if !slices.Contains(lastSec.Fields, "f:known_1") {
		t.Errorf("last section missing unplaced custom field f:known_1: %+v", lastSec.Fields)
	}
	// Verify valid hidden field kept and invalid dropped
	if !reflect.DeepEqual(cleaned.Hidden, []string{"deal_type"}) {
		t.Errorf("cleaned.Hidden = %v, want [\"deal_type\"]", cleaned.Hidden)
	}
}
