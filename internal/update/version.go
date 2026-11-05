// Package update is «Настройки → Обновление»: go24 learns of a new release on GitHub, fetches its signed archive and
// asks go24-updater (the same image, «go24 updater», with the Docker socket) to install it — the files, the settings,
// the images of the components — and to roll back to the previous version with its database when it does not start.
package update

import (
	"strconv"
	"strings"
)

// Version is X.Y.Z with an optional suffix (0.6.0-dev): a version with a suffix comes before the same one without.
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

// ParseVersion reads «0.5.7», «v0.5.7» or «0.6.0-dev».
func ParseVersion(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v Version
	if i := strings.IndexByte(s, '-'); i >= 0 {
		s, v.Pre = s[:i], s[i+1:]
		if v.Pre == "" {
			return v, false
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	nums := []*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return v, false
		}
		*nums[i] = n
	}
	return v, true
}

// Compare is -1, 0 or 1 as v is older than, the same as or newer than o.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	case v.Pre < o.Pre:
		return -1
	}
	return 1
}

func (v Version) String() string {
	s := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Newer says whether version a is newer than b; a version that is not X.Y.Z is never newer.
func Newer(a, b string) bool {
	va, ok := ParseVersion(a)
	if !ok {
		return false
	}
	vb, ok := ParseVersion(b)
	return !ok || va.Compare(vb) > 0
}

// Dev says whether a version is a development build (not X.Y.Z of a release): its code is not from GitHub, and a
// release must not be installed over it.
func Dev(v string) bool {
	p, ok := ParseVersion(v)
	return !ok || p.Pre != ""
}
