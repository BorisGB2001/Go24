package crm

import (
	"strings"
	"testing"
)

func TestParseFill_Unit(t *testing.T) {
	jsonBody := `{"call":"call-123","items":[{"f":"amount","n":"1500","s":"offered"},{"f":"comment","n":"Заметка","s":"filled"}]}`
	f := ParseFill(jsonBody)
	if f.Call != "call-123" {
		t.Errorf("Fill.Call = %q, want \"call-123\"", f.Call)
	}
	if len(f.Items) != 2 {
		t.Fatalf("Fill.Items len = %d, want 2", len(f.Items))
	}
	if f.Items[0].Field != "amount" || f.Items[0].State != "offered" {
		t.Errorf("Item 0 mismatch: %+v", f.Items[0])
	}
	if f.Offered() != 1 {
		t.Errorf("Fill.Offered() = %d, want 1", f.Offered())
	}

	// Invalid json returns empty Fill without panicking
	empty := ParseFill("invalid-json")
	if empty.Call != "" || len(empty.Items) != 0 {
		t.Errorf("expected empty Fill on invalid json, got %+v", empty)
	}
}

func TestFillValue_Unit(t *testing.T) {
	tests := []struct {
		name    string
		field   FillField
		val     string
		wantVal string
		wantOK  bool
	}{
		{
			name:    "empty value",
			field:   FillField{Kind: "string"},
			val:     "   ",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "number clean with spaces and comma",
			field:   FillField{Kind: "number"},
			val:     " 1 500,50 ",
			wantVal: "1500.5",
			wantOK:  true,
		},
		{
			name:    "negative number rejected",
			field:   FillField{Kind: "number"},
			val:     "-100",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "invalid number string rejected",
			field:   FillField{Kind: "number"},
			val:     "abc",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "date valid ISO",
			field:   FillField{Kind: "date"},
			val:     "2026-10-28T15:00:00Z",
			wantVal: "2026-10-28",
			wantOK:  true,
		},
		{
			name:    "date too short",
			field:   FillField{Kind: "date"},
			val:     "2026-10",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "date invalid day",
			field:   FillField{Kind: "date"},
			val:     "2026-02-31",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "bool 1",
			field:   FillField{Kind: "bool"},
			val:     "1",
			wantVal: "1",
			wantOK:  true,
		},
		{
			name:    "bool true",
			field:   FillField{Kind: "bool"},
			val:     "True",
			wantVal: "1",
			wantOK:  true,
		},
		{
			name:    "bool yes",
			field:   FillField{Kind: "bool"},
			val:     "YES",
			wantVal: "1",
			wantOK:  true,
		},
		{
			name:    "bool russian da",
			field:   FillField{Kind: "bool"},
			val:     "Да",
			wantVal: "1",
			wantOK:  true,
		},
		{
			name:    "bool false rejected",
			field:   FillField{Kind: "bool"},
			val:     "false",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "list matches case insensitively and returns canonical option",
			field:   FillField{Kind: "list", Options: []string{"Красный", "Синий"}},
			val:     "красный",
			wantVal: "Красный",
			wantOK:  true,
		},
		{
			name:    "list no match returns false",
			field:   FillField{Kind: "list", Options: []string{"Красный", "Синий"}},
			val:     "Зеленый",
			wantVal: "",
			wantOK:  false,
		},
		{
			name:    "string truncated to 2000 runes",
			field:   FillField{Kind: "string"},
			val:     strings.Repeat("ж", 2500),
			wantVal: strings.Repeat("ж", 2000),
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotVal, gotOK := FillValue(tt.field, tt.val)
			if gotOK != tt.wantOK {
				t.Fatalf("FillValue() ok = %v, wantOK %v", gotOK, tt.wantOK)
			}
			if gotVal != tt.wantVal {
				t.Errorf("FillValue() val = %q, wantVal %q", gotVal, tt.wantVal)
			}
		})
	}
}
