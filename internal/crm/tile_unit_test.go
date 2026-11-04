package crm

import "testing"

// TestTileKnown: «Вид карточки» takes the tile fields of its kind and the custom fields that exist.
func TestTileKnown(t *testing.T) {
	for _, c := range []struct {
		scope, col string
		ok         bool
	}{
		{"deal", "deal_type", true}, {"type:5", "deal_type", false}, {"type:5", "client", true},
		{"lead", "phone", true}, {"deal", "phone", false}, {"deal", "f:7", true}, {"deal", "f:8", false},
		{"contact", "phone", false}, {"deal", "stage", false},
	} {
		if got := tileKnown(c.scope, c.col, []string{"7"}); got != c.ok {
			t.Errorf("%s %s: %v", c.scope, c.col, got)
		}
	}
}
