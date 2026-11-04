package theme

import "testing"

// TestCheck: a known mode and background; a picture of one's own only with one; another background drops it.
func TestCheck(t *testing.T) {
	for _, c := range []struct {
		in Theme
		ok bool
	}{
		{Theme{}, true},
		{Theme{Mode: "dark", Bg: "sky"}, true},
		{Theme{Mode: "light", Bg: "aurora"}, true},
		{Theme{Mode: "sepia"}, false},
		{Theme{Bg: "stars"}, false},
		{Theme{Bg: Custom}, false},
		{Theme{Bg: Custom, Image: "go24/themes/u/1.png", ImageType: "image/png"}, true},
	} {
		if _, err := Check(c.in); (err == nil) != c.ok {
			t.Errorf("Check(%+v) = %v", c.in, err)
		}
	}
	if got, _ := Check(Theme{Bg: "mint", Image: "x", ImageType: "image/png"}); got.Image != "" || got.ImageType != "" {
		t.Errorf("a picture kept with go24's background: %+v", got)
	}
}
