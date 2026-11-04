package sign

import "testing"

// TestPerms: unknown levels fall to the least, a person has the highest level of each right of their roles, Has
// compares the levels in order.
func TestPerms(t *testing.T) {
	p := Perms{"docs": "everything", "send": "all"}.clean()
	if p["docs"] != "own" || p["edit"] != "own" || p["send"] != "all" || p["templates"] != "own" || p["hr"] != "none" {
		t.Fatalf("clean: %v", p)
	}
	p.higher(Perms{"docs": "dept", "edit": "own", "send": "none", "hr": "all"}.clean())
	if p["docs"] != "dept" || p["send"] != "all" || p["hr"] != "all" {
		t.Fatalf("higher: %v", p)
	}
	if !p.Has("docs", "own") || !p.Has("docs", "dept") || p.Has("docs", "all") || p.Has("templates", "all") || p.Has("nope", "all") {
		t.Fatalf("has: %v", p)
	}
	a := AllPerms()
	for _, r := range Rights {
		if a[r.Key] != r.Levels[len(r.Levels)-1] {
			t.Fatalf("all: %v", a)
		}
	}
}
