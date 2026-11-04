package crm

import (
	"slices"
	"testing"
)

func TestColumnScopeAndKind_Unit(t *testing.T) {
	tests := []struct {
		entity    string
		typeID    string
		wantScope string
		wantKind  string
	}{
		{"deal", "", "deal", "deal"},
		{"deal", "sp-uuid-1", "type:sp-uuid-1", "item"},
		{"lead", "", "lead", "lead"},
		{"contact", "", "contact", "contact"},
		{"company", "", "company", "company"},
		{"company", "ignored", "company", "company"}, // typeID only applies to deal
	}

	for _, tt := range tests {
		scope := ColumnScope(tt.entity, tt.typeID)
		if scope != tt.wantScope {
			t.Errorf("ColumnScope(%q, %q) = %q, want %q", tt.entity, tt.typeID, scope, tt.wantScope)
		}
		kind := ColumnKind(scope)
		if kind != tt.wantKind {
			t.Errorf("ColumnKind(%q) = %q, want %q", scope, kind, tt.wantKind)
		}
		if pScope := PresetScope(tt.entity, tt.typeID); pScope != tt.wantScope {
			t.Errorf("PresetScope(%q, %q) = %q, want %q", tt.entity, tt.typeID, pScope, tt.wantScope)
		}
	}
}

func TestListColumns_Unit(t *testing.T) {
	for kind, defaults := range DefaultListColumns {
		allCols := ListColumns[kind]
		if allCols == nil {
			t.Errorf("ListColumns missing for kind %q", kind)
			continue
		}
		for _, def := range defaults {
			if !slices.Contains(allCols, def) {
				t.Errorf("Default column %q not in ListColumns[%q]", def, kind)
			}
		}
	}
}
