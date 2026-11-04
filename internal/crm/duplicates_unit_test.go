package crm

import (
	"slices"
	"testing"
)

func TestPhoneKey_Unit(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{
			name:  "empty",
			phone: "",
			want:  "",
		},
		{
			name:  "letters and symbols only",
			phone: "abc - () / #",
			want:  "",
		},
		{
			name:  "too short under 7 digits",
			phone: "123456",
			want:  "",
		},
		{
			name:  "exactly 7 digits",
			phone: "1234567",
			want:  "1234567",
		},
		{
			name:  "8 digits with spaces and hyphens",
			phone: "123-45-678",
			want:  "12345678",
		},
		{
			name:  "standard 10 digits",
			phone: "9001234567",
			want:  "9001234567",
		},
		{
			name:  "russian phone with country code +7",
			phone: "+7 (900) 123-45-67",
			want:  "9001234567",
		},
		{
			name:  "russian phone with 8 prefix",
			phone: "8 900 123 45 67",
			want:  "9001234567",
		},
		{
			name:  "long international number takes last 10 digits",
			phone: "+1 234 567 890 1234", // 13 digits
			want:  "45678901234"[1:],     // last 10 digits
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := phoneKey(tt.phone)
			if got != tt.want {
				t.Errorf("phoneKey(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}

func TestDuplicatePath_Unit(t *testing.T) {
	tests := []struct {
		name string
		dup  Duplicate
		want string
	}{
		{
			name: "deal duplicate path",
			dup:  Duplicate{Entity: "deal", ID: "123"},
			want: "/crm/deals/123",
		},
		{
			name: "lead duplicate path",
			dup:  Duplicate{Entity: "lead", ID: "456"},
			want: "/crm/leads/456",
		},
		{
			name: "contact duplicate path",
			dup:  Duplicate{Entity: "contact", ID: "789"},
			want: "/crm/contacts/789",
		},
		{
			name: "company duplicate path",
			dup:  Duplicate{Entity: "company", ID: "101"},
			want: "/crm/companies/101",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.dup.Path(); got != tt.want {
				t.Errorf("Duplicate.Path() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDupPair_Unit(t *testing.T) {
	tests := []struct {
		a, b string
		want [2]string
	}{
		{"alpha", "beta", [2]string{"alpha", "beta"}},
		{"beta", "alpha", [2]string{"alpha", "beta"}},
		{"same", "same", [2]string{"same", "same"}},
		{"10", "2", [2]string{"10", "2"}}, // lexical comparison "10" < "2"
		{"2", "10", [2]string{"10", "2"}},
	}

	for _, tt := range tests {
		got := dupPair(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("dupPair(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestPhoneSQL_Unit(t *testing.T) {
	got := phoneSQL("c.phone")
	want := `right(regexp_replace(c.phone, '\D', '', 'g'), 10)`
	if got != want {
		t.Errorf("phoneSQL() = %q, want %q", got, want)
	}
}

func TestDupCriteria_Unit(t *testing.T) {
	expectedCriteria := map[string][]string{
		"contact": {"phone", "email", "name", "inn"},
		"company": {"phone", "email", "name", "inn"},
		"lead":    {"phone", "email", "name"},
	}
	for entity, criteria := range expectedCriteria {
		got := DupCriteria[entity]
		if !slices.Equal(got, criteria) {
			t.Errorf("DupCriteria[%q] = %v, want %v", entity, got, criteria)
		}
	}
}
