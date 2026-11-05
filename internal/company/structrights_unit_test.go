package company

import (
	"slices"
	"testing"
)

// TestStructRightsScope: Scope retrieves the action scope for a given role.
func TestStructRightsScope(t *testing.T) {
	r := StructRights{
		"head": {
			"create": "own",
			"edit":   "all",
		},
		"employee": {
			"create": "",
		},
	}
	if got := r.Scope("head", "create"); got != "own" {
		t.Errorf("Scope(head, create) = %q, want own", got)
	}
	if got := r.Scope("head", "edit"); got != "all" {
		t.Errorf("Scope(head, edit) = %q, want all", got)
	}
	if got := r.Scope("head", "delete"); got != "" {
		t.Errorf("Scope(head, delete) = %q, want empty", got)
	}
	if got := r.Scope("deputy", "create"); got != "" {
		t.Errorf("Scope(deputy, create) = %q, want empty", got)
	}
}

// TestStructAccessCanAndAny: Can and Any evaluate permissions across global and department scopes.
func TestStructAccessCanAndAny(t *testing.T) {
	// Denied (guest or inactive)
	aNone := StructAccess{none: true}
	if aNone.Can("create", "dept1") || aNone.Any("create") {
		t.Errorf("StructAccess with none=true should deny all actions")
	}

	// Full access
	full := FullStructAccess()
	for _, act := range StructActions {
		if !full.Can(act, "dept99") {
			t.Errorf("FullStructAccess should allow Can(%q)", act)
		}
		if !full.Any(act) {
			t.Errorf("FullStructAccess should allow Any(%q)", act)
		}
	}

	// Partial scoped access: "edit" everywhere, "delete" in dept1 only
	a := StructAccess{
		All: map[string]bool{"edit": true},
		Own: map[string]map[string]bool{
			"delete": {"dept1": true},
		},
	}

	// Edit is allowed everywhere
	if !a.Can("edit", "dept1") || !a.Can("edit", "dept2") || !a.Any("edit") {
		t.Errorf("a should allow edit everywhere")
	}

	// Delete is allowed in dept1, but not dept2
	if !a.Can("delete", "dept1") {
		t.Errorf("a should allow delete in dept1")
	}
	if a.Can("delete", "dept2") {
		t.Errorf("a should NOT allow delete in dept2")
	}
	if !a.Any("delete") {
		t.Errorf("a.Any(delete) should be true")
	}

	// Create is allowed nowhere
	if a.Can("create", "dept1") || a.Any("create") {
		t.Errorf("a should not allow create")
	}
}

// TestWorktimeScopes: WorktimeScopes restricts settings to all or none.
func TestWorktimeScopes(t *testing.T) {
	if got := WorktimeScopes("settings"); !slices.Equal(got, []string{"", "all"}) {
		t.Errorf("WorktimeScopes(settings) = %v, want [, all]", got)
	}
	if got := WorktimeScopes("view"); !slices.Equal(got, StructScopes) {
		t.Errorf("WorktimeScopes(view) = %v, want StructScopes", got)
	}
	if got := WorktimeScopes("edit"); !slices.Equal(got, StructScopes) {
		t.Errorf("WorktimeScopes(edit) = %v, want StructScopes", got)
	}
}
