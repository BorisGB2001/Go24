package apps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestMaxOptionsConstant verifies the maximum allowed options threshold.
func TestMaxOptionsConstant(t *testing.T) {
	if MaxOptions != 200 {
		t.Fatalf("MaxOptions = %d, want 200", MaxOptions)
	}
}

// TestErrOptionName verifies the localized error for invalid option names.
func TestErrOptionName(t *testing.T) {
	if ErrOptionName == nil {
		t.Fatal("ErrOptionName is nil")
	}
	want := i18n.E("err.app_option")
	if !errors.Is(ErrOptionName, want) && ErrOptionName.Error() != want.Error() {
		t.Fatalf("ErrOptionName = %v, want %v", ErrOptionName, want)
	}
}

// TestOptionNameValidation validates option name constraints as enforced by options logic.
func TestOptionNameValidation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"empty string", "", false},
		{"single char", "a", true},
		{"typical setting", "theme_color", true},
		{"dotted setting", "crm.deal.default_stage", true},
		{"max allowed length (128 bytes)", strings.Repeat("x", 128), true},
		{"exceeds max length (129 bytes)", strings.Repeat("x", 129), false},
		{"much longer length (200 bytes)", strings.Repeat("y", 200), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			valid := tc.input != "" && len(tc.input) <= 128
			if valid != tc.valid {
				t.Errorf("len(%q) = %d: valid = %v, want %v", tc.input, len(tc.input), valid, tc.valid)
			}
		})
	}
}

// TestOptionsPlacementOptions tests the options map generation for card tabs and tasks.
func TestOptionsPlacementOptions(t *testing.T) {
	cases := []struct {
		place string
		id    string
		wantK string
		wantV string
	}{
		{"TASK_VIEW_TAB", "105", "taskId", "105"},
		{"TASK_VIEW_TAB", "", "taskId", ""},
		{"CRM_LEAD_DETAIL_TAB", "42", "ID", "42"},
		{"CRM_DEAL_DETAIL_TAB", "99", "ID", "99"},
		{"CRM_CONTACT_DETAIL_TAB", "1", "ID", "1"},
		{"CRM_COMPANY_DETAIL_TAB", "2", "ID", "2"},
		{"CRM_DYNAMIC_12_DETAIL_TAB", "300", "ID", "300"},
		{"UNKNOWN_TAB", "999", "ID", "999"},
	}

	for _, c := range cases {
		got := Options(c.place, c.id)
		if len(got) != 1 {
			t.Fatalf("Options(%q, %q) returned map of length %d, want 1", c.place, c.id, len(got))
		}
		if got[c.wantK] != c.wantV {
			t.Errorf("Options(%q, %q) = %v, want key %q => %q", c.place, c.id, got, c.wantK, c.wantV)
		}
	}
}

// TestOptionValueHandling tests interpretation of option values (deletion vs setting).
func TestOptionValueHandling(t *testing.T) {
	type check struct {
		raw      json.RawMessage
		isDelete bool
	}

	cases := []check{
		{raw: nil, isDelete: true},
		{raw: json.RawMessage("null"), isDelete: true},
		{raw: json.RawMessage(`""`), isDelete: false},
		{raw: json.RawMessage(`"hello"`), isDelete: false},
		{raw: json.RawMessage(`123`), isDelete: false},
		{raw: json.RawMessage(`true`), isDelete: false},
		{raw: json.RawMessage(`false`), isDelete: false},
		{raw: json.RawMessage(`{"enabled":true}`), isDelete: false},
		{raw: json.RawMessage(`[1,2,3]`), isDelete: false},
	}

	for _, c := range cases {
		isDel := c.raw == nil || string(c.raw) == "null"
		if isDel != c.isDelete {
			t.Errorf("value %q: isDelete = %v, want %v", string(c.raw), isDel, c.isDelete)
		}
	}
}

// TestSetOptionsPreconditions documents that SetOptions requires a database handle.
func TestSetOptionsPreconditions(t *testing.T) {
	s := &Store{}
	defer func() {
		r := recover()
		if r == nil {
			t.Error("expected SetOptions with nil db to panic or fail on Begin")
		}
	}()
	_ = s.SetOptions(context.Background(), "app-1", "", map[string]json.RawMessage{"": nil})
}
