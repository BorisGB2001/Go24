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

// TestInviteGuest: a guest's invitation goes into the guests' flow; one from a common link fixes nothing (the person
// types their name and e-mail), a personal one fixes the e-mail and the name.
func TestInviteGuest(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v3/flows/instances/go24-guest-enrollment/":
			_ = json.NewEncoder(w).Encode(map[string]string{"pk": "flow-g"})
		case r.Method == "POST" && r.URL.Path == "/api/v3/stages/invitation/invitations/":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			got = append(got, m)
			_ = json.NewEncoder(w).Encode(map[string]string{"pk": "inv-g"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	inv, err := c.InviteGuest(context.Background(), "https://sso.example", "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Link != "https://sso.example/if/flow/go24-guest-enrollment/?itoken=inv-g" {
		t.Fatalf("link = %s", inv.Link)
	}
	if _, err := c.InviteGuest(context.Background(), "https://sso.example", "bob@client.example", "Боб", time.Hour); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sent %d", len(got))
	}
	open, _ := got[0]["fixed_data"].(map[string]any)
	personal, _ := got[1]["fixed_data"].(map[string]any)
	if got[0]["flow"] != "flow-g" || got[0]["single_use"] != true || len(open) != 0 || !strings.HasPrefix(got[0]["name"].(string), "go24-guest-") ||
		personal["email"] != "bob@client.example" || personal["name"] != "Боб" {
		t.Fatalf("sent = %v", got)
	}
}

// TestLeaveGroup: the user (by login, exactly) leaves the group (by name, exactly); an unknown user is nothing to do.
func TestLeaveGroup(t *testing.T) {
	var removed []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/core/users/":
			res := []map[string]any{{"pk": 3, "username": "bob2"}}
			if r.URL.Query().Get("username") == "bob" {
				res = append(res, map[string]any{"pk": 5, "username": "bob"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": res})
		case r.URL.Path == "/api/v3/core/groups/":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"pk": "g1", "name": "go24-guests"}, {"pk": "g2", "name": "go24-guests-old"}}})
		case r.Method == "POST" && r.URL.Path == "/api/v3/core/groups/g1/remove_user/":
			var m map[string]any
			_ = json.NewDecoder(r.Body).Decode(&m)
			removed = append(removed, m)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "tok")
	if err := c.LeaveGroup(context.Background(), "bob", "go24-guests"); err != nil {
		t.Fatal(err)
	}
	if err := c.LeaveGroup(context.Background(), "nobody", "go24-guests"); err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0]["pk"] != float64(5) {
		t.Fatalf("removed = %v", removed)
	}
	var none *Client
	if err := none.LeaveGroup(context.Background(), "bob", "go24-guests"); err != nil {
		t.Fatal(err)
	}
}
