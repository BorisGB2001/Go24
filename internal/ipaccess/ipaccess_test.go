package ipaccess

import (
	"net/netip"
	"slices"
	"testing"
)

// TestParseMatch: addresses, ranges and subnets (IPv4 and IPv6) are read one per line or by commas, written the one
// way, and matched; a wrong one is named.
func TestParseMatch(t *testing.T) {
	nets, err := Parse("192.168.1.15\n10.1.2.3/8, 172.16.0.10-172.16.0.20; 2001:db8::/32 ::ffff:8.8.8.8\n\n192.168.1.15")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.15", "10.0.0.0/8", "172.16.0.10-172.16.0.20", "2001:db8::/32", "8.8.8.8"}
	if !slices.Equal(nets, want) {
		t.Fatalf("nets %v, want %v", nets, want)
	}
	for ip, in := range map[string]bool{"192.168.1.15": true, "192.168.1.16": false, "10.255.0.1": true, "11.0.0.1": false,
		"172.16.0.10": true, "172.16.0.20": true, "172.16.0.21": false, "172.16.0.9": false, "2001:db8::1": true,
		"2001:db9::1": false, "8.8.8.8": true, "::ffff:192.168.1.15": true} {
		if got := Match(nets, netip.MustParseAddr(ip)); got != in {
			t.Errorf("%s: %v, want %v", ip, got, in)
		}
	}
	for _, bad := range []string{"300.1.1.1", "10.0.0.0/33", "10.0.0.9-10.0.0.1", "10.0.0.1-::1", "go24.lan"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}
