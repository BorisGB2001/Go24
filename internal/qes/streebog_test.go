package qes

import (
	"encoding/hex"
	"hash"
	"testing"
)

// The examples of GOST R 34.11-2012 (digests in the order CMS keeps them) and a message overflowing Σ.
func TestStreebog(t *testing.T) {
	for _, c := range []struct {
		msg  string
		new  func() hash.Hash
		want string
	}{
		{"303132333435363738393031323334353637383930313233343536373839303132333435363738393031323334353637383930313233343536373839303132",
			NewStreebog512, "1b54d01a4af5b9d5cc3d86d68d285462b19abc2475222f35c085122be4ba1ffa00ad30f8767b3a82384c6574f024c311e2a481332b08ef7f41797891c1646f48"},
		{"303132333435363738393031323334353637383930313233343536373839303132333435363738393031323334353637383930313233343536373839303132",
			NewStreebog256, "9d151eefd8590b89daa6ba6cb74af9275dd051026bb149a452fd84e5e57b5500"},
		{"d1e520e2e5f2f0e82c20d1f2f0e8e1eee6e820e2edf3f6e82c20e2e5fef2fa20f120eceef0ff20f1f2f0e5ebe0ece820ede020f5f0e0e1f0fbff20efebfaeafb20c8e3eef0e5e2fb",
			NewStreebog512, "1e88e62226bfca6f9994f1f2d51569e0daf8475a3b0fe61a5300eee46d961376035fe83549ada2b8620fcd7c496ce5b33f0cb9dddc2b6460143b03dabac9fb28"},
		{"d1e520e2e5f2f0e82c20d1f2f0e8e1eee6e820e2edf3f6e82c20e2e5fef2fa20f120eceef0ff20f1f2f0e5ebe0ece820ede020f5f0e0e1f0fbff20efebfaeafb20c8e3eef0e5e2fb",
			NewStreebog256, "9dd2fe4e90409e5da87f53976d7405b0c0cac628fc669a741d50063c557e8f50"},
		{"d0cf11e0a1b11ae1000000000000000000000000000000003e000300feff0900060000000000000000000000010000000100000000000000001000002400000001000000feffffff0000000000000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			NewStreebog256, "c766085540caaa8953bfcf7a1ba220619cee50d65dc242f82f23ba4b180b18e0"},
	} {
		msg, _ := hex.DecodeString(c.msg)
		h := c.new()
		h.Write(msg)
		if got := hex.EncodeToString(h.Sum(nil)); got != c.want {
			t.Errorf("streebog%d(%.16s…) = %s, want %s", h.Size()*8, c.msg, got, c.want)
		}
		h.Reset() // by pieces, after a reset — the same
		for i := range msg {
			h.Write(msg[i : i+1])
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != c.want {
			t.Errorf("streebog%d by bytes = %s", h.Size()*8, got)
		}
	}
}
