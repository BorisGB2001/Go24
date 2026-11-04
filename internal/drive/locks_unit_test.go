package drive

import (
	"testing"
)

func TestIsUUID(t *testing.T) {
	for _, c := range []struct {
		input string
		want  bool
	}{
		{"c0274198-469b-449e-ba1f-29df6cb8dc96", true},
		{"C0274198-469B-449E-BA1F-29DF6CB8DC96", true},
		{"00000000-0000-0000-0000-000000000000", true},
		// Missing hyphen at index 8
		{"c02741984469b-449e-ba1f-29df6cb8dc96", false},
		// Missing hyphen at index 13
		{"c0274198-469b0449e-ba1f-29df6cb8dc96", false},
		// Missing hyphen at index 18
		{"c0274198-469b-449e0ba1f-29df6cb8dc96", false},
		// Missing hyphen at index 23
		{"c0274198-469b-449e-ba1f029df6cb8dc96", false},
		// Non-hex character inside segment
		{"g0274198-469b-449e-ba1f-29df6cb8dc96", false},
		{"c0274198-469z-449e-ba1f-29df6cb8dc96", false},
		{"c0274198-469b-449e-ba1f-29df6cb8dc9!", false},
	} {
		if got := isUUID(c.input); got != c.want {
			t.Errorf("isUUID(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}
