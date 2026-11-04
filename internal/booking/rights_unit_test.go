package booking

import (
	"context"
	"testing"
)

// TestAudienceEmpty tests checking if an audience configuration is unconstrained.
func TestAudienceEmpty(t *testing.T) {
	tests := []struct {
		name string
		aud  Audience
		want bool
	}{
		{"zero value", Audience{}, true},
		{"nil slices", Audience{Users: nil, Depts: nil}, true},
		{"empty slices", Audience{Users: []string{}, Depts: []string{}}, true},
		{"user only", Audience{Users: []string{"usr-1"}}, false},
		{"dept only", Audience{Depts: []string{"dept-1"}}, false},
		{"both users and depts", Audience{Users: []string{"usr-1"}, Depts: []string{"dept-1"}}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.aud.empty(); got != tc.want {
				t.Fatalf("Audience.empty() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestMayAdmin tests that administrators are unconditionally granted full access.
func TestMayAdmin(t *testing.T) {
	ctx := context.Background()
	s := &Store{} // nil db; admin check does not query the database

	for _, uid := range []string{"admin-1", "user-2", ""} {
		use, manage, err := s.May(ctx, uid, true)
		if err != nil || !use || !manage {
			t.Fatalf("May(%q, true) = use:%v, manage:%v, err:%v; want true, true, nil", uid, use, manage, err)
		}
	}
}
