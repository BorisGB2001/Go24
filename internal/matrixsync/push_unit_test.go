package matrixsync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go24/internal/matrix"
)

// TestPushRulesMutedRooms tests retrieving muted room push rules via httptest without a live Matrix server.
func TestPushRulesMutedRooms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/_matrix/client/v3/pushrules/" {
			http.NotFound(w, r)
			return
		}

		// Return override rules with various states
		resp := map[string]any{
			"global": map[string]any{
				"override": []map[string]any{
					{
						"rule_id": "!muted_room:example.com",
						"enabled": true,
						"actions": []any{"dont_notify"},
					},
					{
						"rule_id": "!notified_room:example.com",
						"enabled": true,
						"actions": []any{"notify"},
					},
					{
						"rule_id": "!disabled_rule:example.com",
						"enabled": false,
						"actions": []any{},
					},
					{
						"rule_id": "rule_without_exclamation",
						"enabled": true,
						"actions": []any{},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := matrix.New(srv.URL, "example.com", "test-token")
	ctx := context.Background()

	muted, err := client.MutedRooms(ctx, "test-token")
	if err != nil {
		t.Fatalf("client.MutedRooms() error: %v", err)
	}

	// Only rooms starting with ! and enabled are parsed
	if !muted["!muted_room:example.com"] {
		t.Errorf("!muted_room:example.com should be muted (true), got %v", muted["!muted_room:example.com"])
	}
	if muted["!notified_room:example.com"] {
		t.Errorf("!notified_room:example.com has 'notify' action so should not be muted (false), got %v", muted["!notified_room:example.com"])
	}
	if _, exists := muted["!disabled_rule:example.com"]; exists {
		t.Error("disabled rule should not be in muted map")
	}
	if _, exists := muted["rule_without_exclamation"]; exists {
		t.Error("rule without exclamation should not be in muted map")
	}
}

// TestPushRulesMuteAndUnmute tests setting and removing room mute push rules.
func TestPushRulesMuteAndUnmute(t *testing.T) {
	var lastMethod, lastPath string
	var lastBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastMethod = r.Method
		lastPath = r.URL.Path

		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/_matrix/client/v3/pushrules/global/override/"):
			_ = json.NewDecoder(r.Body).Decode(&lastBody)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/_matrix/client/v3/pushrules/global/override/!unmute:example.com":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/_matrix/client/v3/pushrules/global/override/!not_found:example.com":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"Rule not found"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client := matrix.New(srv.URL, "example.com", "test-token")
	ctx := context.Background()

	// 1. Mute room (on = true): PUT override rule
	targetRoom := "!test_room:example.com"
	if err := client.Mute(ctx, "test-token", targetRoom, true); err != nil {
		t.Fatalf("Mute(true) error: %v", err)
	}
	if lastMethod != http.MethodPut || !strings.Contains(lastPath, targetRoom) {
		t.Errorf("Mute(true) called %s %s", lastMethod, lastPath)
	}
	actions, ok := lastBody["actions"].([]any)
	if !ok || len(actions) != 0 {
		t.Errorf("Mute(true) expected empty actions, got %+v", lastBody["actions"])
	}

	// 2. Unmute room (on = false): DELETE override rule
	if err := client.Mute(ctx, "test-token", "!unmute:example.com", false); err != nil {
		t.Fatalf("Mute(false) error: %v", err)
	}
	if lastMethod != http.MethodDelete {
		t.Errorf("Mute(false) expected DELETE, got %s", lastMethod)
	}

	// 3. Unmute room with M_NOT_FOUND: treated as success (nil error)
	if err := client.Mute(ctx, "test-token", "!not_found:example.com", false); err != nil {
		t.Fatalf("Mute(false) with M_NOT_FOUND should return nil, got: %v", err)
	}
}
