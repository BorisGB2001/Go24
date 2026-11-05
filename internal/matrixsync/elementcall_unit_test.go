package matrixsync

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"go24/internal/matrix"
)

// TestAllowCallsOnce: at start the bot lets the members of the rooms it may change take part in calls from Element,
// page after page of the server's rooms; a room already so and one where nobody go24 may act as can change the
// levels are left alone.
func TestAllowCallsOnce(t *testing.T) {
	const bot = "@go24:t.lan"
	member := func(mx string) map[string]any {
		return map[string]any{"type": "m.room.member", "state_key": mx, "content": map[string]any{"membership": "join"}}
	}
	levels := func(users map[string]any, events map[string]any) map[string]any {
		return map[string]any{"type": "m.room.power_levels", "state_key": "", "content": map[string]any{
			"users": users, "events_default": 0, "state_default": 50, "events": events}}
	}
	state := map[string][]map[string]any{
		"!bot:t.lan":  {member(bot), levels(map[string]any{bot: 100}, map[string]any{"m.room.power_levels": 100})},
		"!done:t.lan": {member(bot), levels(map[string]any{bot: 100}, map[string]any{matrix.CallType: 0, matrix.CallMemberType: 0})},
		"!dm:t.lan":   {member("@a:t.lan"), levels(map[string]any{"@a:t.lan": 0}, map[string]any{})}, // the bot is not in it, nobody may
	}
	var mu sync.Mutex
	var put []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/_matrix/client/v3/account/whoami":
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": bot})
		case p == "/_synapse/admin/v1/rooms" && r.URL.Query().Get("from") == "":
			_ = json.NewEncoder(w).Encode(map[string]any{"rooms": []map[string]string{{"room_id": "!bot:t.lan"}, {"room_id": "!done:t.lan"}}, "next_batch": 2})
		case p == "/_synapse/admin/v1/rooms" && r.URL.Query().Get("from") == "2":
			_ = json.NewEncoder(w).Encode(map[string]any{"rooms": []map[string]string{{"room_id": "!dm:t.lan"}}})
		case strings.HasPrefix(p, "/_synapse/admin/v1/rooms/") && strings.HasSuffix(p, "/state"):
			room := strings.TrimSuffix(strings.TrimPrefix(p, "/_synapse/admin/v1/rooms/"), "/state")
			_ = json.NewEncoder(w).Encode(map[string]any{"state": state[room]})
		case r.Method == http.MethodPut && strings.Contains(p, "/state/m.room.power_levels"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			ev, _ := body["events"].(map[string]any)
			if ev[matrix.CallMemberType] != float64(0) || ev["m.room.power_levels"] != float64(100) {
				t.Errorf("the levels put: %v", body)
			}
			put = append(put, strings.Split(strings.TrimPrefix(p, "/_matrix/client/v3/rooms/"), "/")[0])
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := &Service{M: matrix.New(srv.URL, "t.lan", "bot"), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.allowCalls(context.Background())
	if len(put) != 1 || put[0] != "!bot:t.lan" {
		t.Fatalf("the rooms changed: %v", put)
	}
}
