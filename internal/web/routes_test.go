package web

import "testing"

// TestRoutes: the patterns go together (http.ServeMux panics on two that match the same paths, and go24 would not
// start) — the other tests of Routes need a database.
func TestRoutes(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatal(r)
		}
	}()
	(&Server{}).Routes()
}
