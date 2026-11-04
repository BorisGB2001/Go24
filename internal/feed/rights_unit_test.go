package feed

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
)

func TestAudienceOpen(t *testing.T) {
	for _, c := range []struct {
		name string
		a    Audience
		want bool
	}{
		{"empty", Audience{}, true},
		{"empty slices", Audience{Users: []string{}, Depts: []string{}}, true},
		{"users only", Audience{Users: []string{"u1"}}, false},
		{"depts only", Audience{Depts: []string{"d1"}}, false},
		{"users and depts", Audience{Users: []string{"u1"}, Depts: []string{"d1"}}, false},
	} {
		if got := c.a.open(); got != c.want {
			t.Errorf("%s: Audience.open() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRightsJSON(t *testing.T) {
	orig := Rights{
		All:       Audience{Users: []string{"u1", "u2"}, Depts: []string{"d1"}},
		Important: Audience{Users: []string{"u3"}},
	}
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal(Rights) err = %v", err)
	}

	var parsed Rights
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("Unmarshal(Rights) err = %v", err)
	}

	if !slices.Equal(parsed.All.Users, orig.All.Users) ||
		!slices.Equal(parsed.All.Depts, orig.All.Depts) ||
		!slices.Equal(parsed.Important.Users, orig.Important.Users) {
		t.Errorf("roundtrip Rights mismatch: got %+v, want %+v", parsed, orig)
	}
}

func TestCheckRightsNoOp(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	// When neither toAll nor important is set, checkRights must return nil without touching DB
	if err := s.checkRights(ctx, "u1", false, false); err != nil {
		t.Errorf("checkRights(false, false) err = %v, want nil", err)
	}
}
