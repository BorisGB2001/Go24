package authentik

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInvite: the invitation is bound to the go24 enrollment flow, single-use, with the name and e-mail fixed;
// the link points at the public address.
func TestInvite(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(403)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v3/flows/instances/go24-enrollment/":
			_ = json.NewEncoder(w).Encode(map[string]string{"pk": "flow-1"})
		case r.Method == "POST" && r.URL.Path == "/api/v3/stages/invitation/invitations/":
			_ = json.NewDecoder(r.Body).Decode(&got)
			_ = json.NewEncoder(w).Encode(map[string]string{"pk": "inv-1"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	inv, err := c.Invite(context.Background(), "https://sso.example/", "ann@example.com", "Анна Петрова", 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Link != "https://sso.example/if/flow/go24-enrollment/?itoken=inv-1" || inv.Expires.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("invitation = %+v", inv)
	}
	fixed, _ := got["fixed_data"].(map[string]any)
	if got["flow"] != "flow-1" || got["single_use"] != true || fixed["email"] != "ann@example.com" || fixed["name"] != "Анна Петрова" ||
		!strings.HasPrefix(got["name"].(string), "go24-ann-at-example-com-") {
		t.Fatalf("sent = %v", got)
	}
	if _, err := New(srv.URL, "bad").Invite(context.Background(), "https://sso.example", "a@b.c", "x", time.Hour); err == nil {
		t.Fatal("a refused token must fail")
	}
}
