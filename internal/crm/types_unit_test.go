package crm

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestTypeName_Unit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:    "valid type name",
			input:   "  Договоры подряда  ",
			want:    "Договоры подряда",
			wantErr: false,
		},
		{
			name:    "empty type name returns err.crm_type_name",
			input:   "   ",
			wantErr: true,
		},
		{
			name:    "name over 100 runes returns err.crm_type_name",
			input:   strings.Repeat("т", 101),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := typeName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("typeName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if tt.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != "err.crm_type_name" {
					t.Errorf("expected err.crm_type_name, got %v", err)
				}
			} else if got != tt.want {
				t.Errorf("typeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
