package telephony

import (
	"strings"
	"testing"
)

func TestOutTrunks(t *testing.T) {
	tAll := Trunk{ID: "1", Name: "All Allowed", OutAll: true}
	tSpecific := Trunk{ID: "2", Name: "Specific Allowed", Out: []string{"u1", "u2"}}
	tOther := Trunk{ID: "3", Name: "Other Allowed", Out: []string{"u3"}}
	trunks := []Trunk{tAll, tSpecific, tOther}

	// u1: allowed on tAll (OutAll) and tSpecific
	u1Trunks := OutTrunks(trunks, "u1")
	if len(u1Trunks) != 2 || u1Trunks[0].ID != "1" || u1Trunks[1].ID != "2" {
		t.Errorf("OutTrunks(u1) = %+v, want [tAll, tSpecific]", u1Trunks)
	}

	// u3: allowed on tAll and tOther
	u3Trunks := OutTrunks(trunks, "u3")
	if len(u3Trunks) != 2 || u3Trunks[0].ID != "1" || u3Trunks[1].ID != "3" {
		t.Errorf("OutTrunks(u3) = %+v, want [tAll, tOther]", u3Trunks)
	}

	// u_unknown: allowed only on tAll
	uUnknownTrunks := OutTrunks(trunks, "u_unknown")
	if len(uUnknownTrunks) != 1 || uUnknownTrunks[0].ID != "1" {
		t.Errorf("OutTrunks(u_unknown) = %+v, want [tAll]", uUnknownTrunks)
	}

	// Empty trunks slice
	if got := OutTrunks(nil, "u1"); len(got) != 0 {
		t.Errorf("OutTrunks(nil) = %+v, want empty", got)
	}
}

func TestRenderForwardsDetailed(t *testing.T) {
	tr := Trunk{
		ID:     "11111111-2222-3333-4444-555555555555",
		Number: "+74950001122",
	}

	prefs := map[string]Prefs{
		"u1": {Forward: ForwardOffline, To: "+79001110001", After: 10},
		"u2": {Forward: ForwardAlways, To: "+79001110002", After: 0},
		"u3": {Forward: ForwardNever, To: "+79001110003"},               // ForwardNever -> skipped
		"u4": {Forward: ForwardNoAnswer, To: ""},                        // empty To -> skipped
		"u5": {Forward: ForwardNoAnswer, To: "+79001110005", After: 25}, // no inner number -> skipped
		"u6": {Forward: ForwardNoAnswer, To: "+79001110006", After: 15}, // has inner number 100
	}

	numbers := map[string]string{
		"u1": "102",
		"u2": "101",
		"u3": "103",
		"u4": "104",
		"u6": "100",
	}

	// u2 has an outbound trunk assigned; u1 and u6 do not
	outs := map[string]Trunk{
		"101": tr,
	}

	got := RenderForwards(prefs, numbers, outs)

	// Verify sorting: numbers are sorted: 100 (u6) < 101 (u2) < 102 (u1)
	i100 := strings.Index(got, "exten => 100,1,")
	i101 := strings.Index(got, "exten => 101,1,")
	i102 := strings.Index(got, "exten => 102,1,")

	if i100 == -1 || i101 == -1 || i102 == -1 {
		t.Fatalf("missing extensions in:\n%s", got)
	}
	if !(i100 < i101 && i101 < i102) {
		t.Errorf("extensions not sorted numerically: 100=%d, 101=%d, 102=%d", i100, i101, i102)
	}

	// u6 (100): ForwardNoAnswer, To +79001110006, After 15, no out trunk
	for _, want := range []string{
		"exten => 100,1,Set(GO24_FWM=noanswer)\n",
		"same => n,Set(GO24_FWTO=+79001110006)\n",
		"same => n,Set(GO24_FWS=15)\n",
		"same => n,Set(GO24_FWOT=)\n",
		"same => n,Set(GO24_FWON=)\n",
		"same => n,Return()\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("u6 (100) missing %q in:\n%s", want, got)
		}
	}

	// u2 (101): ForwardAlways, To +79001110002, with out trunk tr
	for _, want := range []string{
		"exten => 101,1,Set(GO24_FWM=always)\n",
		"same => n,Set(GO24_FWTO=+79001110002)\n",
		"same => n,Set(GO24_FWS=0)\n",
		"same => n,Set(GO24_FWOT=" + tr.Key() + ")\n",
		"same => n,Set(GO24_FWON=+74950001122)\n",
		"same => n,Return()\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("u2 (101) missing %q in:\n%s", want, got)
		}
	}

	// u3, u4, u5 must not appear
	for _, bad := range []string{"103", "104", "105"} {
		if strings.Contains(got, "exten => "+bad) {
			t.Errorf("extension %s should not appear in:\n%s", bad, got)
		}
	}
}
