package apps

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	id, sec := strings.Repeat("a", 12), strings.Repeat("b", 64)
	if gotID, gotSec, ok := parse(accessPrefix+id+"_"+sec, accessPrefix); !ok || gotID != id || gotSec != sec {
		t.Fatalf("good token rejected: %v %q %q", ok, gotID, gotSec)
	}
	for _, bad := range []string{"", accessPrefix, refreshPrefix + id + "_" + sec, "go24_" + id + "_" + sec,
		accessPrefix + id[:11] + "_" + sec, accessPrefix + strings.Repeat("z", 12) + "_" + sec, accessPrefix + id + "_" + sec[:63]} {
		if _, _, ok := parse(bad, accessPrefix); ok {
			t.Errorf("bad token accepted: %q", bad)
		}
	}
	if !IsToken(accessPrefix+id+"_"+sec) || IsToken("go24_"+id+"_"+sec) || IsToken(refreshPrefix+id+"_"+sec) {
		t.Fatal("IsToken")
	}
	if MemberID("https://go24/") != MemberID("https://go24") || len(MemberID("https://go24")) != 32 {
		t.Fatal("MemberID")
	}
}
