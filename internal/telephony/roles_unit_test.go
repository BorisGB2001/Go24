package telephony

import (
	"testing"
)

func TestAllPerms(t *testing.T) {
	all := AllPerms()
	expected := map[string]string{
		"calls":    "all",
		"records":  "all",
		"outgoing": "all",
		"settings": "all",
		"monitor":  "all",
	}
	for k, want := range expected {
		if got := all[k]; got != want {
			t.Errorf("AllPerms()[%q] = %q, want %q", k, got, want)
		}
	}
}

func TestPermsClean(t *testing.T) {
	dirty := Perms{
		"calls":       "dept",
		"records":     "invalid_level", // invalid -> reset to "none"
		"outgoing":    "crm",
		"unknown_key": "all", // unknown key -> dropped
	}
	cleaned := dirty.clean()

	if cleaned["calls"] != "dept" {
		t.Errorf("cleaned calls = %q, want dept", cleaned["calls"])
	}
	if cleaned["records"] != "none" {
		t.Errorf("cleaned records = %q, want none", cleaned["records"])
	}
	if cleaned["outgoing"] != "crm" {
		t.Errorf("cleaned outgoing = %q, want crm", cleaned["outgoing"])
	}
	if cleaned["settings"] != "none" {
		t.Errorf("cleaned settings = %q, want none", cleaned["settings"])
	}
	if cleaned["monitor"] != "none" {
		t.Errorf("cleaned monitor = %q, want none", cleaned["monitor"])
	}
	if _, ok := cleaned["unknown_key"]; ok {
		t.Errorf("cleaned should drop unknown_key, got %v", cleaned)
	}
}

func TestPermsHas(t *testing.T) {
	p := Perms{
		"calls":    "dept",
		"records":  "own",
		"outgoing": "none",
		"settings": "all",
		"monitor":  "none",
	}

	tests := []struct {
		key   string
		level string
		want  bool
	}{
		// calls level is "dept" (none < own < dept < all)
		{"calls", "none", true},
		{"calls", "own", true},
		{"calls", "dept", true},
		{"calls", "all", false},

		// records level is "own"
		{"records", "none", true},
		{"records", "own", true},
		{"records", "dept", false},
		{"records", "all", false},

		// outgoing level is "none"
		{"outgoing", "none", true},
		{"outgoing", "crm", false},
		{"outgoing", "all", false},

		// settings level is "all"
		{"settings", "none", true},
		{"settings", "all", true},

		// unknown right key -> false
		{"non_existent_key", "all", false},
		{"non_existent_key", "none", false},
	}

	for _, tc := range tests {
		t.Run(tc.key+":"+tc.level, func(t *testing.T) {
			if got := p.Has(tc.key, tc.level); got != tc.want {
				t.Errorf("Perms.Has(%q, %q) = %v, want %v", tc.key, tc.level, got, tc.want)
			}
		})
	}
}

func TestPermsHigher(t *testing.T) {
	p1 := Perms{
		"calls":    "own",
		"records":  "dept",
		"outgoing": "crm",
		"settings": "none",
		"monitor":  "dept",
	}

	p2 := Perms{
		"calls":    "dept", // higher than own
		"records":  "own",  // lower than dept
		"outgoing": "all",  // higher than crm
		"settings": "all",  // higher than none
		"monitor":  "none", // lower than dept
	}

	p1.higher(p2)

	expected := Perms{
		"calls":    "dept",
		"records":  "dept",
		"outgoing": "all",
		"settings": "all",
		"monitor":  "dept",
	}

	for k, want := range expected {
		if got := p1[k]; got != want {
			t.Errorf("p1.higher[%q] = %q, want %q", k, got, want)
		}
	}
}
