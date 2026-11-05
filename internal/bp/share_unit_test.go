package bp

import (
	"testing"
)

// TestPerson: identification of direct employee references versus special roles and field variables.
func TestPerson(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"starter", false},
		{"manager", false},
		{"responsible", false},
		{"client", false},
		{"field:assignee", false},
		{"field:user_id", false},
		{"usr-123", true},
		{"123e4567-e89b-12d3-a456-426614174000", true},
		{"admin@example.com", true},
	}

	for _, tt := range tests {
		if got := person(tt.input); got != tt.want {
			t.Errorf("person(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestEachPerson: traversal and mutation of employees in step To, From, Users, and Watchers.
func TestEachPerson(t *testing.T) {
	step := Step{
		To:       "u-1",
		From:     "manager", // role keyword: should not be transformed
		Users:    []string{"u-2", "starter", "drop-me"},
		Watchers: []string{"u-3", "field:resp"},
	}

	// Mapping function that maps u-X to mapped-X, drops "drop-me" (by returning ""), and prefixes others
	mapping := func(id string) string {
		if id == "drop-me" {
			return ""
		}
		return "mapped-" + id
	}

	eachPerson(&step, mapping)

	if step.To != "mapped-u-1" {
		t.Errorf("step.To = %q, want 'mapped-u-1'", step.To)
	}
	if step.From != "manager" {
		t.Errorf("step.From = %q, want 'manager'", step.From)
	}

	// Users: "u-2" becomes "mapped-u-2", "starter" remains untouched, "drop-me" is removed
	expectedUsers := []string{"mapped-u-2", "starter"}
	if len(step.Users) != len(expectedUsers) {
		t.Fatalf("step.Users len = %d, want %d", len(step.Users), len(expectedUsers))
	}
	for i, u := range step.Users {
		if u != expectedUsers[i] {
			t.Errorf("step.Users[%d] = %q, want %q", i, u, expectedUsers[i])
		}
	}

	// Watchers: "u-3" becomes "mapped-u-3", "field:resp" untouched
	if len(step.Watchers) != 2 || step.Watchers[0] != "mapped-u-3" || step.Watchers[1] != "field:resp" {
		t.Errorf("step.Watchers mismatch: %+v", step.Watchers)
	}
}

// TestEachDefault: transformation of user field defaults in template form and request steps.
func TestEachDefault(t *testing.T) {
	tpl := Template{
		Fields: []Field{
			{Key: "assignee", Kind: "user", Default: "u-10"},
			{Key: "count", Kind: "number", Default: "5"}, // non-user: unchanged
			{Key: "empty_user", Kind: "user", Default: ""},
		},
		Steps: []Step{
			{
				Kind: "request",
				Fields: []Field{
					{Key: "req_approver", Kind: "user", Default: "u-20"},
					{Key: "comment", Kind: "text", Default: "n/a"},
				},
			},
			{
				Kind: "notify", // has no fields
			},
		},
	}

	eachDefault(&tpl, func(old string) string {
		return "new-" + old
	})

	if tpl.Fields[0].Default != "new-u-10" {
		t.Errorf("form user field default = %q, want 'new-u-10'", tpl.Fields[0].Default)
	}
	if tpl.Fields[1].Default != "5" {
		t.Errorf("form number field default = %q, want '5'", tpl.Fields[1].Default)
	}
	if tpl.Fields[2].Default != "" {
		t.Errorf("form empty user field default = %q, want ''", tpl.Fields[2].Default)
	}
	if tpl.Steps[0].Fields[0].Default != "new-u-20" {
		t.Errorf("step user field default = %q, want 'new-u-20'", tpl.Steps[0].Fields[0].Default)
	}
	if tpl.Steps[0].Fields[1].Default != "n/a" {
		t.Errorf("step text field default = %q, want 'n/a'", tpl.Steps[0].Fields[1].Default)
	}
}

