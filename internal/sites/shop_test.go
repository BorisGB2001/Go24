package sites

import "testing"

// TestParseCart: the cart of an order — the same product twice is added up, empty items are skipped; an empty cart,
// a strange id or quantity is refused.
func TestParseCart(t *testing.T) {
	a, b := "0e8f6c1a-1111-4a6b-9c55-2b7c1d9f0a01", "0e8f6c1a-2222-4a6b-9c55-2b7c1d9f0a02"
	got, err := ParseCart(a + ":2, ," + b + ":1," + a + ":3")
	if err != nil || len(got) != 2 || got[0] != (CartLine{a, 5}) || got[1] != (CartLine{b, 1}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"", " , ", a + ":0", a + ":-1", a + ":x", a + ":10000", "nope:1", a} {
		if _, err := ParseCart(bad); err == nil {
			t.Fatalf("%q is taken", bad)
		}
	}
	if got, _ := ParseCart(a + ":9999," + a + ":5"); got[0].Qty != maxQty {
		t.Fatalf("the sum over the most: %+v", got)
	}
}
