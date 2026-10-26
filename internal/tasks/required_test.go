package tasks

import "testing"

// TestRequiredAt: a required field must be filled at its stages (at every one when none), a yes/no one never.
func TestRequiredAt(t *testing.T) {
	for _, c := range []struct {
		f     Field
		stage string
		want  bool
	}{
		{Field{Kind: "string"}, "a", false},
		{Field{Kind: "string", Required: true}, "a", true},
		{Field{Kind: "string", Required: true}, "", true},
		{Field{Kind: "list", Required: true, Stages: []string{"b"}}, "a", false},
		{Field{Kind: "list", Required: true, Stages: []string{"b"}}, "b", true},
		{Field{Kind: "string", Required: true, Stages: []string{"b"}}, "", false},
		{Field{Kind: "bool", Required: true}, "a", false},
	} {
		if got := c.f.RequiredAt(c.stage); got != c.want {
			t.Errorf("%+v at %q: %v", c.f, c.stage, got)
		}
	}
	if got := stageList(true, []string{" b", "", "b", "c"}); len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("stageList: %v", got)
	}
	if got := stageList(false, []string{"b"}); len(got) != 0 {
		t.Errorf("stageList of a field not required: %v", got)
	}
}
