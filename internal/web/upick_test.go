package web

import (
	"testing"

	"go24/internal/tasks"
)

// TestUserPick: the chosen IDs get their names from the list or the fixed choices; IDs it does not know are left
// for the script to name; a single choice keeps one person; a mistaken option is an error, not a broken page.
func TestUserPick(t *testing.T) {
	users := []tasks.User{{ID: "u1", Name: "Анна"}, {ID: "u2", Name: "Пётр"}}
	p, err := newUserPick("auditors", []string{"u2", "gone"}, users, "multi", "ph", "Кто?")
	if err != nil || !p.Multiple || p.Placeholder != "Кто?" || len(p.Sel) != 2 || p.Sel[0].Name != "Пётр" || p.Sel[1].Name != "" || !p.Missing {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = newUserPick("to", "starter", users, "required", "+starter", "Инициатор", "noself")
	if err != nil || !p.Required || p.Self || len(p.Sel) != 1 || p.Sel[0].Name != "Инициатор" || p.Missing {
		t.Fatalf("%+v %v", p, err)
	}
	p, err = newUserPick("members", map[string]bool{"u1": true, "u2": true}, users)
	if err != nil || len(p.Sel) != 1 || p.Sel[0].ID != "u1" {
		t.Fatalf("single keeps one: %+v %v", p, err)
	}
	if p, err = newUserPick("x", "", nil); err != nil || len(p.Sel) != 0 {
		t.Fatalf("nothing chosen: %+v %v", p, err)
	}
	if _, err := newUserPick("x", "", users, "multy"); err == nil {
		t.Fatal("an unknown option")
	}
	if _, err := newUserPick("x", 5, users); err == nil {
		t.Fatal("a selection of a wrong type")
	}
}
