package bp

import (
	"context"
	"testing"

	"go24/internal/company"
)

// TestCheckCondition: evaluation of conditions with check helper (empty, filled, contains, eq, ne, gt, lt).
func TestCheckCondition(t *testing.T) {
	tests := []struct {
		v, op, want string
		expected    bool
	}{
		// empty
		{"", "empty", "", true},
		{"   ", "empty", "", true},
		{"value", "empty", "", false},

		// filled
		{"value", "filled", "", true},
		{"", "filled", "", false},
		{"   ", "filled", "", false},

		// contains
		{"Hello World", "contains", "world", true},
		{"Hello World", "contains", "WORLD", true},
		{"Hello World", "contains", "xyz", false},

		// eq
		{"10", "eq", "10.0", true},
		{"1,5", "eq", "1.50", true},
		{"10", "eq", "20", false},
		{"ABC", "eq", "abc", true},
		{"ABC", "eq", "xyz", false},

		// ne
		{"10", "ne", "20", true},
		{"10", "ne", "10.0", false},
		{"abc", "ne", "xyz", true},
		{"abc", "ne", "ABC", false},

		// gt
		{"10", "gt", "2", true}, // numeric: 10 > 2 (lexicographically "10" < "2")
		{"2", "gt", "10", false},
		{"zebra", "gt", "apple", true},
		{"apple", "gt", "zebra", false},
		{"2026-11-02", "gt", "2026-11-01", true},
		{"2026-11-01", "gt", "2026-11-02", false},

		// lt
		{"2", "lt", "10", true}, // numeric: 2 < 10
		{"10", "lt", "2", false},
		{"apple", "lt", "zebra", true},
		{"zebra", "lt", "apple", false},

		// unknown op
		{"10", "like", "10", false},
	}

	for _, tt := range tests {
		got := check(tt.v, tt.op, tt.want)
		if got != tt.expected {
			t.Errorf("check(%q, %q, %q) = %v, want %v", tt.v, tt.op, tt.want, got, tt.expected)
		}
	}
}

// TestJump: step index resolution for normal and parallel branch execution.
func TestJump(t *testing.T) {
	steps := make([]Step, 5)

	// Linear instance with 5 steps (indices 0..4, end = 5)
	inst := Instance{Steps: steps}
	if got := jump(inst, 0); got != 5 {
		t.Errorf("jump(0) = %d, want 5 (end)", got)
	}
	if got := jump(inst, -1); got != 5 {
		t.Errorf("jump(-1) = %d, want 5 (end)", got)
	}
	if got := jump(inst, 10); got != 5 {
		t.Errorf("jump(10) = %d, want 5 (end)", got)
	}
	if got := jump(inst, 1); got != 0 {
		t.Errorf("jump(1) = %d, want 0", got)
	}
	if got := jump(inst, 3); got != 2 {
		t.Errorf("jump(3) = %d, want 2", got)
	}
	if got := jump(inst, 5); got != 4 {
		t.Errorf("jump(5) = %d, want 4", got)
	}

	// Branch instance with branch end at index 3
	branchInst := Instance{Steps: steps, branch: 1, end: 3}
	if got := jump(branchInst, 0); got != 3 {
		t.Errorf("jump(branch, 0) = %d, want 3", got)
	}
	if got := jump(branchInst, 4); got != 3 {
		t.Errorf("jump(branch, 4) = %d, want 3 (clamped to end)", got)
	}
	if got := jump(branchInst, 2); got != 1 {
		t.Errorf("jump(branch, 2) = %d, want 1", got)
	}
}

// TestFill: text placeholder replacement and type-specific display formatting.
func TestFill(t *testing.T) {
	ctx := context.Background()
	s := &Store{
		Tr: func(key string, args ...any) string {
			if key == "bp.bool.Y" {
				return "Да"
			}
			return key
		},
	}
	inst := Instance{
		ID:        "proc-123",
		Name:      "Vacation Request",
		Entity:    "none",
		StartedBy: company.Person{ID: "usr-1", Name: "Ivan Ivanov"},
		Fields: []Field{
			{Key: "at", Label: "Date", Kind: "datetime"},
			{Key: "urgent", Label: "Urgent", Kind: "bool"},
			{Key: "note", Label: "Note", Kind: "string"},
		},
		Values: map[string]string{
			"at":     "2026-11-02T15:30",
			"urgent": "Y",
			"note":   "Urgent task",
		},
	}

	// fill (human-visible presentation)
	text := "Proc: {process}, Starter: {starter}, Link: {link}, At: {at}, Urgent: {urgent}, Note: {note}"
	got := s.fill(ctx, inst, text)
	want := "Proc: Vacation Request, Starter: Ivan Ivanov, Link: /bp/i/proc-123, At: 2026-11-02 15:30, Urgent: Да, Note: Urgent task"
	if got != want {
		t.Errorf("fill() = %q\nwant = %q", got, want)
	}

	// fillRaw (internal representation: keeps 'T' in datetime and raw bool)
	gotRaw := s.fillRaw(ctx, inst, "{at} {urgent}")
	wantRaw := "2026-11-02T15:30 Y"
	if gotRaw != wantRaw {
		t.Errorf("fillRaw() = %q, want %q", gotRaw, wantRaw)
	}
}

// TestCardPath: path resolution for files, lists, and CRM entity cards.
func TestCardPath(t *testing.T) {
	tests := []struct {
		entity, id string
		want       string
	}{
		{"file", "f-1", "/files/f-1/about"},
		{"list", "el-1", "/bp/el/el-1"},
		{"deal", "d-1", "/crm/deals/d-1"},
		{"lead", "l-1", "/crm/leads/l-1"},
		{"contact", "c-1", "/crm/contacts/c-1"},
		{"company", "co-1", "/crm/companies/co-1"},
	}

	for _, tt := range tests {
		if got := CardPath(tt.entity, tt.id); got != tt.want {
			t.Errorf("CardPath(%q, %q) = %q, want %q", tt.entity, tt.id, got, tt.want)
		}
	}
}
